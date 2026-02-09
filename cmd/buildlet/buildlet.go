// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The buildlet is an HTTP server that untars content to disk and runs
// commands it has untarred, streaming their output back over HTTP.
// It is part of Go's continuous build system.
//
// This program intentionally allows remote code execution, and
// provides no security of its own. It is assumed that any user uses
// it with an appropriately-configured firewall between their VM
// instances.
package main // import "golang.org/x/build/cmd/buildlet"

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/compute/metadata"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/gliderlabs/ssh"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/internal/cloud"
	"golang.org/x/build/internal/envutil"
	"golang.org/x/build/pargzip"
)

var (
	haltEntireOS     = flag.Bool("halt", true, "halt OS in /halt handler. If false, the buildlet process just ends.")
	rebootOnHalt     = flag.Bool("reboot", false, "reboot system in /halt handler.")
	workDir          = flag.String("workdir", "", "Temporary directory to use. The contents of this directory may be deleted at any time. If empty, TempDir is used to create one.")
	listenAddr       = flag.String("listen", "AUTO", "address to listen on. Unused in reverse mode. Warning: this service is inherently insecure and offers no protection of its own. Do not expose this port to the world.")
	reverseType      = flag.String("reverse-type", "", "if non-empty, go into reverse mode where the buildlet dials the coordinator instead of listening for connections. The value is the dashboard/builders.go Hosts map key, naming a HostConfig. This buildlet will receive work for any BuildConfig specifying this named HostConfig.")
	coordinator      = flag.String("coordinator", "localhost:8119", "address of coordinator, in production use farmer.golang.org. Only used in reverse mode.")
	hostname         = flag.String("hostname", "", "hostname to advertise to coordinator for reverse mode; default is actual hostname")
	healthAddr       = flag.String("health-addr", "0.0.0.0:8080", "For reverse buildlets, address to listen for /healthz requests separately from the reverse dialer to the coordinator.")
	version          = flag.Bool("version", false, "print buildlet version and exit")
	gomoteServerAddr = flag.String("gomote-server-addr", "gomotessh.golang.org:443", "Gomote server address and port")
	swarmingBot      = flag.Bool("swarming-bot", false, "start the buildlet on a swarming bot")
)

// Bump this whenever something notable happens, or when another
// component needs a certain feature. This shows on the coordinator
// per reverse client, and is also accessible via the buildlet
// package's client API (via the Status method).
//
// Notable versions:
//
//	 3: switched to revdial protocol
//	 5: reverse dialing uses timeouts+tcp keepalives, pargzip fix
//	 7: version bumps while debugging revdial hang (Issue 12816)
//	 8: mac screensaver disabled
//	11: move from self-signed cert to LetsEncrypt (Issue 16442)
//	15: ssh support
//	16: make macstadium builders always haltEntireOS
//	17: make macstadium halts use sudo
//	18: set TMPDIR and GOCACHE
//	21: GO_BUILDER_SET_GOPROXY=coordinator support
//	22: TrimSpace the reverse buildlet's gobuildkey contents
//	23: revdial v2
//	24: removeAllIncludingReadonly
//	25: use removeAllIncludingReadonly for all work area cleanup
//	26: clean up path validation and normalization
//	27: export GOPLSCACHE=$workdir/goplscache
//	28: add support for gomote server
//	29: fall back to /bin/sh when SHELL is unset
const buildletVersion = 29

func defaultListenAddr() string {
	if runtime.GOOS == "darwin" {
		// Darwin will never run on GCE, so let's always
		// listen on a high port (so we don't need to be
		// root).
		return ":5936"
	}
	// check if env is dev
	if !metadata.OnGCE() && !onEC2() {
		return "localhost:5936"
	}
	// In production, default to port 80 or 443, depending on
	// whether TLS is configured.
	if metadataValue(metaKeyTLSCert) != "" {
		return ":443"
	}
	return ":80"
}

// Functionality set non-nil by some platforms:
var (
	configureSerialLogOutput func()
	setOSRlimit              func() error
)

// If non-empty, the $TMPDIR, $GOCACHE, and $GOPLSCACHE environment
// variables to use for child processes.
var (
	processTmpDirEnv     string
	processGoCacheEnv    string
	processGoplsCacheEnv string
)

const (
	metaKeyPassword = "password"
	metaKeyTLSCert  = "tls-cert"
	metaKeyTLSkey   = "tls-key"
	windowsWorkdir  = `C:\workdir`
)

func main() {
	builderEnv := os.Getenv("GO_BUILDER_ENV")
	defer teardownOnce()
	onGCE := metadata.OnGCE()
	switch runtime.GOOS {
	case "plan9":
		if onGCE {
			log.SetOutput(&gcePlan9LogWriter{w: os.Stderr})
		}
	case "linux":
		if onGCE && !inKube {
			if w, err := os.OpenFile("/dev/console", os.O_WRONLY, 0); err == nil {
				log.SetOutput(w)
			}
		}
	case "windows":
		if onGCE {
			configureSerialLogOutput()
		}
	}

	flag.Parse()
	if *version {
		fmt.Printf("buildlet version %v (%s-%s)\n", buildletVersion, runtime.GOOS, runtime.GOARCH)
		fmt.Printf("built with %v\n", runtime.Version())
		os.Exit(0)
	}
	log.Printf("buildlet starting.")

	if builderEnv == "android-amd64-emu" {
		startAndroidEmulator()
	}

	// Optimize emphemeral filesystems. Prefer speed over safety,
	// since these VMs only last for the duration of one build.
	switch runtime.GOOS {
	case "openbsd", "freebsd", "netbsd":
		makeBSDFilesystemFast()
	}
	if setOSRlimit != nil {
		err := setOSRlimit()
		if err != nil {
			log.Fatalf("setOSRLimit: %v", err)
		}
		log.Printf("set OS rlimits.")
	}

	isReverse := *reverseType != ""

	if *listenAddr == "AUTO" && !isReverse {
		v := defaultListenAddr()
		log.Printf("Will listen on %s", v)
		*listenAddr = v
	}

	if !onGCE && !isReverse && !onEC2() && !strings.HasPrefix(*listenAddr, "localhost:") {
		log.Printf("** WARNING ***  This server is unsafe and offers no security. Be careful.")
	}
	if onGCE {
		fixMTU()
	}
	if *workDir == "" && setWorkdirToTmpfs != nil {
		setWorkdirToTmpfs()
	}
	if *workDir == "" {
		switch runtime.GOOS {
		case "windows":
			// We want a short path on Windows, due to
			// Windows issues with maximum path lengths.
			*workDir = windowsWorkdir
			if err := os.MkdirAll(*workDir, 0755); err != nil {
				log.Fatalf("error creating workdir: %v", err)
			}
		default:
			wdName := "workdir"
			if *reverseType != "" {
				wdName += "-" + *reverseType
			}
			dir := filepath.Join(os.TempDir(), wdName)
			removeAllAndMkdir(dir)
			*workDir = dir
		}
	}

	os.Setenv("WORKDIR", *workDir) // mostly for demos

	if _, err := os.Lstat(*workDir); err != nil {
		log.Fatalf("invalid --workdir %q: %v", *workDir, err)
	}

	// Set up and clean $TMPDIR and $GOCACHE directories.
	if runtime.GOOS != "plan9" { // go.dev/cl/207283 seems to indicate plan9 should work, but someone needs to test it.
		processTmpDirEnv = filepath.Join(*workDir, "tmp")
		removeAllAndMkdir(processTmpDirEnv)

		processGoCacheEnv = filepath.Join(*workDir, "gocache")
		removeAllAndMkdir(processGoCacheEnv)

		processGoplsCacheEnv = filepath.Join(*workDir, "goplscache")
		removeAllAndMkdir(processGoplsCacheEnv)
	}

	http.HandleFunc("/", handleRoot)
	http.HandleFunc("/debug/x", handleX)

	var password string
	if !isReverse {
		password = metadataValue(metaKeyPassword)
	}
	requireAuth := func(handler func(w http.ResponseWriter, r *http.Request)) http.Handler {
		return requirePasswordHandler{http.HandlerFunc(handler), password}
	}
	http.Handle("/debug/goroutines", requireAuth(handleGoroutines))
	http.Handle("/writetgz", requireAuth(handleWriteTGZ))
	http.Handle("/write", requireAuth(handleWrite))
	http.Handle("/exec", requireAuth(handleExec))
	http.Handle("/halt", requireAuth(handleHalt))
	http.Handle("/tgz", requireAuth(handleGetTGZ))
	http.Handle("/removeall", requireAuth(handleRemoveAll))
	http.Handle("/workdir", requireAuth(handleWorkDir))
	http.Handle("/status", requireAuth(handleStatus))
	http.Handle("/ls", requireAuth(handleLs))
	http.Handle("/connect-ssh", requireAuth(handleConnectSSH))
	http.HandleFunc("/healthz", handleHealthz)

	if !isReverse && !*swarmingBot {
		listenForCoordinator()
	} else {
		go func() {
			if err := serveReverseHealth(); err != nil {
				log.Printf("Error in serveReverseHealth: %v", err)
			}
		}()
		ln, err := dialServer()
		if err != nil {
			log.Fatalf("Error dialing server: %v", err)
		}
		srv := &http.Server{}
		err = srv.Serve(ln)
		log.Printf("http.Serve on reverse connection complete: %v", err)
		log.Printf("buildlet reverse mode exiting.")
		if *haltEntireOS {
			// The coordinator disconnects before doHalt has time to
			// execute. handleHalt has a 1s delay.
			time.Sleep(5 * time.Second)
		}
		os.Exit(0)
	}
}

