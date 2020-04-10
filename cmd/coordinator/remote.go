// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

// Code related to remote buildlets. See x/build/remote-buildlet.txt

package main // import "golang.org/x/build/cmd/coordinator"

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/gliderlabs/ssh"
	"github.com/kr/pty"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/gophers"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/types"
	gossh "golang.org/x/crypto/ssh"
)

var (
	remoteBuildlets = struct {
		sync.Mutex
		m map[string]*remoteBuildlet // keyed by buildletName
	}{m: map[string]*remoteBuildlet{}}

	cleanTimer *time.Timer
)

const (
	remoteBuildletIdleTimeout   = 30 * time.Minute
	remoteBuildletCleanInterval = time.Minute
)

func init() {
	cleanTimer = time.AfterFunc(remoteBuildletCleanInterval, expireBuildlets)
}

type remoteBuildlet struct {
	User        string // "user-foo" build key
	Name        string // dup of key
	HostType    string
	BuilderType string // default builder config to use if not overwritten
	Created     time.Time
	Expires     time.Time

	buildlet *buildlet.Client
}

// renew renews rb's idle timeout if ctx hasn't expired.
// renew should run in its own goroutine.
func (rb *remoteBuildlet) renew(ctx context.Context) {
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	select {
	case <-ctx.Done():
		return
	default:
	}
	if got := remoteBuildlets.m[rb.Name]; got == rb {
		rb.Expires = time.Now().Add(remoteBuildletIdleTimeout)
		time.AfterFunc(time.Minute, func() { rb.renew(ctx) })
	}
}

func addRemoteBuildlet(rb *remoteBuildlet) (name string) {
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	n := 0
	for {
		name = fmt.Sprintf("%s-%s-%d", rb.User, rb.BuilderType, n)
		if _, ok := remoteBuildlets.m[name]; ok {
			n++
		} else {
			remoteBuildlets.m[name] = rb
			return name
		}
	}
}

func isGCERemoteBuildlet(instName string) bool {
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	for _, rb := range remoteBuildlets.m {
		if rb.buildlet.GCEInstanceName() == instName {
			return true
		}
	}
	return false
}

func expireBuildlets() {
	defer cleanTimer.Reset(remoteBuildletCleanInterval)
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	now := time.Now()
	for name, rb := range remoteBuildlets.m {
		if !rb.Expires.IsZero() && rb.Expires.Before(now) {
			go rb.buildlet.Close()
			delete(remoteBuildlets.m, name)
		}
	}
}

var timeNow = time.Now // for testing

