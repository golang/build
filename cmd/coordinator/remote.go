// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16 && (linux || darwin)
// +build go1.16
// +build linux darwin

// Code related to remote buildlets. See x/build/remote-buildlet.txt

package main // import "golang.org/x/build/cmd/coordinator"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/coordinator/pool/queue"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/types"
)

var (
	remoteBuildlets = &remote.Buildlets{
		M: map[string]*remote.Buildlet{},
	}

	cleanTimer *time.Timer
)

const (
	remoteBuildletIdleTimeout   = 30 * time.Minute
	remoteBuildletCleanInterval = time.Minute
)

func init() {
	cleanTimer = time.AfterFunc(remoteBuildletCleanInterval, expireBuildlets)
}

func addRemoteBuildlet(rb *remote.Buildlet) (name string) {
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	n := 0
	for {
		name = fmt.Sprintf("%s-%s-%d", rb.User, rb.BuilderType, n)
		if _, ok := remoteBuildlets.M[name]; ok {
			n++
		} else {
			remoteBuildlets.M[name] = rb
			return name
		}
	}
}

func isGCERemoteBuildlet(instName string) bool {
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	for _, rb := range remoteBuildlets.M {
		if rb.Buildlet().GCEInstanceName() == instName {
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
	for name, rb := range remoteBuildlets.M {
		if !rb.Expires.IsZero() && rb.Expires.Before(now) {
			go rb.Buildlet().Close()
			delete(remoteBuildlets.M, name)
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

	recordBuildletCreate(r.Context(), builderType)

	wantStream := false // streaming JSON updates, one JSON message (type msg) per line
	if clientVersion >= buildlet.GomoteCreateStreamVersion {
		wantStream = true
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.(http.Flusher).Flush()
	}

	si := &queue.SchedItem{
		HostType:  bconf.HostType,
		IsGomote:  true,
		IsRelease: user == "user-relui",
		User:      user,
	}

	ctx := r.Context()

	// ticker for sending status updates to client
	var ticker <-chan time.Time
	if wantStream {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		ticker = t.C
	}

	resc := make(chan buildlet.Client)
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
		Buildlet *remote.Buildlet          `json:"buildlet,omitempty"`
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
			st := sched.WaiterState(si)
			sendJSONLine(msg{Status: &st})
		case bc := <-resc:
			now := timeNow()
			rb := &remote.Buildlet{
				User:        user,
				BuilderType: builderType,
				HostType:    bconf.HostType,
				Created:     now,
				Expires:     now.Add(remoteBuildletIdleTimeout),
			}
			rb.SetBuildlet(bc)
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
	res := make([]*remote.Buildlet, 0) // so it's never JSON "null"
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()
	user, _, _ := r.BasicAuth()
	for _, rb := range remoteBuildlets.M {
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

type byBuildletName []*remote.Buildlet

func (s byBuildletName) Len() int           { return len(s) }
func (s byBuildletName) Less(i, j int) bool { return s[i].Name < s[j].Name }
func (s byBuildletName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func remoteBuildletStatus() string {
	remoteBuildlets.Lock()
	defer remoteBuildlets.Unlock()

	if len(remoteBuildlets.M) == 0 {
		return "<i>(none)</i>"
	}

	var buf bytes.Buffer
	var all []*remote.Buildlet
	for _, rb := range remoteBuildlets.M {
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
	rb, ok := remoteBuildlets.M[buildletName]
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
		err := rb.Buildlet().Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		rb.Buildlet().Close()
		remoteBuildlets.Lock()
		delete(remoteBuildlets.M, buildletName)
		remoteBuildlets.Unlock()
		return
	}

	if r.Method == "POST" && r.URL.Path == "/tcpproxy" {
		recordGomoteRDPUsage(r.Context())
		proxyBuildletTCP(w, r, rb)
		return
	}

	outReq, err := http.NewRequest(r.Method, rb.Buildlet().URL()+r.URL.Path+"?"+r.URL.RawQuery, r.Body)
	if err != nil {
		log.Printf("bad proxy request: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outReq.Header = r.Header
	outReq.ContentLength = r.ContentLength
	proxy := &httputil.ReverseProxy{
		Director:      func(*http.Request) {}, // nothing
		Transport:     rb.Buildlet().ProxyRoundTripper(),
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
func proxyBuildletTCP(w http.ResponseWriter, r *http.Request, rb *remote.Buildlet) {
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
	ip, _, err := net.SplitHostPort(rb.Buildlet().IPPort())
	if err != nil {
		http.Error(w, fmt.Sprintf("unexpected backend ip:port %q", rb.Buildlet().IPPort()), http.StatusInternalServerError)
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