type teardownFunc func()

var (
	tdOnce        sync.Once
	teardownOnce  func() = func() { tdOnce.Do(teardown) }
	teardownFuncs []teardownFunc
)

func teardown() {
	for _, f := range teardownFuncs {
		f()
	}
}

func dialServer() (net.Listener, error) {
	if *swarmingBot {
		return dialGomoteServer()
	}
	return dialCoordinator()
}

func listenForCoordinator() {
	tlsCert, tlsKey := metadataValue(metaKeyTLSCert), metadataValue(metaKeyTLSkey)
	if (tlsCert == "") != (tlsKey == "") {
		log.Fatalf("tls-cert and tls-key must both be supplied, or neither.")
	}

	log.Printf("Listening on %s ...", *listenAddr)
	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", *listenAddr, err)
	}
	ln = tcpKeepAliveListener{ln.(*net.TCPListener)}

	var srv http.Server
	if tlsCert != "" {
		cert, err := tls.X509KeyPair([]byte(tlsCert), []byte(tlsKey))
		if err != nil {
			log.Fatalf("TLS cert error: %v", err)
		}
		tlsConf := &tls.Config{
			Certificates: []tls.Certificate{cert},
		}
		ln = tls.NewListener(ln, tlsConf)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	signalChan := make(chan os.Signal, 1)
	if registerSignal != nil {
		registerSignal(signalChan)
	}
	select {
	case sig := <-signalChan:
		log.Printf("received signal %v; shutting down gracefully.", sig)
	case err := <-serveErr:
		log.Fatalf("Serve: %v", err)
	}
	time.AfterFunc(5*time.Second, func() {
		log.Printf("timeout shutting down gracefully; exiting immediately")
		os.Exit(1)
	})
	if err := srv.Shutdown(context.Background()); err != nil {
		log.Printf("Graceful shutdown error: %v; exiting immediately instead", err)
		os.Exit(1)
	}
	log.Printf("graceful shutdown complete.")
	os.Exit(0)
}

// registerSignal if non-nil registers shutdown signals with the provided chan.
var registerSignal func(chan<- os.Signal)

var inKube = os.Getenv("KUBERNETES_SERVICE_HOST") != ""

var (
	// ec2UD contains a copy of the EC2 vm user data retrieved from the metadata.
	ec2UD *cloud.EC2UserData
	// ec2MdC is an EC2 metadata client.
	ec2MdC *ec2metadata.EC2Metadata
)

// onEC2 evaluates if the buildlet is running on an EC2 instance.
func onEC2() bool {
	if ec2MdC != nil {
		return ec2MdC.Available()
	}
	cfg := aws.NewConfig()
	// TODO(golang/go#42604) - Improve detection of our qemu forwarded
	// metadata service for Windows ARM VMs running on EC2.
	if runtime.GOOS == "windows" && runtime.GOARCH == "arm64" {
		cfg = cfg.WithEndpoint("http://10.0.2.100:8173/latest")
	}
	ses, err := session.NewSession(cfg)
	if err != nil {
		log.Printf("unable to create aws session: %s", err)
		return false
	}
	ec2MdC = ec2metadata.New(ses, cfg)
	return ec2MdC.Available()
}

// mdValueFromUserData maps a metadata key value into the corresponding
// EC2UserData value. If a mapping is not found, an empty string is returned.
func mdValueFromUserData(ud *cloud.EC2UserData, key string) string {
	switch key {
	case metaKeyTLSCert:
		return ud.TLSCert
	case metaKeyTLSkey:
		return ud.TLSKey
	case metaKeyPassword:
		return ud.TLSPassword
	default:
		return ""
	}
}