// always wrapped in requireBuildletProxyAuth.
func handleBuildletCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST required", 400)
		return
	}
	clientVersion := r.FormValue("version")
	if clientVersion < buildlet.GomoteCreateMinVersion {
		http.Error(w, fmt.Sprintf("gomote client version %q is too old; predates server minimum version %q", clientVersion, buildlet.GomoteCreateMinVersion), 400)
		return
	}

	builderType := r.FormValue("builderType")
	if builderType == "" {
		http.Error(w, "missing 'builderType' parameter", 400)
		return
	}
	bconf, ok := dashboard.Builders[builderType]
	if !ok {
		http.Error(w, "unknown builder type in 'builderType' parameter", 400)
		return
	}
	user, _, _ := r.BasicAuth()

	w.Header().Set("X-Supported-Version", buildlet.GomoteCreateStreamVersion)

	wantStream := false // streaming JSON updates, one JSON message (type msg) per line
	if clientVersion >= buildlet.GomoteCreateStreamVersion {
		wantStream = true
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.(http.Flusher).Flush()
	}

	si := &SchedItem{
		HostType: bconf.HostType,
		IsGomote: true,
	}

	ctx := r.Context()

	// ticker for sending status updates to client
	var ticker <-chan time.Time
	if wantStream {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		ticker = t.C
	}

	resc := make(chan *buildlet.Client)
	errc := make(chan error)

	hconf := bconf.HostConfig()

	go func() {
		bc, err := sched.GetBuildlet(ctx, si)
		if bc != nil {
			resc <- bc
		} else {
			errc <- err
		}
	}()
	// One of these fields is set:
	type msg struct {
		Error    string                    `json:"error,omitempty"`
		Buildlet *remoteBuildlet           `json:"buildlet,omitempty"`
		Status   *types.BuildletWaitStatus `json:"status,omitempty"`
	}
	sendJSONLine := func(v interface{}) {
		jenc, err := json.Marshal(v)
		if err != nil {
			log.Fatalf("remote: error marshalling JSON of type %T: %v", v, v)
		}
		jenc = append(jenc, '\n')
		w.Write(jenc)
		w.(http.Flusher).Flush()
	}
	sendText := func(s string) {
		sendJSONLine(msg{Status: &types.BuildletWaitStatus{Message: s}})
	}

	// If the gomote builder type requested is a reverse buildlet
	// and all instances are busy, try canceling a post-submit
	// build so it'll reconnect and the scheduler will give it to
	// the higher priority gomote user.
	isReverse := hconf.IsReverse
	if isReverse {
		if hs := pool.ReversePool().BuildReverseStatusJSON().HostTypes[hconf.HostType]; hs == nil {
			sendText(fmt.Sprintf("host type %q is not elastic; no machines are connected", hconf.HostType))
		} else {
			sendText(fmt.Sprintf("host type %q is not elastic; %d of %d machines connected, %d busy",
				hconf.HostType, hs.Connected, hs.Expect, hs.Busy))
			if hs.Connected > 0 && hs.Idle == 0 {
				// Try to cancel one.
				if cancelOnePostSubmitBuildWithHostType(hconf.HostType) {
					sendText(fmt.Sprintf("canceled a post-submit build on a machine of type %q; it should reconnect and get assigned to you", hconf.HostType))
				}
			}
		}
	}

	for {
		select {
		case <-ticker:
			st := sched.waiterState(si)
			sendJSONLine(msg{Status: &st})
		case bc := <-resc:
			now := timeNow()
			rb := &remoteBuildlet{
				User:        user,
				BuilderType: builderType,
				HostType:    bconf.HostType,
				buildlet:    bc,
				Created:     now,
				Expires:     now.Add(remoteBuildletIdleTimeout),
			}
			rb.Name = addRemoteBuildlet(rb)
			bc.SetName(rb.Name)
			log.Printf("created buildlet %v for %v (%s)", rb.Name, rb.User, bc.String())
			if wantStream {
				// We already sent the Content-Type
				// (and perhaps status update JSON
				// lines) earlier, so just send the
				// final JSON update with the result:
				sendJSONLine(msg{Buildlet: rb})
			} else {
				// Legacy client path.
				// TODO: delete !wantStream support 3-6 months after 2019-11-19.
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				sendJSONLine(rb)
			}
			return
		case err := <-errc:
			log.Printf("error creating gomote buildlet: %v", err)
			if wantStream {
				sendJSONLine(msg{Error: err.Error()})
			} else {
				http.Error(w, err.Error(), 500)
			}
			return
		}
	}
}

// always wrapped in requireBuildletProxyAuth.
func handleBuildletList(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET required", 400)
		return
	}
	res := make([]*remoteBuildlet, 0) // so it's never JSON "null"
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	user, _, _ := r.BasicAuth()
	for _, rb := range remoteBuildlets.m {
		if rb.User == user {
			res = append(res, rb)
		}
	}
	sort.Sort(byBuildletName(res))
	jenc, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jenc = append(jenc, '\n')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(jenc)
}

type byBuildletName []*remoteBuildlet