// metadataValue returns the GCE metadata instance value for the given key.
// If the instance is on EC2 the corresponding value will be extracted from
// the user data available via the metadata.
// If the metadata is not defined, the returned string is empty.
//
// If not running on GCE or EC2, it falls back to using environment variables
// for local development.
func metadataValue(key string) string {
	// The common case (on GCE, but not in Kubernetes):
	if metadata.OnGCE() && !inKube {
		v, err := metadata.InstanceAttributeValue(key)
		if _, notDefined := err.(metadata.NotDefinedError); notDefined {
			return ""
		}
		if err != nil {
			log.Fatalf("metadata.InstanceAttributeValue(%q): %v", key, err)
		}
		return v
	}

	if onEC2() {
		if ec2UD != nil {
			return mdValueFromUserData(ec2UD, key)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ec2MetaJson, err := ec2MdC.GetUserDataWithContext(ctx)
		if err != nil {
			log.Fatalf("unable to retrieve EC2 user data: %v", err)
		}
		ec2UD = &cloud.EC2UserData{}
		err = json.Unmarshal([]byte(ec2MetaJson), ec2UD)
		if err != nil {
			log.Fatalf("unable to unmarshal user data json: %v", err)
		}
		return mdValueFromUserData(ec2UD, key)
	}

	// Else allow use of environment variables to fake
	// metadata keys, for Kubernetes pods or local testing.
	envKey := "META_" + strings.Replace(key, "-", "_", -1)
	v := os.Getenv(envKey)
	// Respect curl-style '@' prefix to mean the rest is a filename.
	if strings.HasPrefix(v, "@") {
		slurp, err := os.ReadFile(v[1:])
		if err != nil {
			log.Fatalf("Error reading file for GCEMETA_%v: %v", key, err)
		}
		return string(slurp)
	}
	if v == "" {
		log.Printf("Warning: not running on GCE, and no %v environment variable defined", envKey)
	}
	return v
}

// tcpKeepAliveListener is a net.Listener that sets TCP keep-alive
// timeouts on accepted connections.
type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (c net.Conn, err error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(3 * time.Minute)
	return tc, nil
}

func fixMTU_freebsd() error { return fixMTU_ifconfig("vtnet0") }
func fixMTU_openbsd() error { return fixMTU_ifconfig("vio0") }
func fixMTU_ifconfig(iface string) error {
	out, err := exec.Command("/sbin/ifconfig", iface, "mtu", "1460").CombinedOutput()
	if err != nil {
		return fmt.Errorf("/sbin/ifconfig %s mtu 1460: %v, %s", iface, err, out)
	}
	return nil
}

func fixMTU_plan9() error {
	f, err := os.OpenFile("/net/ipifc/0/ctl", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, "mtu 1460\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func fixMTU() {
	fn, ok := map[string]func() error{
		"openbsd": fixMTU_openbsd,
		"freebsd": fixMTU_freebsd,
		"plan9":   fixMTU_plan9,
	}[runtime.GOOS]
	if ok {
		if err := fn(); err != nil {
			log.Printf("Failed to set MTU: %v", err)
		} else {
			log.Printf("Adjusted MTU.")
		}
	}
}

// flushWriter is an io.Writer that Flushes after each Write if the
// underlying Writer implements http.Flusher.
type flushWriter struct {
	rw http.ResponseWriter
}

func (fw flushWriter) Write(p []byte) (n int, err error) {
	n, err = fw.rw.Write(p)
	if f, ok := fw.rw.(http.Flusher); ok {
		f.Flush()
	}
	return
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	fmt.Fprintf(w, "buildlet running on %s-%s\n", runtime.GOOS, runtime.GOARCH)
}

func handleGoroutines(w http.ResponseWriter, r *http.Request) {
	log.Printf("Dumping goroutines.")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	buf := make([]byte, 2<<20)
	buf = buf[:runtime.Stack(buf, true)]
	w.Write(buf)
	log.Printf("Dumped goroutines.")
}

// unauthenticated /debug/x handler, to test MTU settings.
func handleX(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.FormValue("n"))
	if n > 1<<20 {
		n = 1 << 20
	}
	log.Printf("Dumping %d X.", n)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = 'X'
	}
	w.Write(buf)
	log.Printf("Dumped X.")
}

func handleGetTGZ(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "requires GET method", http.StatusBadRequest)
		return
	}
	if !mkdirAllWorkdirOr500(w) {
		return
	}
	dir, err := nativeRelPath(r.FormValue("dir"))
	if err != nil {
		http.Error(w, "invalid 'dir' parameter: "+err.Error(), http.StatusBadRequest)
		return
	}

	zw := pargzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	base := filepath.Join(*workDir, dir)
	err = filepath.Walk(base, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(path, base)), "/")
		var linkName string
		if fi.Mode()&os.ModeSymlink != 0 {
			linkName, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		th, err := tar.FileInfoHeader(fi, linkName)
		if err != nil {
			return err
		}
		th.Name = rel
		if fi.IsDir() && !strings.HasSuffix(th.Name, "/") {
			th.Name += "/"
		}
		if th.Name == "/" {
			return nil
		}
		if err := tw.WriteHeader(th); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("Walk error: %v", err)
		panic(http.ErrAbortHandler)
	}
	tw.Close()
	zw.Close()
}