func (s byBuildletName) Len() int           { return len(s) }
func (s byBuildletName) Less(i, j int) bool { return s[i].Name < s[j].Name }
func (s byBuildletName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func remoteBuildletStatus() string {
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()

	if len(remoteBuildlets.m) == 0 {
		return "<i>(none)</i>"
	}

	var buf bytes.Buffer
	var all []*remoteBuildlet
	for _, rb := range remoteBuildlets.m {
		all = append(all, rb)
	}
	sort.Sort(byBuildletName(all))

	buf.WriteString("<ul>")
	for _, rb := range all {
		fmt.Fprintf(&buf, "<li><b>%s</b>, created %v ago, expires in %v</li>\n",
			html.EscapeString(rb.Name),
			time.Since(rb.Created), rb.Expires.Sub(time.Now()))
	}
	buf.WriteString("</ul>")

	return buf.String()
}

func proxyBuildletHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.Error(w, "https required", http.StatusBadRequest)
		return
	}
	buildletName := r.Header.Get("X-Buildlet-Proxy")
	if buildletName == "" {
		http.Error(w, "missing X-Buildlet-Proxy; server misconfig", http.StatusInternalServerError)
		return
	}
	remoteBuildlets.Lock()
	rb, ok := remoteBuildlets.m[buildletName]
	if ok {
		rb.Expires = time.Now().Add(remoteBuildletIdleTimeout)
	}
	remoteBuildlets.Unlock()
	if !ok {
		http.Error(w, "unknown or expired buildlet", http.StatusBadGateway)
		return
	}
	user, _, _ := r.BasicAuth()
	if rb.User != user {
		http.Error(w, "you don't own that buildlet", http.StatusUnauthorized)
		return
	}

	if r.Method == "POST" && r.URL.Path == "/halt" {
		err := rb.buildlet.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		rb.buildlet.Close()
		remoteBuildlets.Lock()
		delete(remoteBuildlets.m, buildletName)
		remoteBuildlets.Unlock()
		return
	}

	if r.Method == "POST" && r.URL.Path == "/tcpproxy" {
		proxyBuildletTCP(w, r, rb)
		return
	}

	outReq, err := http.NewRequest(r.Method, rb.buildlet.URL()+r.URL.Path+"?"+r.URL.RawQuery, r.Body)
	if err != nil {
		log.Printf("bad proxy request: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outReq.Header = r.Header
	outReq.ContentLength = r.ContentLength
	proxy := &httputil.ReverseProxy{
		Director:      func(*http.Request) {}, // nothing
		Transport:     rb.buildlet.ProxyRoundTripper(),
		FlushInterval: 500 * time.Millisecond,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("gomote proxy error for %s: %v", buildletName, err)
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "(golang.org/issue/28365): gomote proxy error: %v", err)
		},
	}
	proxy.ServeHTTP(w, outReq)
}

// proxyBuildletTCP handles connecting to and proxying between a
// backend buildlet VM's TCP port and the client. This is called once
// it's already authenticated by proxyBuildletHTTP.
func proxyBuildletTCP(w http.ResponseWriter, r *http.Request, rb *remoteBuildlet) {
	if r.ProtoMajor > 1 {
		// TODO: deal with HTTP/2 requests if https://farmer.golang.org enables it later.
		// Currently it does not, as other handlers Hijack too. We'd need to teach clients
		// when to explicitly disable HTTP/1, or update the protocols to do read/write
		// bodies instead of 101 Switching Protocols.
		http.Error(w, "unexpected HTTP/2 request", http.StatusInternalServerError)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "not a Hijacker", http.StatusInternalServerError)
		return
	}
	// The target port is a header instead of a query parameter for no real reason other
	// than being consistent with the reverse buildlet registration headers.
	port, err := strconv.Atoi(r.Header.Get("X-Target-Port"))
	if err != nil {
		http.Error(w, "invalid or missing X-Target-Port", http.StatusBadRequest)
		return
	}
	hc, ok := dashboard.Hosts[rb.HostType]
	if !ok || !hc.IsVM() {
		// TODO: implement support for non-VM types if/when needed.
		http.Error(w, fmt.Sprintf("unsupported non-VM host type %q", rb.HostType), http.StatusBadRequest)
		return
	}
	ip, _, err := net.SplitHostPort(rb.buildlet.IPPort())
	if err != nil {
		http.Error(w, fmt.Sprintf("unexpected backend ip:port %q", rb.buildlet.IPPort()), http.StatusInternalServerError)
		return
	}

	c, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to connect to port %v: %v", port, err), http.StatusInternalServerError)
		return
	}
	defer c.Close()

	// Hijack early so we can check for any unexpected buffered
	// request data without doing a potentially blocking
	// r.Body.Read. Also it's nice to be able to WriteString the
	// response header explicitly. But using w.WriteHeader+w.Flush
	// would probably also work. Somewhat arbitrary to do it early.
	cc, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, fmt.Sprintf("Hijack: %v", err), http.StatusInternalServerError)
		return
	}
	defer cc.Close()

	if buf.Reader.Buffered() != 0 {
		io.WriteString(cc, "HTTP/1.0 400 Bad Request\r\n\r\nUnexpected buffered data.\n")
		return
	}

	// If we send a 101 response with an Upgrade header and a
	// "Connection: Upgrade" header, that makes net/http's
	// *Response.isProtocolSwitch() return true, which gives us a
	// writable Response.Body on the client side, which simplifies
	// the gomote code.
	io.WriteString(cc, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: tcpproxy\r\nConnection: upgrade\r\n\r\n")

	errc := make(chan error, 2)
	// Copy from HTTP client to backend.
	go func() {
		_, err := io.Copy(c, cc)
		errc <- err
	}()
	// And copy from backend to the HTTP client.
	go func() {
		_, err := io.Copy(cc, c)
		errc <- err
	}()
	<-errc
}

func requireBuildletProxyAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			http.Error(w, "missing required authentication", 400)
			return
		}
		if !strings.HasPrefix(user, "user-") || builderKey(user) != pass {
			if *mode == "dev" {
				log.Printf("ignoring gomote authentication failure for %q in dev mode", user)
			} else {
				http.Error(w, "bad username or password", 401)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

var sshPrivateKeyFile string

func writeSSHPrivateKeyToTempFile(key []byte) (path string, err error) {
	tf, err := ioutil.TempFile("", "ssh-priv-key")
	if err != nil {
		return "", err
	}
	if err := tf.Chmod(0600); err != nil {
		return "", err
	}
	if _, err := tf.Write(key); err != nil {
		return "", err
	}
	return tf.Name(), tf.Close()
}

func listenAndServeSSH(sc *secret.Client) {
	const listenAddr = ":2222" // TODO: flag if ever necessary?
	var hostKey []byte
	var err error
	if *mode == "dev" {
		sshPrivateKeyFile = filepath.Join(os.Getenv("HOME"), "keys", "id_gomotessh_rsa")
		hostKey, err = ioutil.ReadFile(sshPrivateKeyFile)
		if os.IsNotExist(err) {
			log.Printf("SSH host key file %s doesn't exist; not running SSH server.", sshPrivateKeyFile)
			return
		}
		if err != nil {
			log.Fatal(err)
		}
	} else {
		gce := pool.NewGCEConfiguration()
		if gce.StorageClient() == nil {
			log.Printf("GCS storage client not available; not running SSH server.")
			return
		}
		r, err := gce.StorageClient().Bucket(gce.BuildEnv().BuildletBucket).Object("coordinator-gomote-ssh.key").NewReader(context.Background())
		if err != nil {
			log.Printf("Failed to read ssh host key: %v; not running SSH server.", err)
			return
		}
		hostKey, err = ioutil.ReadAll(r)
		if err != nil {
			log.Printf("Failed to read ssh host key: %v; not running SSH server.", err)
			return
		}
		sshPrivateKeyFile, err = writeSSHPrivateKeyToTempFile(hostKey)
		log.Printf("ssh: writeSSHPrivateKeyToTempFile = %v, %v", sshPrivateKeyFile, err)
		if err != nil {
			log.Printf("error writing ssh private key to temp file: %v; not running SSH server", err)
			return
		}
	}
	signer, err := gossh.ParsePrivateKey(hostKey)
	if err != nil {
		log.Printf("failed to parse SSH host key: %v; running running SSH server", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pubKey, err := sc.Retrieve(ctx, secret.NameGomoteSSHPublicKey)
	if err != nil {
		log.Fatalf("failed to get secret manager %s value", secret.NameGomoteSSHPublicKey)
	}

	ah := &sshHandlers{gomotePublicKey: pubKey}

	s := &ssh.Server{
		Addr:             listenAddr,
		Handler:          ah.handleIncomingSSHPostAuth,
		PublicKeyHandler: handleSSHPublicKeyAuth,
	}
	s.AddHostKey(signer)

	log.Printf("running SSH server on %s", listenAddr)
	err = s.ListenAndServe()
	log.Printf("SSH server ended with error: %v", err)
	// TODO: make ListenAndServe errors Fatal, once it has a proven track record. starting paranoid.
}

func handleSSHPublicKeyAuth(ctx ssh.Context, key ssh.PublicKey) bool {
	inst := ctx.User() // expected to be of form "user-USER-goos-goarch-etc"
	user := userFromGomoteInstanceName(inst)
	if user == "" {
		return false
	}
	// Map the gomote username to the github username, and use the
	// github user's public ssh keys for authentication. This is
	// mostly of laziness and pragmatism, not wanting to invent or
	// maintain a new auth mechanism or password/key registry.
	githubUser := gophers.GitHubOfGomoteUser(user)
	keys := githubPublicKeys(githubUser)
	for _, authKey := range keys {
		if ssh.KeysEqual(key, authKey.PublicKey) {
			log.Printf("for instance %q, github user %q key matched: %s", inst, githubUser, authKey.AuthorizedLine)
			return true
		}
	}
	return false
}

type sshHandlers struct {
	gomotePublicKey string
}

func (ah *sshHandlers) handleIncomingSSHPostAuth(s ssh.Session) {
	inst := s.User()
	user := userFromGomoteInstanceName(inst)

	requestedMutable := strings.HasPrefix(inst, "mutable-")
	if requestedMutable {
		inst = strings.TrimPrefix(inst, "mutable-")
	}

	ptyReq, winCh, isPty := s.Pty()
	if !isPty {
		fmt.Fprintf(s, "scp etc not yet supported; https://golang.org/issue/21140\n")
		return
	}

	if ah.gomotePublicKey == "" {
		fmt.Fprint(s, "invalid gomote-ssh-public-key")
		return
	}

	remoteBuildlets.Lock()
	rb, ok := remoteBuildlets.m[inst]
	remoteBuildlets.Unlock()
	if !ok {
		fmt.Fprintf(s, "unknown instance %q", inst)
		return
	}

	hostType := rb.HostType
	hostConf, ok := dashboard.Hosts[hostType]
	if !ok {
		fmt.Fprintf(s, "instance %q has unknown host type %q\n", inst, hostType)
		return
	}

	bconf, ok := dashboard.Builders[rb.BuilderType]
	if !ok {
		fmt.Fprintf(s, "instance %q has unknown builder type %q\n", inst, rb.BuilderType)
		return
	}

	ctx, cancel := context.WithCancel(s.Context())
	defer cancel()
	go rb.renew(ctx)

	sshUser := hostConf.SSHUsername
	useLocalSSHProxy := bconf.GOOS() != "plan9"
	if sshUser == "" && useLocalSSHProxy {
		fmt.Fprintf(s, "instance %q host type %q does not have SSH configured\n", inst, hostType)
		return
	}
	if !hostConf.IsHermetic() && !requestedMutable {
		fmt.Fprintf(s, "WARNING: instance %q host type %q is not currently\n", inst, hostType)
		fmt.Fprintf(s, "configured to have a hermetic filesystem per boot.\n")
		fmt.Fprintf(s, "You must be careful not to modify machine state\n")
		fmt.Fprintf(s, "that will affect future builds. Do you agree? If so,\n")
		fmt.Fprintf(s, "run gomote ssh --i-will-not-break-the-host <INST>\n")
		return
	}

	log.Printf("connecting to ssh to instance %q ...", inst)

	fmt.Fprintf(s, "# Welcome to the gomote ssh proxy, %s.\n", user)
	fmt.Fprintf(s, "# Connecting to/starting remote ssh...\n")
	fmt.Fprintf(s, "#\n")

	var localProxyPort int
	if useLocalSSHProxy {
		sshConn, err := rb.buildlet.ConnectSSH(sshUser, ah.gomotePublicKey)
		log.Printf("buildlet(%q).ConnectSSH = %T, %v", inst, sshConn, err)
		if err != nil {
			fmt.Fprintf(s, "failed to connect to ssh on %s: %v\n", inst, err)
			return
		}
		defer sshConn.Close()

		// Now listen on some localhost port that we'll proxy to sshConn.
		// The openssh ssh command line tool will connect to this IP.
		ln, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			fmt.Fprintf(s, "local listen error: %v\n", err)
			return
		}
		localProxyPort = ln.Addr().(*net.TCPAddr).Port
		log.Printf("ssh local proxy port for %s: %v", inst, localProxyPort)
		var lnCloseOnce sync.Once
		lnClose := func() { lnCloseOnce.Do(func() { ln.Close() }) }
		defer lnClose()

		// Accept at most one connection from localProxyPort and proxy
		// it to sshConn.
		go func() {
			c, err := ln.Accept()
			lnClose()
			if err != nil {
				return
			}
			defer c.Close()
			errc := make(chan error, 1)
			go func() {
				_, err := io.Copy(c, sshConn)
				errc <- err
			}()
			go func() {
				_, err := io.Copy(sshConn, c)
				errc <- err
			}()
			err = <-errc
		}()
	}
	workDir, err := rb.buildlet.WorkDir(ctx)
	if err != nil {
		fmt.Fprintf(s, "Error getting WorkDir: %v\n", err)
		return
	}
	ip, _, ipErr := net.SplitHostPort(rb.buildlet.IPPort())

	fmt.Fprintf(s, "# `gomote push` and the builders use:\n")
	fmt.Fprintf(s, "# - workdir: %s\n", workDir)
	fmt.Fprintf(s, "# - GOROOT: %s/go\n", workDir)
	fmt.Fprintf(s, "# - GOPATH: %s/gopath\n", workDir)
	fmt.Fprintf(s, "# - env: %s\n", strings.Join(bconf.Env(), " ")) // TODO: shell quote?
	fmt.Fprintf(s, "# Happy debugging.\n")

	log.Printf("ssh to %s: starting ssh -p %d for %s@localhost", inst, localProxyPort, sshUser)
	var cmd *exec.Cmd
	switch bconf.GOOS() {
	default:
		cmd = exec.Command("ssh",
			"-p", strconv.Itoa(localProxyPort),
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "StrictHostKeyChecking=no",
			"-i", sshPrivateKeyFile,
			sshUser+"@localhost")
	case "plan9":
		fmt.Fprintf(s, "# Plan9 user/pass: glenda/glenda123\n")
		if ipErr != nil {
			fmt.Fprintf(s, "# Failed to get IP out of %q: %v\n", rb.buildlet.IPPort(), err)
			return
		}
		cmd = exec.Command("/usr/local/bin/drawterm",
			"-a", ip, "-c", ip, "-u", "glenda", "-k", "user=glenda")
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", ptyReq.Term))
	f, err := pty.Start(cmd)
	if err != nil {
		log.Printf("running ssh client to %s: %v", inst, err)
		return
	}
	defer f.Close()
	go func() {
		for win := range winCh {
			setWinsize(f, win.Width, win.Height)
		}
	}()
	go func() {
		io.Copy(f, s) // stdin
	}()
	io.Copy(s, f) // stdout
	cmd.Process.Kill()
	cmd.Wait()
}

func setWinsize(f *os.File, w, h int) {
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSWINSZ),
		uintptr(unsafe.Pointer(&struct{ h, w, x, y uint16 }{uint16(h), uint16(w), 0, 0})))
}

// userFromGomoteInstanceName returns the username part of a gomote
// remote instance name.
//
// The instance name is of two forms. The normal form is:
//
//     user-bradfitz-linux-amd64-0
//
// The overloaded form to convey that the user accepts responsibility
// for changes to the underlying host is to prefix the same instance
// name with the string "mutable-", such as:
//
//     mutable-user-bradfitz-darwin-amd64-10_8-0
//
// The mutable part is ignored by this function.
func userFromGomoteInstanceName(name string) string {
	name = strings.TrimPrefix(name, "mutable-")
	if !strings.HasPrefix(name, "user-") {
		return ""
	}
	user := name[len("user-"):]
	hyphen := strings.IndexByte(user, '-')
	if hyphen == -1 {
		return ""
	}
	return user[:hyphen]
}

// authorizedKey is a Github user's SSH authorized key, in both string and parsed format.
type authorizedKey struct {
	AuthorizedLine string // e.g. "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILj8HGIG9NsT34PHxO8IBq0riSBv7snp30JM8AanBGoV"
	PublicKey      ssh.PublicKey
}

func githubPublicKeys(user string) []authorizedKey {
	// TODO: caching, rate limiting.
	req, err := http.NewRequest("GET", "https://github.com/"+user+".keys", nil)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("getting %s github keys: %v", user, err)
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil
	}
	var keys []authorizedKey
	bs := bufio.NewScanner(res.Body)
	for bs.Scan() {
		key, _, _, _, err := ssh.ParseAuthorizedKey(bs.Bytes())
		if err != nil {
			log.Printf("parsing github user %q key %q: %v", user, bs.Text(), err)
			continue
		}
		keys = append(keys, authorizedKey{
			PublicKey:      key,
			AuthorizedLine: strings.TrimSpace(bs.Text()),
		})
	}
	if err := bs.Err(); err != nil {
		return nil
	}
	return keys
}