func handleWriteTGZ(w http.ResponseWriter, r *http.Request) {
	if !mkdirAllWorkdirOr500(w) {
		return
	}
	urlParam, _ := url.ParseQuery(r.URL.RawQuery)
	baseDir := *workDir
	if dir := urlParam.Get("dir"); dir != "" {
		var err error
		dir, err = nativeRelPath(dir)
		if err != nil {
			log.Printf("writetgz: bogus dir %q", dir)
			http.Error(w, "invalid 'dir' parameter: "+err.Error(), http.StatusBadRequest)
			return
		}
		baseDir = filepath.Join(baseDir, dir)

		// Special case: if the directory is "go1.4" and it already exists, do nothing.
		// This lets clients do a blind write to it and not do extra work.
		if r.Method == "POST" && dir == "go1.4" {
			if fi, err := os.Stat(baseDir); err == nil && fi.IsDir() {
				log.Printf("writetgz: skipping URL puttar to go1.4 dir; already exists")
				io.WriteString(w, "SKIP")
				return
			}
		}

		if err := os.MkdirAll(baseDir, 0755); err != nil {
			log.Printf("writetgz: %v", err)
			http.Error(w, "mkdir of base: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	var tgz io.Reader
	var urlStr string
	switch r.Method {
	case "PUT":
		tgz = r.Body
		log.Printf("writetgz: untarring Request.Body into %s", baseDir)
	case "POST":
		urlStr = r.FormValue("url")
		if urlStr == "" {
			log.Printf("writetgz: missing url POST param")
			http.Error(w, "missing url POST param", http.StatusBadRequest)
			return
		}
		t0 := time.Now()
		res, err := http.Get(urlStr)
		if err != nil {
			log.Printf("writetgz: failed to fetch tgz URL %s: %v", urlStr, err)
			http.Error(w, fmt.Sprintf("fetching URL %s: %v", urlStr, err), http.StatusInternalServerError)
			return
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			log.Printf("writetgz: failed to fetch tgz URL %s: status=%v", urlStr, res.Status)
			http.Error(w, fmt.Sprintf("writetgz: fetching provided URL %q: %s", urlStr, res.Status), http.StatusInternalServerError)
			return
		}
		tgz = res.Body
		log.Printf("writetgz: untarring %s (got headers in %v) into %s", urlStr, time.Since(t0), baseDir)
	default:
		log.Printf("writetgz: invalid method %q", r.Method)
		http.Error(w, "requires PUT or POST method", http.StatusBadRequest)
		return
	}

	err := untar(tgz, baseDir)
	if err != nil {
		http.Error(w, err.Error(), httpStatus(err))
		return
	}
	io.WriteString(w, "OK")
}

func handleWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		http.Error(w, "requires POST method", http.StatusBadRequest)
		return
	}

	param, _ := url.ParseQuery(r.URL.RawQuery)

	path := param.Get("path")
	if _, err := nativeRelPath(path); err != nil {
		http.Error(w, "invalid 'path' parameter: "+err.Error(), http.StatusBadRequest)
		return
	}
	path = filepath.FromSlash(path)
	path = filepath.Join(*workDir, path)

	modeInt, err := strconv.ParseInt(param.Get("mode"), 10, 64)
	mode := os.FileMode(modeInt)
	if err != nil || !mode.IsRegular() {
		http.Error(w, "bad mode", http.StatusBadRequest)
		return
	}

	// Make the parent directory, along with any necessary parents, if needed.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := writeFile(r.Body, path, mode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	io.WriteString(w, "OK")
}

func writeFile(r io.Reader, path string, mode os.FileMode) error {
	if runtime.GOOS == "darwin" && mode&0111 != 0 {
		// The darwin kernel caches binary signatures and SIGKILLs
		// binaries with mismatched signatures. Overwriting a binary
		// with O_TRUNC does not clear the cache, rendering the new
		// copy unusable. Removing the original file first does clear
		// the cache. See #54132.
		err := os.Remove(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	// Try to set the mode again, in case the file already existed.
	if runtime.GOOS != "windows" {
		if err := f.Chmod(mode); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// untar reads the gzip-compressed tar file from r and writes it into dir.
func untar(r io.Reader, dir string) (err error) {
	t0 := time.Now()
	nFiles := 0
	madeDir := map[string]bool{}
	defer func() {
		td := time.Since(t0)
		if err == nil {
			log.Printf("extracted tarball into %s: %d files, %d dirs (%v)", dir, nFiles, len(madeDir), td)
		} else {
			log.Printf("error extracting tarball into %s after %d files, %d dirs, %v: %v", dir, nFiles, len(madeDir), td, err)
		}
	}()
	zr, err := gzip.NewReader(r)
	if err != nil {
		return badRequestf("requires gzip-compressed body: %w", err)
	}
	tr := tar.NewReader(zr)
	loggedChtimesError := false
	for {
		f, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("tar reading error: %v", err)
			return badRequestf("tar error: %w", err)
		}
		if f.Typeflag == tar.TypeXGlobalHeader {
			// golang.org/issue/22748: git archive exports
			// a global header ('g') which after Go 1.9
			// (for a bit?) contained an empty filename.
			// Ignore it.
			continue
		}
		rel, err := nativeRelPath(f.Name)
		if err != nil {
			return badRequestf("tar file contained invalid name %q: %v", f.Name, err)
		}
		abs := filepath.Join(dir, rel)

		fi := f.FileInfo()
		mode := fi.Mode()
		switch {
		case mode.IsRegular():
			// Make the directory. This is redundant because it should
			// already be made by a directory entry in the tar
			// beforehand. Thus, don't check for errors; the next
			// write will fail with the same error.
			dir := filepath.Dir(abs)
			if !madeDir[dir] {
				if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
					return err
				}
				madeDir[dir] = true
			}
			if runtime.GOOS == "darwin" && mode&0111 != 0 {
				// See comment in writeFile.
				err := os.Remove(abs)
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			wf, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE|os.O_TRUNC, mode.Perm())
			if err != nil {
				return err
			}
			n, err := io.Copy(wf, tr)
			if closeErr := wf.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
			if err != nil {
				return fmt.Errorf("error writing to %s: %v", abs, err)
			}
			if n != f.Size {
				return fmt.Errorf("only wrote %d bytes to %s; expected %d", n, abs, f.Size)
			}
			modTime := f.ModTime
			if modTime.After(t0) {
				// Clamp modtimes at system time. See
				// golang.org/issue/19062 when clock on
				// buildlet was behind the gitmirror server
				// doing the git-archive.
				modTime = t0
			}
			if !modTime.IsZero() {
				if err := os.Chtimes(abs, modTime, modTime); err != nil && !loggedChtimesError {
					// benign error. Gerrit doesn't even set the
					// modtime in these, and we don't end up relying
					// on it anywhere (the gomote push command relies
					// on digests only), so this is a little pointless
					// for now.
					log.Printf("error changing modtime: %v (further Chtimes errors suppressed)", err)
					loggedChtimesError = true // once is enough
				}
			}
			nFiles++
		case mode.IsDir():
			if err := os.MkdirAll(abs, 0755); err != nil {
				return err
			}
			madeDir[abs] = true
		case mode&os.ModeSymlink != 0:
			// TODO: ignore these for now. They were breaking x/build tests.
			// Implement these if/when we ever have a test that needs them.
			// But maybe we'd have to skip creating them on Windows for some builders
			// without permissions.
		default:
			return badRequestf("tar file entry %s contained unsupported file type %v", f.Name, mode)
		}
	}
	return nil
}

// Process-State is an HTTP Trailer set in the /exec handler to "ok"
// on success, or os.ProcessState.String() on failure.
const hdrProcessState = "Process-State"

func handleExec(w http.ResponseWriter, r *http.Request) {
	cn := w.(http.CloseNotifier)
	clientGone := cn.CloseNotify()
	handlerDone := make(chan bool)
	defer close(handlerDone)

	if r.Method != "POST" {
		http.Error(w, "requires POST method", http.StatusBadRequest)
		return
	}
	if r.ProtoMajor*10+r.ProtoMinor < 11 {
		// We need trailers, only available in HTTP/1.1 or HTTP/2.
		http.Error(w, "HTTP/1.1 or higher required", http.StatusBadRequest)
		return
	}
	// Create *workDir and any needed temporary subdirectories.
	if !mkdirAllWorkdirOr500(w) {
		return
	}
	for _, dir := range []string{processTmpDirEnv, processGoCacheEnv, processGoplsCacheEnv} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := checkAndroidEmulator(); err != nil {
		http.Error(w, "android emulator not running: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Trailer", hdrProcessState) // declare it so we can set it

	sysMode := r.FormValue("mode") == "sys"
	debug, _ := strconv.ParseBool(r.FormValue("debug"))

	absCmd, err := absExecCmd(r.FormValue("cmd"), sysMode) // required
	if err != nil {
		http.Error(w, "invalid 'cmd' parameter: "+err.Error(), httpStatus(err))
		return
	}

	absDir, err := absExecDir(r.FormValue("dir"), sysMode, filepath.Dir(absCmd)) // optional
	if err != nil {
		http.Error(w, "invalid 'dir' parameter: "+err.Error(), httpStatus(err))
		return
	}

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	postEnv := r.PostForm["env"]

	goarch := "amd64" // unless we find otherwise
	if v := envutil.Get(runtime.GOOS, postEnv, "GOARCH"); v != "" {
		goarch = v
	}
	if v, _ := strconv.ParseBool(envutil.Get(runtime.GOOS, postEnv, "GO_DISABLE_OUTBOUND_NETWORK")); v {
		disableOutboundNetwork()
	}

	env := append(baseEnv(goarch), postEnv...)
	if v := processTmpDirEnv; v != "" {
		env = append(env, "TMPDIR="+v)
	}
	if v := processGoCacheEnv; v != "" {
		env = append(env, "GOCACHE="+v)
	}
	if v := processGoplsCacheEnv; v != "" {
		env = append(env, "GOPLSCACHE="+v)
	}
	if path := r.PostForm["path"]; len(path) > 0 {
		if kv, ok := pathEnv(runtime.GOOS, env, path, *workDir); ok {
			env = append(env, kv)
		}
	}
	env = envutil.Dedup(runtime.GOOS, env)

	var cmd *exec.Cmd
	if needsBashWrapper(absCmd) {
		cmd = exec.Command("bash", absCmd)
	} else {
		cmd = exec.Command(absCmd)
	}
	cmd.Args = append(cmd.Args, r.PostForm["cmdArg"]...)
	cmd.Env = env
	envutil.SetDir(cmd, absDir)
	cmdOutput := flushWriter{w}
	cmd.Stdout = cmdOutput
	cmd.Stderr = cmdOutput

	log.Printf("[%p] Running %s with args %q and env %q in dir %s",
		cmd, cmd.Path, cmd.Args, cmd.Env, cmd.Dir)

	if debug {
		fmt.Fprintf(cmdOutput, ":: Running %s with args %q and env %q in dir %s\n\n",
			cmd.Path, cmd.Args, cmd.Env, cmd.Dir)
	}

	t0 := time.Now()
	err = cmd.Start()
	if err == nil {
		go func() {
			select {
			case <-clientGone:
				err := killProcessTree(cmd.Process)
				if err != nil {
					log.Printf("Kill failed: %v", err)
				}
			case <-handlerDone:
				return
			}
		}()
		err = cmd.Wait()
	}
	state := "ok"
	if err != nil {
		if ps := cmd.ProcessState; ps != nil {
			state = ps.String()
		} else {
			state = err.Error()
		}
	}
	w.Header().Set(hdrProcessState, state)
	log.Printf("[%p] Run = %s, after %v", cmd, state, time.Since(t0))
}

// absExecCmd returns the native, absolute path corresponding to the "cmd"
// argument passed to the "exec" endpoint.
func absExecCmd(cmdArg string, sysMode bool) (absCmd string, err error) {
	if cmdArg == "" {
		return "", badRequestf("requires 'cmd' parameter")
	}

	if filepath.IsAbs(cmdArg) {
		return filepath.Clean(cmdArg), nil
	}

	relCmd, err := nativeRelPath(cmdArg)
	if err != nil {
		return "", badRequestf("invalid 'cmd' parameter: %w", err)
	}

	if strings.Contains(relCmd, string(filepath.Separator)) {
		if sysMode {
			return "", badRequestf("'sys' mode requires absolute or system 'cmd' path")
		}
		return filepath.Join(*workDir, filepath.FromSlash(cmdArg)), nil
	}

	if !sysMode {
		absCmd, err = exec.LookPath(filepath.Join(*workDir, cmdArg))
		if err == nil {
			return absCmd, nil
		}
		// Not found in workdir; treat as a system command even if sysMode is false.
	}

	absCmd, err = exec.LookPath(cmdArg)
	if err != nil {
		return "", httpError{http.StatusUnprocessableEntity, fmt.Errorf("command %q not found", cmdArg)}
	}
	return absCmd, nil
}

// absExecDir returns the native, absolute path corresponding to the "dir"
// argument passed to the "exec" endpoint.
func absExecDir(dirArg string, sysMode bool, cmdDir string) (absDir string, err error) {
	if dirArg == "" {
		if sysMode {
			return *workDir, nil
		}
		return cmdDir, nil
	}

	if filepath.IsAbs(dirArg) {
		return filepath.Clean(dirArg), nil
	}

	relDir, err := nativeRelPath(dirArg)
	if err != nil {
		return "", badRequestf("invalid 'dir' parameter: %w", err)
	}
	return filepath.Join(*workDir, relDir), nil
}

// needsBashWrapper reports whether the given command needs to
// run through bash.
func needsBashWrapper(cmd string) bool {
	if !strings.HasSuffix(cmd, ".bash") {
		return false
	}
	// The mobile platforms can't execute shell scripts directly.
	ismobile := runtime.GOOS == "android" || runtime.GOOS == "ios"
	return ismobile
}

// pathNotExist reports whether path does not exist.
func pathNotExist(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// pathEnv returns a key=value string for the system path variable
// (either PATH or path depending on the platform) with values
// substituted from env:
//   - the string "$PATH" expands to the original value of the path variable
//   - the string "$WORKDIR" expands to the provided workDir
//   - the string "$EMPTY" expands to the empty string
//
// The "ok" result reports whether kv differs from the path found in env.
func pathEnv(goos string, env, path []string, workDir string) (kv string, ok bool) {
	pathVar := "PATH"
	if goos == "plan9" {
		pathVar = "path"
	}

	orig := envutil.Get(goos, env, pathVar)
	r := strings.NewReplacer(
		"$PATH", orig,
		"$WORKDIR", workDir,
		"$EMPTY", "",
	)

	// Apply substitutions to a copy of the path argument.
	subst := make([]string, 0, len(path))
	for _, elem := range path {
		if s := r.Replace(elem); s != "" {
			subst = append(subst, s)
		}
	}
	kv = pathVar + "=" + strings.Join(subst, pathListSeparator(goos))
	v := kv[len(pathVar)+1:]
	return kv, v != orig
}

func pathListSeparator(goos string) string {
	switch goos {
	case "windows":
		return ";"
	case "plan9":
		return "\x00"
	default:
		return ":"
	}
}

var (
	defaultBootstrap     string
	defaultBootstrapOnce sync.Once
)

func baseEnv(goarch string) []string {
	var env []string
	if runtime.GOOS == "windows" {
		env = windowsBaseEnv(goarch)
	} else {
		env = os.Environ()
	}

	defaultBootstrapOnce.Do(func() {
		defaultBootstrap = filepath.Join(*workDir, "go1.4")

		// Prefer buildlet process's inherited GOROOT_BOOTSTRAP if
		// there was one and our default doesn't exist.
		if v := os.Getenv("GOROOT_BOOTSTRAP"); v != "" && v != defaultBootstrap {
			if pathNotExist(defaultBootstrap) {
				defaultBootstrap = v
			}
		}
	})
	env = append(env, "GOROOT_BOOTSTRAP="+defaultBootstrap)

	return env
}

func windowsBaseEnv(goarch string) (e []string) {
	e = append(e, "GOBUILDEXIT=1") // exit all.bat with completion status

	for _, pair := range os.Environ() {
		const pathEq = "PATH="
		if hasPrefixFold(pair, pathEq) {
			e = append(e, "PATH="+windowsPath(pair[len(pathEq):], goarch))
		} else {
			e = append(e, pair)
		}
	}
	return e
}

// hasPrefixFold is a case-insensitive strings.HasPrefix.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// windowsPath cleans the windows %PATH% environment.
// is64Bit is whether this is a windows-amd64-* builder.
// The PATH is assumed to be that of the image described in env/windows/README.
func windowsPath(old string, goarch string) string {
	vv := filepath.SplitList(old)
	newPath := make([]string, 0, len(vv))
	is64Bit := goarch != "386"

	// for windows-buildlet-v2 images
	for _, v := range vv {
		// The base VM image has both the 32-bit and 64-bit gcc installed.
		// They're both in the environment, so scrub the one
		// we don't want (TDM-GCC-64 or TDM-GCC-32).
		//
		// This is not present in arm64 images.
		if strings.Contains(v, "TDM-GCC-") {
			gcc64 := strings.Contains(v, "TDM-GCC-64")
			if is64Bit != gcc64 {
				continue
			}
		}
		newPath = append(newPath, v)
	}

	switch goarch {
	case "arm64":
		newPath = append(newPath, `C:\godep\llvm-aarch64\bin`)
	case "386":
		newPath = append(newPath, `C:\godep\gcc32\bin`)
	default:
		newPath = append(newPath, `C:\godep\gcc64\bin`)
	}

	return strings.Join(newPath, string(filepath.ListSeparator))
}

func handleHalt(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "requires POST method", http.StatusBadRequest)
		return
	}

	// Do the halt in 1 second, to give the HTTP response time to
	// complete.
	//
	// TODO(bradfitz): maybe prevent any (unlikely) future HTTP
	// requests from doing anything from this point on in the
	// remaining second.
	log.Printf("Halting in 1 second.")
	time.AfterFunc(1*time.Second, func() {
		teardownOnce()
		if *rebootOnHalt {
			doReboot()
		}
		if *haltEntireOS {
			doHalt()
		}
		log.Printf("Ending buildlet process due to halt.")
		os.Exit(0)
		return
	})
}

func doHalt() {
	log.Printf("Halting machine.")
	// Backup mechanism, if exec hangs for any reason:
	time.AfterFunc(5*time.Second, func() { os.Exit(0) })
	var err error
	switch runtime.GOOS {
	case "openbsd":
		// Quick, no fs flush, and power down:
		err = exec.Command("halt", "-q", "-n", "-p").Run()
	case "freebsd":
		// Power off (-p), via halt (-o), now.
		err = exec.Command("shutdown", "-p", "-o", "now").Run()
	case "linux":
		// Don't sync (-n), force without shutdown (-f), and power off (-p).
		err = exec.Command("/bin/halt", "-n", "-f", "-p").Run()
	case "plan9":
		err = exec.Command("fshalt").Run()
	case "darwin":
		switch os.Getenv("GO_BUILDER_ENV") {
		case "macstadium_vm", "qemu_vm":
			// Fast, sloppy, unsafe, because we're never reusing this VM again.
			err = exec.Command("/usr/bin/sudo", "/sbin/halt", "-n", "-q", "-l").Run()
		default:
			err = errors.New("not respecting -halt flag on macOS in unknown environment")
		}
	case "windows":
		err = errors.New("not respecting -halt flag on Windows in unknown environment")
		if runtime.GOARCH == "arm64" {
			err = exec.Command("shutdown", "/s").Run()
		}
	default:
		err = errors.New("no system-specific halt command run; will just end buildlet process")
	}
	log.Printf("Shutdown: %v", err)
	log.Printf("Ending buildlet process post-halt")
	os.Exit(0)
}

func doReboot() {
	log.Printf("Rebooting machine.")
	var err error
	switch runtime.GOOS {
	case "windows":
		err = exec.Command("shutdown", "/r").Run()
	default:
		err = exec.Command("reboot").Run()
	}
	log.Printf("Reboot: %v", err)
	log.Printf("Ending buildlet process post-halt")
	os.Exit(0)
}

func handleRemoveAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "requires POST method", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths := r.Form["path"]
	if len(paths) == 0 {
		http.Error(w, "requires 'path' parameter", http.StatusBadRequest)
		return
	}
	for _, p := range paths {
		if _, err := nativeRelPath(p); err != nil {
			http.Error(w, "invalid 'path' parameter: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	for _, p := range paths {
		log.Printf("Removing %s", p)
		fullDir := filepath.Join(*workDir, filepath.FromSlash(p))
		err := removeAllIncludingReadonly(fullDir)
		if p == "." && err != nil {
			// If workDir is a mountpoint and/or contains a binary
			// using it, we can get a "Device or resource busy" error.
			// See if it's now empty and ignore the error.
			if f, oerr := os.Open(*workDir); oerr == nil {
				if all, derr := f.Readdirnames(-1); derr == nil && len(all) == 0 {
					log.Printf("Ignoring fail of RemoveAll(.)")
					err = nil
				} else {
					log.Printf("Readdir = %q, %v", all, derr)
				}
				f.Close()
			} else {
				log.Printf("Failed to open workdir: %v", oerr)
			}
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

// mkdirAllWorkdirOr500 reports whether *workDir either exists or was created.
// If it returns false, it also writes an HTTP 500 error to w.
// This is used by callers to verify *workDir exists, even if it might've been
// deleted previously.
func mkdirAllWorkdirOr500(w http.ResponseWriter) bool {
	if err := os.MkdirAll(*workDir, 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return false
	}
	return true
}

func handleWorkDir(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "requires GET method", http.StatusBadRequest)
		return
	}
	fmt.Fprint(w, *workDir)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "requires GET method", http.StatusBadRequest)
		return
	}
	status := buildlet.Status{
		Version: buildletVersion,
	}
	b, err := json.Marshal(status)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(b)
}

func handleLs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "requires GET method", http.StatusBadRequest)
		return
	}

	dir := r.FormValue("dir")
	if dir != "" {
		var err error
		dir, err = nativeRelPath(dir)
		if err != nil {
			http.Error(w, "invalid 'dir' parameter: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	recursive, _ := strconv.ParseBool(r.FormValue("recursive"))
	digest, _ := strconv.ParseBool(r.FormValue("digest"))
	skip := r.Form["skip"] // '/'-separated relative dirs

	if !mkdirAllWorkdirOr500(w) {
		return
	}

	base := filepath.Join(*workDir, filepath.FromSlash(dir))
	anyOutput := false
	err := filepath.Walk(base, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(path, base)), "/")
		if rel == "" && fi.IsDir() {
			return nil
		}
		if fi.IsDir() {
			for _, v := range skip {
				if rel == v {
					return filepath.SkipDir
				}
			}
		}
		anyOutput = true
		fmt.Fprintf(w, "%s\t%s", fi.Mode(), rel)
		if fi.Mode().IsRegular() {
			fmt.Fprintf(w, "\t%d\t%s", fi.Size(), fi.ModTime().UTC().Format(time.RFC3339))
			if digest {
				if sha1, err := fileSHA1(path); err != nil {
					return err
				} else {
					io.WriteString(w, "\t"+sha1)
				}
			}
		} else if fi.Mode().IsDir() {
			io.WriteString(w, "/")
		}
		io.WriteString(w, "\n")
		if fi.IsDir() && !recursive {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		log.Printf("Walk error: %v", err)
		if anyOutput {
			// Decent way to signal failure to the caller, since it'll break
			// the chunked response, rather than have a valid EOF.
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		http.Error(w, "Walk error: "+err.Error(), 500)
		return
	}
}

func useBuildletSSHServer() bool {
	return *swarmingBot && runtime.GOOS != "plan9"
}

func handleConnectSSH(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "requires POST method", http.StatusBadRequest)
		return
	}
	if r.ContentLength != 0 {
		http.Error(w, "requires zero Content-Length", http.StatusBadRequest)
		return
	}
	sshUser := r.Header.Get("X-Go-Ssh-User")
	authKey := r.Header.Get("X-Go-Authorized-Key")
	if sshUser != "" && authKey != "" {
		if err := appendSSHAuthorizedKey(sshUser, authKey); err != nil {
			http.Error(w, "adding ssh authorized key: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	sshServerOnce.Do(startSSHServer)

	var sshConn net.Conn
	var err error

	// In theory we shouldn't need retries here at all, but the
	// startSSHServerLinux's use of sshd -D is kinda sketchy and
	// restarts the process whenever we connect to it, so in case
	// it's just down between restarts, try a few times. 5 tries
	// and 5 seconds seems plenty.
	const maxTries = 5
	for try := 1; try <= maxTries; try++ {
		sshConn, err = net.Dial("tcp", "localhost:"+sshPort())
		if err == nil {
			break
		}
		if try == maxTries {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		time.Sleep(time.Second)
	}
	defer sshConn.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		log.Printf("conn can't hijack for ssh proxy; HTTP/2 enabled by default?")
		http.Error(w, "conn can't hijack", http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		log.Printf("ssh hijack error: %v", err)
		http.Error(w, "ssh hijack error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()
	fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: ssh\r\nConnection: Upgrade\r\n\r\n")
	errc := make(chan error, 1)
	go func() {
		_, err := io.Copy(sshConn, conn)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(conn, sshConn)
		errc <- err
	}()
	<-errc
}

var buildletSSHServer *ssh.Server
var buldletAuthKeys []byte

// sshPort returns the port to use for the local SSH server.
func sshPort() string {
	// use port 2222 regardless of where the buildlet is running.
	if useBuildletSSHServer() {
		return "2222"
	}

	// runningInCOS is whether we're running under GCE's Container-Optimized OS (COS).
	const runningInCOS = runtime.GOOS == "linux" && runtime.GOARCH == "amd64"

	if runningInCOS {
		// If running in COS, we can't use port 22, as the system's sshd is already using it.
		// Our container runs in the system network namespace, not isolated as is typical
		// in Docker or Kubernetes. So use another high port. See https://golang.org/issue/26969.
		return "2200"
	}
	return "22"
}

var sshServerOnce sync.Once

// startSSHServer starts an SSH server.
func startSSHServer() {
	if useBuildletSSHServer() {
		startSSHServerSwarming()
		return
	}
	if inLinuxContainer() {
		startSSHServerLinux()
		return
	}
	if runtime.GOOS == "netbsd" {
		startSSHServerNetBSD()
		return
	}

	log.Printf("start ssh server: don't know how to start SSH server on this host type")
}

// inLinuxContainer reports whether it looks like we're on Linux running inside a container.
func inLinuxContainer() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if numProcs() >= 4 {
		// There should 1 process running (this buildlet
		// binary) if we're in Docker. Maybe 2 if something
		// else is happening. But if there are 4 or more,
		// we'll be paranoid and assuming we're running on a
		// user or host system and don't want to start an ssh
		// server.
		return false
	}
	// TODO: use a more explicit env variable or on-disk signal
	// that we're in a Go buildlet Docker image. But for now, this
	// seems to be consistently true:
	fi, err := os.Stat("/usr/local/bin/stage0")
	return err == nil && fi.Mode().IsRegular()
}

// startSSHServerLinux starts an SSH server on a Linux system.
func startSSHServerLinux() {
	log.Printf("start ssh server for linux")

	// First, create the privsep directory, otherwise we get a successful cmd.Start,
	// but this error message and then an exit:
	//    Missing privilege separation directory: /var/run/sshd
	if err := os.MkdirAll("/var/run/sshd", 0700); err != nil {
		log.Printf("creating /var/run/sshd: %v", err)
		return
	}

	// The AWS Docker images don't have ssh host keys in
	// their image, at least as of 2017-07-23. So make them first.
	// These are the types sshd -D complains about currently.
	if runtime.GOARCH == "arm" {
		for _, keyType := range []string{"rsa", "dsa", "ed25519", "ecdsa"} {
			file := "/etc/ssh/ssh_host_" + keyType + "_key"
			if _, err := os.Stat(file); err == nil {
				continue
			}
			out, err := exec.Command("/usr/bin/ssh-keygen", "-f", file, "-N", "", "-t", keyType).CombinedOutput()
			log.Printf("ssh-keygen of type %s: err=%v, %s\n", keyType, err, out)
		}
	}

	go func() {
		for {
			// TODO: using sshd -D isn't great as it only
			// handles a single connection and exits.
			// Maybe run in sshd -i (inetd) mode instead,
			// and hook that up to the buildlet directly?
			t0 := time.Now()
			cmd := exec.Command("/usr/sbin/sshd", "-D", "-p", sshPort(), "-d", "-d")
			cmd.Stderr = os.Stderr
			err := cmd.Start()
			if err != nil {
				log.Printf("starting sshd: %v", err)
				return
			}
			log.Printf("sshd started.")
			log.Printf("sshd exited: %v; restarting", cmd.Wait())
			if d := time.Since(t0); d < time.Second {
				time.Sleep(time.Second - d)
			}
		}
	}()
	waitLocalSSH()
}

func startSSHServerNetBSD() {
	cmd := exec.Command("/etc/rc.d/sshd", "start")
	err := cmd.Start()
	if err != nil {
		log.Printf("starting sshd: %v", err)
		return
	}
	log.Printf("sshd started.")
	waitLocalSSH()
}

// waitLocalSSH waits for sshd to start accepting connections.
func waitLocalSSH() {
	for i := 0; i < 40; i++ {
		time.Sleep(10 * time.Millisecond * time.Duration(i+1))
		c, err := net.Dial("tcp", "localhost:"+sshPort())
		if err == nil {
			c.Close()
			log.Printf("sshd connected.")
			return
		}
	}
	log.Printf("timeout waiting for sshd to come up")
}

func numProcs() int {
	n := 0
	fis, _ := os.ReadDir("/proc")
	for _, fi := range fis {
		if _, err := strconv.Atoi(fi.Name()); err == nil {
			n++
		}
	}
	return n
}

func fileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s1 := sha1.New()
	if _, err := io.Copy(s1, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", s1.Sum(nil)), nil
}

// nativeRelPath verifies that p is a non-empty relative path
// using either slashes or the buildlet's native path separator,
// and returns it canonicalized to the native path separator.
func nativeRelPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path not provided")
	}

	if filepath.Separator != '/' && strings.Contains(p, string(filepath.Separator)) {
		clean := filepath.Clean(p)
		if filepath.IsAbs(clean) {
			return "", fmt.Errorf("path %q is not relative", p)
		}
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q refers to a parent directory", p)
		}
		if strings.HasPrefix(p, string(filepath.Separator)) || filepath.VolumeName(clean) != "" {
			// On Windows, this catches semi-relative paths like "C:" (meaning “the
			// current working directory on volume C:”) and "\windows" (meaning “the
			// windows subdirectory of the current drive letter”).
			return "", fmt.Errorf("path %q is relative to volume", p)
		}
		return p, nil
	}

	clean := path.Clean(p)
	if path.IsAbs(clean) {
		return "", fmt.Errorf("path %q is not relative", p)
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q refers to a parent directory", p)
	}
	canon := filepath.FromSlash(p)
	if filepath.VolumeName(canon) != "" {
		return "", fmt.Errorf("path %q begins with a native volume name", p)
	}
	return canon, nil
}

// An httpError wraps an error with a corresponding HTTP status code.
type httpError struct {
	statusCode int
	err        error
}

func (he httpError) Error() string   { return he.err.Error() }
func (he httpError) Unwrap() error   { return he.err }
func (he httpError) httpStatus() int { return he.statusCode }

// badRequestf returns an httpError with status 400 and an error constructed by
// formatting the given arguments.
func badRequestf(format string, args ...any) error {
	return httpError{http.StatusBadRequest, fmt.Errorf(format, args...)}
}

// httpStatus returns the httpStatus of err if it is or wraps an httpError,
// or StatusInternalServerError otherwise.
func httpStatus(err error) int {
	var he httpError
	if !errors.As(err, &he) {
		return http.StatusInternalServerError
	}
	return he.statusCode
}

// requirePasswordHandler is an http.Handler auth wrapper that enforces an
// HTTP Basic password. The username is ignored.
type requirePasswordHandler struct {
	h        http.Handler
	password string // empty means no password
}

func (h requirePasswordHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, gotPass, _ := r.BasicAuth()
	if h.password != "" && h.password != gotPass {
		http.Error(w, "invalid password", http.StatusForbidden)
		return
	}
	h.h.ServeHTTP(w, r)
}

// gcePlan9LogWriter truncates log writes to 128 bytes,
// to work around a GCE serial port bug affecting Plan 9.
type gcePlan9LogWriter struct {
	w   io.Writer
	buf []byte
}

func (pw *gcePlan9LogWriter) Write(p []byte) (n int, err error) {
	const max = 128 - len("\n\x00")
	if len(p) < max {
		return pw.w.Write(p)
	}
	if pw.buf == nil {
		pw.buf = make([]byte, max+1)
	}
	n = copy(pw.buf[:max], p)
	pw.buf[n] = '\n'
	return pw.w.Write(pw.buf[:n+1])
}

var killProcessTree = killProcessTreeUnix

func killProcessTreeUnix(p *os.Process) error {
	return p.Kill()
}

func vmwareGetInfo(key string) string {
	cmd := exec.Command("/Library/Application Support/VMware Tools/vmware-tools-daemon",
		"--cmd",
		"info-get "+key)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if strings.Contains(stderr.String(), "No value found") {
			return ""
		}
		log.Fatalf("Error running vmware-tools-daemon --cmd 'info-get %s': %v, %s\n%s", key, err, stderr.Bytes(), stdout.Bytes())
	}
	return strings.TrimSpace(stdout.String())
}

func makeBSDFilesystemFast() {
	if !metadata.OnGCE() {
		log.Printf("Not on GCE; not remounting root filesystem.")
		return
	}
	btype, err := metadata.InstanceAttributeValue("buildlet-host-type")
	if _, ok := err.(metadata.NotDefinedError); ok && len(btype) == 0 {
		log.Printf("Not remounting root filesystem due to missing buildlet-host-type metadata.")
		return
	}
	if err != nil {
		log.Printf("Not remounting root filesystem due to failure getting builder type instance metadata: %v", err)
		return
	}
	// Tested on OpenBSD, FreeBSD, and NetBSD:
	out, err := exec.Command("/sbin/mount", "-u", "-o", "async,noatime", "/").CombinedOutput()
	if err != nil {
		log.Printf("Warning: failed to remount %s root filesystem with async,noatime: %v, %s", runtime.GOOS, err, out)
		return
	}
	log.Printf("Remounted / with async,noatime.")
}

func appendSSHAuthorizedKey(sshUser, authKey string) error {
	if *swarmingBot {
		buldletAuthKeys = append(buldletAuthKeys, fmt.Appendf(nil, "%s\n%s\n", sshUser, authKey)...)
		return nil
	}
	var homeRoot string
	switch runtime.GOOS {
	case "darwin":
		homeRoot = "/Users"
	case "plan9":
		return fmt.Errorf("ssh not supported on %v", runtime.GOOS)
	case "windows":
		homeRoot = `C:\Users`
	default:
		homeRoot = "/home"
		if runtime.GOOS == "freebsd" {
			if fi, err := os.Stat("/usr/home/" + sshUser); err == nil && fi.IsDir() {
				homeRoot = "/usr/home"
			}
		}
		if sshUser == "root" {
			homeRoot = "/"
		}
	}
	sshDir := filepath.Join(homeRoot, sshUser, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(sshDir, 0700); err != nil {
		return err
	}
	authFile := filepath.Join(sshDir, "authorized_keys")
	exist, err := os.ReadFile(authFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(exist), authKey) {
		return nil
	}
	f, err := os.OpenFile(authFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%s\n", authKey); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "freebsd" {
		exec.Command("/usr/sbin/chown", "-R", sshUser, sshDir).Run()
	}
	if runtime.GOOS == "windows" {
		if res, err := exec.Command("icacls.exe", authFile, "/grant", `NT SERVICE\sshd:(R)`).CombinedOutput(); err != nil {
			return fmt.Errorf("setting permissions on authorized_keys with: %v\n%s", err, res)
		}
	}
	return nil
}

// setWorkdirToTmpfs sets the *workDir (--workdir) flag to /workdir
// if the flag is empty and /workdir is a tmpfs mount, as it is on the various
// hosts that use rundockerbuildlet.
//
// It is set non-nil on operating systems where the functionality is
// needed & available. Currently we only use it on Linux.
var setWorkdirToTmpfs func()

func initBaseUnixEnv() {
	if os.Getenv("USER") == "" {
		os.Setenv("USER", "root")
	}
	if os.Getenv("HOME") == "" {
		os.Setenv("HOME", "/root")
	}
}

// removeAllAndMkdir calls removeAllIncludingReadonly and then os.Mkdir on the given
// dir, failing the process if either step fails.
func removeAllAndMkdir(dir string) {
	if err := removeAllIncludingReadonly(dir); err != nil {
		log.Fatal(err)
	}
	if err := os.Mkdir(dir, 0755); err != nil {
		log.Fatal(err)
	}
}

// removeAllIncludingReadonly is like os.RemoveAll except that it'll
// also try to change permissions to work around permission errors
// when deleting.
func removeAllIncludingReadonly(dir string) error {
	err := os.RemoveAll(dir)
	if err == nil || !os.IsPermission(err) {
		return err
	}
	// Make a best effort (ignoring errors) attempt to make all
	// files and directories writable before we try to delete them
	// all again.
	filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		const ownerWritable = 0200
		if err != nil || fi.Mode().Perm()&ownerWritable != 0 {
			return nil
		}
		os.Chmod(path, fi.Mode().Perm()|ownerWritable)
		return nil
	})
	return os.RemoveAll(dir)
}

var (
	androidEmuDead = make(chan error) // closed on death
	androidEmuErr  error              // set prior to channel close
)

func startAndroidEmulator() {
	cmd := exec.Command("/android/sdk/emulator/emulator",
		"@android-avd",
		"-no-audio",
		"-no-window",
		"-no-boot-anim",
		"-no-snapshot-save",
		"-wipe-data", // required to prevent a hang with -no-window when recovering from a snapshot?
	)
	log.Printf("running Android emulator: %v", cmd.Args)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Fatalf("failed to start Android emulator: %v", err)
	}
	go func() {
		err := cmd.Wait()
		if err == nil {
			err = errors.New("exited without error")
		}
		androidEmuErr = err
		close(androidEmuDead)
	}()
}

// checkAndroidEmulator returns an error if this machine is an Android builder
// and the Android emulator process has exited.
func checkAndroidEmulator() error {
	select {
	case <-androidEmuDead:
		return androidEmuErr
	default:
		return nil
	}
}

var disableNetOnce sync.Once

func disableOutboundNetwork() {
	if runtime.GOOS != "linux" {
		return
	}
	disableNetOnce.Do(disableOutboundNetworkLinux)
}

func disableOutboundNetworkLinux() {
	iptables, err := exec.LookPath("iptables-legacy")
	if err != nil {
		// Some older distributions, such as Debian Stretch, don't yet have nftables,
		// so "iptables" gets us the legacy version whose rules syntax is used below.
		iptables, err = exec.LookPath("iptables")
		if err != nil {
			log.Println("disableOutboundNetworkLinux failed to find iptables:", err)
			return
		}
	}
	runOrLog(exec.Command(iptables, "-I", "OUTPUT", "2", "-m", "state", "--state", "NEW", "-d", "10.0.0.0/8", "-p", "tcp", "-j", "ACCEPT"))
	runOrLog(exec.Command(iptables, "-I", "OUTPUT", "3", "-m", "state", "--state", "NEW", "-p", "tcp", "--dport", "443", "-j", "REJECT", "--reject-with", "icmp-host-prohibited"))
	runOrLog(exec.Command(iptables, "-I", "OUTPUT", "3", "-m", "state", "--state", "NEW", "-p", "tcp", "--dport", "80", "-j", "REJECT", "--reject-with", "icmp-host-prohibited"))
	runOrLog(exec.Command(iptables, "-I", "OUTPUT", "3", "-m", "state", "--state", "NEW", "-p", "tcp", "--dport", "22", "-j", "REJECT", "--reject-with", "icmp-host-prohibited"))
}

func runOrLog(cmd *exec.Cmd) {
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("failed to run %s: %v, %s", cmd.Args, err, out)
	}
}

// handleHealthz always returns 200 OK.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintln(w, "ok")
}

// serveReverseHealth serves /healthz requests on healthAddr for
// reverse buildlets.
//
// This can be used to monitor the health of guest buildlets, such as
// the Windows ARM64 qemu guest buildlet.
func serveReverseHealth() error {
	m := &http.ServeMux{}
	m.HandleFunc("/healthz", handleHealthz)
	return http.ListenAndServe(*healthAddr, m)
}

func shell() string {
	switch runtime.GOOS {
	case "linux":
		return "bash"
	case "windows":
		return `C:\Windows\System32\cmd.exe`
	default:
		if shell := os.Getenv("SHELL"); shell != "" {
			return shell
		}
		return "/bin/sh"
	}
}
