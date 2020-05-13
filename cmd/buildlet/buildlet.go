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
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/compute/metadata"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/pargzip"
)

var (
	haltEntireOS = flag.Bool("halt", true, "halt OS in /halt handler. If false, the buildlet process just ends.")
	rebootOnHalt = flag.Bool("reboot", false, "reboot system in /halt handler.")
	workDir      = flag.String("workdir", "", "Temporary directory to use. The contents of this directory may be deleted at any time. If empty, TempDir is used to create one.")
	listenAddr   = flag.String("listen", "AUTO", "address to listen on. Unused in reverse mode. Warning: this service is inherently insecure and offers no protection of its own. Do not expose this port to the world.")
	reverseType  = flag.String("reverse-type", "", "if non-empty, go into reverse mode where the buildlet dials the coordinator instead of listening for connections. The value is the dashboard/builders.go Hosts map key, naming a HostConfig. This buildlet will receive work for any BuildConfig specifying this named HostConfig.")
	coordinator  = flag.String("coordinator", "localhost:8119", "address of coordinator, in production use farmer.golang.org. Only used in reverse mode.")
	hostname     = flag.String("hostname", "", "hostname to advertise to coordinator for reverse mode; default is actual hostname")
)

// Bump this whenever something notable happens, or when another
// component needs a certain feature. This shows on the coordinator
// per reverse client, and is also accessible via the buildlet
// package's client API (via the Status method).
//
// Notable versions:
//    3: switched to revdial protocol
//    5: reverse dialing uses timeouts+tcp keepalives, pargzip fix
//    7: version bumps while debugging revdial hang (Issue 12816)
//    8: mac screensaver disabled
//   11: move from self-signed cert to LetsEncrypt (Issue 16442)
//   15: ssh support
//   16: make macstadium builders always haltEntireOS
//   17: make macstadium halts use sudo
//   18: set TMPDIR and GOCACHE
//   21: GO_BUILDER_SET_GOPROXY=coordinator support
//   22: TrimSpace the reverse buildlet's gobuildkey contents
//   23: revdial v2
//   24: removeAllIncludingReadonly
//   25: use removeAllIncludingReadonly for all work area cleanup
const buildletVersion = 25

func defaultListenAddr() string {
	if runtime.GOOS == "darwin" {
		// Darwin will never run on GCE, so let's always
		// listen on a high port (so we don't need to be
		// root).
		return ":5936"
	}
	// check if if env is dev
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
	osHalt                   func()
	configureSerialLogOutput func()
	setOSRlimit              func() error
)

// If non-empty, the $TMPDIR and $GOCACHE environment variables to use
// for child processes.
var (
	processTmpDirEnv  string
	processGoCacheEnv string
)

const (
	metaKeyPassword = "password"
	metaKeyTLSCert  = "tls-cert"
	metaKeyTLSkey   = "tls-key"
)

func main() {
	builderEnv := os.Getenv("GO_BUILDER_ENV")

	switch builderEnv {
	case "macstadium_vm":
		configureMacStadium()
	case "linux-arm-arm5spacemonkey":
		initBaseUnixEnv() // Issue 28041
	}

	onGCE := metadata.OnGCE()
	switch runtime.GOOS {
	case "plan9":
		log.SetOutput(&plan9LogWriter{w: os.Stderr})
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

	log.Printf("buildlet starting.")
	flag.Parse()

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
			*workDir = `C:\workdir`
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
	if runtime.GOOS != "windows" && runtime.GOOS != "plan9" {
		processTmpDirEnv = filepath.Join(*workDir, "tmp")
		processGoCacheEnv = filepath.Join(*workDir, "gocache")
		removeAllAndMkdir(processTmpDirEnv)
		removeAllAndMkdir(processGoCacheEnv)
	}

	initGorootBootstrap()

	http.HandleFunc("/", handleRoot)
	http.HandleFunc("/debug/goroutines", handleGoroutines)
	http.HandleFunc("/debug/x", handleX)

	var password string
	if !isReverse {
		password = metadataValue(metaKeyPassword)
	}
	requireAuth := func(handler func(w http.ResponseWriter, r *http.Request)) http.Handler {
		return requirePasswordHandler{http.HandlerFunc(handler), password}
	}
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

	if !isReverse {
		listenForCoordinator()
	} else {
		if err := dialCoordinator(); err != nil {
			log.Fatalf("Error dialing coordinator: %v", err)
		}
		log.Printf("buildlet reverse mode exiting.")
		os.Exit(0)
	}
}

var inheritedGorootBootstrap string

func initGorootBootstrap() {
	// Remember any GOROOT_BOOTSTRAP to use as a backup in handleExec
	// if $WORKDIR/go1.4 ends up not existing.
	inheritedGorootBootstrap = os.Getenv("GOROOT_BOOTSTRAP")

	// Default if not otherwise configured in dashboard/builders.go:
	os.Setenv("GOROOT_BOOTSTRAP", filepath.Join(*workDir, "go1.4"))
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
	ec2UD *buildlet.EC2UserData
	// ec2MdC is an EC2 metadata client.
	ec2MdC *ec2metadata.EC2Metadata
)

// onEC2 evaluates if the buildlet is running on an EC2 instance.
func onEC2() bool {
	if ec2MdC != nil {
		return ec2MdC.Available()
	}
	ses, err := session.NewSession()
	if err != nil {
		log.Printf("unable to create aws session: %s", err)
		return false
	}
	ec2MdC = ec2metadata.New(ses)
	return ec2MdC.Available()
}

// mdValueFromUserData maps a metadata key value into the corresponding
// EC2UserData value. If a mapping is not found, an empty string is returned.
func mdValueFromUserData(ud *buildlet.EC2UserData, key string) string {
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
		ec2UD = &buildlet.EC2UserData{}
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
		slurp, err := ioutil.ReadFile(v[1:])
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

// unauthenticated /debug/goroutines handler
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

// This is a remote code execution daemon, so security is kinda pointless, but:
func validRelativeDir(dir string) bool {
	if strings.Contains(dir, `\`) || path.IsAbs(dir) {
		return false
	}
	dir = path.Clean(dir)
	if strings.HasPrefix(dir, "../") || strings.HasSuffix(dir, "/..") || dir == ".." {
		return false
	}
	return true
}

func handleGetTGZ(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "requires GET method", http.StatusBadRequest)
		return
	}
	if !mkdirAllWorkdirOr500(w) {
		return
	}
	dir := r.FormValue("dir")
	if !validRelativeDir(dir) {
		http.Error(w, "bogus dir", http.StatusBadRequest)
		return
	}
	var zw io.WriteCloser
	if r.FormValue("pargzip") == "0" {
		zw = gzip.NewWriter(w)
	} else {
		zw = pargzip.NewWriter(w)
	}
	tw := tar.NewWriter(zw)
	base := filepath.Join(*workDir, filepath.FromSlash(dir))
	err := filepath.Walk(base, func(path string, fi os.FileInfo, err error) error {
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
		if !validRelativeDir(dir) {
			log.Printf("writetgz: bogus dir %q", dir)
			http.Error(w, "bogus dir", http.StatusBadRequest)
			return
		}
		dir = filepath.FromSlash(dir)
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
		status := http.StatusInternalServerError
		if he, ok := err.(httpStatuser); ok {
			status = he.httpStatus()
		}
		http.Error(w, err.Error(), status)
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
	if path == "" || !validRelPath(path) {
		http.Error(w, "bad path", http.StatusBadRequest)
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

	// Make the directory if it doesn't exist.
	// TODO(adg): support dirmode parameter?
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
		return badRequest("requires gzip-compressed body: " + err.Error())
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
			return badRequest("tar error: " + err.Error())
		}
		if f.Typeflag == tar.TypeXGlobalHeader {
			// golang.org/issue/22748: git archive exports
			// a global header ('g') which after Go 1.9
			// (for a bit?) contained an empty filename.
			// Ignore it.
			continue
		}
		if !validRelPath(f.Name) {
			return badRequest(fmt.Sprintf("tar file contained invalid name %q", f.Name))
		}
		rel := filepath.FromSlash(f.Name)
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
			return badRequest(fmt.Sprintf("tar file entry %s contained unsupported file type %v", f.Name, mode))
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
	// Create *workDir and (if needed) tmp and gocache.
	if !mkdirAllWorkdirOr500(w) {
		return
	}
	for _, dir := range []string{processTmpDirEnv, processGoCacheEnv} {
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

	cmdPath := r.FormValue("cmd") // required
	absCmd := cmdPath
	dir := r.FormValue("dir") // optional
	sysMode := r.FormValue("mode") == "sys"
	debug, _ := strconv.ParseBool(r.FormValue("debug"))

	if sysMode {
		if cmdPath == "" {
			http.Error(w, "requires 'cmd' parameter", http.StatusBadRequest)
			return
		}
		if dir == "" {
			dir = *workDir
		} else {
			dir = filepath.FromSlash(dir)
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(*workDir, dir)
			}
		}
	} else {
		if !validRelPath(cmdPath) {
			http.Error(w, "requires 'cmd' parameter", http.StatusBadRequest)
			return
		}
		absCmd = filepath.Join(*workDir, filepath.FromSlash(cmdPath))
		if dir == "" {
			dir = filepath.Dir(absCmd)
		} else {
			if !validRelPath(dir) {
				http.Error(w, "bogus 'dir' parameter", http.StatusBadRequest)
				return
			}
			dir = filepath.Join(*workDir, filepath.FromSlash(dir))
		}
	}

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	postEnv := r.PostForm["env"]

	goarch := "amd64" // unless we find otherwise
	if v := getEnv(postEnv, "GOARCH"); v != "" {
		goarch = v
	}
	if v, _ := strconv.ParseBool(getEnv(postEnv, "GO_DISABLE_OUTBOUND_NETWORK")); v {
		disableOutboundNetwork()
	}

	env := append(baseEnv(goarch), postEnv...)

	if v := processTmpDirEnv; v != "" {
		env = append(env, "TMPDIR="+v)
	}
	if v := processGoCacheEnv; v != "" {
		env = append(env, "GOCACHE="+v)
	}

	// Prefer buildlet process's inherited GOROOT_BOOTSTRAP if
	// there was one and the one we're about to use doesn't exist.
	if v := getEnv(env, "GOROOT_BOOTSTRAP"); v != "" && inheritedGorootBootstrap != "" && pathNotExist(v) {
		env = append(env, "GOROOT_BOOTSTRAP="+inheritedGorootBootstrap)
	}
	env = setPathEnv(env, r.PostForm["path"], *workDir)

	var cmd *exec.Cmd
	if needsBashWrapper(absCmd) {
		cmd = exec.Command("bash", absCmd)
	} else {
		cmd = exec.Command(absCmd)
	}
	cmd.Args = append(cmd.Args, r.PostForm["cmdArg"]...)
	cmd.Dir = dir
	cmdOutput := flushWriter{w}
	cmd.Stdout = cmdOutput
	cmd.Stderr = cmdOutput
	cmd.Env = env

	log.Printf("[%p] Running %s with args %q and env %q in dir %s",
		cmd, cmd.Path, cmd.Args, cmd.Env, cmd.Dir)

	if debug {
		fmt.Fprintf(cmdOutput, ":: Running %s with args %q and env %q in dir %s\n\n",
			cmd.Path, cmd.Args, cmd.Env, cmd.Dir)
	}

	t0 := time.Now()
	err := cmd.Start()
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

// needsBashWrappers reports whether the given command needs to
// run through bash.
func needsBashWrapper(cmd string) bool {
	if !strings.HasSuffix(cmd, ".bash") {
		return false
	}
	// The mobile platforms can't execute shell scripts directly.
	ismobile := runtime.GOOS == "android" || runtime.GOOS == "darwin" && (runtime.GOARCH == "arm" || runtime.GOARCH == "arm64")
	return ismobile
}

// pathNotExist reports whether path does not exist.
func pathNotExist(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func getEnv(env []string, key string) string {
	for _, kv := range env {
		if len(kv) <= len(key) || kv[len(key)] != '=' {
			continue
		}
		if runtime.GOOS == "windows" {
			// Case insensitive.
			if strings.EqualFold(kv[:len(key)], key) {
				return kv[len(key)+1:]
			}
		} else {
			// Case sensitive.
			if kv[:len(key)] == key {
				return kv[len(key)+1:]
			}
		}
	}
	return ""
}

// setPathEnv returns a copy of the provided environment with any existing
// PATH variables replaced by the user-provided path.
// These substitutions are applied to user-supplied path elements:
//   - the string "$PATH" expands to the original PATH elements
//   - the substring "$WORKDIR" expands to the provided workDir
// A path of just ["$EMPTY"] removes the PATH variable from the environment.
func setPathEnv(env, path []string, workDir string) []string {
	if len(path) == 0 {
		return env
	}

	var (
		pathIdx  = -1
		pathOrig = ""
	)

	for i, s := range env {
		if isPathEnvPair(s) {
			pathIdx = i
			pathOrig = s[len("PaTh="):] // in whatever case
			break
		}
	}
	if len(path) == 1 && path[0] == "$EMPTY" {
		// Remove existing path variable if it exists.
		if pathIdx >= 0 {
			env = append(env[:pathIdx], env[pathIdx+1:]...)
		}
		return env
	}

	// Apply substitions to a copy of the path argument.
	path = append([]string{}, path...)
	for i, s := range path {
		if s == "$PATH" {
			path[i] = pathOrig // ok if empty
		} else {
			path[i] = strings.Replace(s, "$WORKDIR", workDir, -1)
		}
	}

	// Put the new PATH in env.
	env = append([]string{}, env...)
	pathEnv := pathEnvVar() + "=" + strings.Join(path, pathSeparator())
	if pathIdx >= 0 {
		env[pathIdx] = pathEnv
	} else {
		env = append(env, pathEnv)
	}

	return env
}

// isPathEnvPair reports whether the key=value pair s represents
// the operating system's path variable.
func isPathEnvPair(s string) bool {
	// On Unix it's PATH.
	// On Plan 9 it's path.
	// On Windows it's pAtH case-insensitive.
	if runtime.GOOS == "windows" {
		return len(s) >= 5 && strings.EqualFold(s[:5], "PATH=")
	}
	if runtime.GOOS == "plan9" {
		return strings.HasPrefix(s, "path=")
	}
	return strings.HasPrefix(s, "PATH=")
}

// On Unix it's PATH.
// On Plan 9 it's path.
// On Windows it's pAtH case-insensitive.
func pathEnvVar() string {
	if runtime.GOOS == "plan9" {
		return "path"
	}
	return "PATH"
}

func pathSeparator() string {
	if runtime.GOOS == "plan9" {
		return "\x00"
	} else {
		return string(filepath.ListSeparator)
	}
}

func baseEnv(goarch string) []string {
	if runtime.GOOS == "windows" {
		return windowsBaseEnv(goarch)
	}
	return os.Environ()
}

func windowsBaseEnv(goarch string) (e []string) {
	e = append(e, "GOBUILDEXIT=1") // exit all.bat with completion status

	is64 := goarch != "386"
	for _, pair := range os.Environ() {
		const pathEq = "PATH="
		if hasPrefixFold(pair, pathEq) {
			e = append(e, "PATH="+windowsPath(pair[len(pathEq):], is64))
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
func windowsPath(old string, is64Bit bool) string {
	vv := filepath.SplitList(old)
	newPath := make([]string, 0, len(vv))

	// for windows-buildlet-v2 images
	for _, v := range vv {
		// The base VM image has both the 32-bit and 64-bit gcc installed.
		// They're both in the environment, so scrub the one
		// we don't want (TDM-GCC-64 or TDM-GCC-32).
		if strings.Contains(v, "TDM-GCC-") {
			gcc64 := strings.Contains(v, "TDM-GCC-64")
			if is64Bit != gcc64 {
				continue
			}
		}
		newPath = append(newPath, v)
	}

	// for windows-amd64-* images
	if is64Bit {
		newPath = append(newPath, `C:\godep\gcc64\bin`)
	} else {
		newPath = append(newPath, `C:\godep\gcc32\bin`)
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
	time.AfterFunc(1*time.Second, doHalt)
}

func doHalt() {
	if *rebootOnHalt {
		if err := exec.Command("reboot").Run(); err != nil {
			log.Printf("Error running reboot: %v", err)
		}
		os.Exit(0)
	}
	if !*haltEntireOS {
		log.Printf("Ending buildlet process due to halt.")
		os.Exit(0)
		return
	}
	log.Printf("Halting machine.")
	time.AfterFunc(5*time.Second, func() { os.Exit(0) })
	if osHalt != nil {
		// TODO: Windows: http://msdn.microsoft.com/en-us/library/windows/desktop/aa376868%28v=vs.85%29.aspx
		osHalt()
		os.Exit(0)
	}
	// Backup mechanism, if exec hangs for any reason:
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
		if os.Getenv("GO_BUILDER_ENV") == "macstadium_vm" {
			// Fast, sloppy, unsafe, because we're never reusing this VM again.
			err = exec.Command("/usr/bin/sudo", "/sbin/halt", "-n", "-q", "-l").Run()
		} else {
			err = errors.New("not respecting -halt flag on macOS in unknown environment")
		}
	default:
		err = errors.New("no system-specific halt command run; will just end buildlet process")
	}
	log.Printf("Shutdown: %v", err)
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
		if !validRelPath(p) {
			http.Error(w, fmt.Sprintf("bad 'path' parameter: %q", p), http.StatusBadRequest)
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
	recursive, _ := strconv.ParseBool(r.FormValue("recursive"))
	digest, _ := strconv.ParseBool(r.FormValue("digest"))
	skip := r.Form["skip"] // '/'-separated relative dirs

	if !mkdirAllWorkdirOr500(w) {
		return
	}
	if !validRelativeDir(dir) {
		http.Error(w, "bogus dir", http.StatusBadRequest)
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

// sshPort returns the port to use for the local SSH server.
func sshPort() string {
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

	// The scaleway Docker images don't have ssh host keys in
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
	fis, _ := ioutil.ReadDir("/proc")
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

func validRelPath(p string) bool {
	if p == "" || strings.Contains(p, `\`) || strings.HasPrefix(p, "/") || strings.Contains(p, "../") {
		return false
	}
	return true
}

type httpStatuser interface {
	error
	httpStatus() int
}

type httpError struct {
	statusCode int
	msg        string
}

func (he httpError) Error() string   { return he.msg }
func (he httpError) httpStatus() int { return he.statusCode }

func badRequest(msg string) error {
	return httpError{http.StatusBadRequest, msg}
}

// requirePassword is an http.Handler auth wrapper that enforces a
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

// plan9LogWriter truncates log writes to 128 bytes,
// to work around some Plan 9 and/or GCE serial port bug.
type plan9LogWriter struct {
	w   io.Writer
	buf []byte
}

func (pw *plan9LogWriter) Write(p []byte) (n int, err error) {
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

// configureMacStadium configures the buildlet flags for use on a Mac
// VM running on MacStadium under VMWare.
func configureMacStadium() {
	*haltEntireOS = true

	// TODO: setup RAM disk for tmp and set *workDir

	disableMacScreensaver()
	enableMacDeveloperMode()

	version, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		log.Fatalf("failed to find sw_vers -productVersion: %v", err)
	}
	majorMinor := regexp.MustCompile(`^(\d+)\.(\d+)`)
	m := majorMinor.FindStringSubmatch(string(version))
	if m == nil {
		log.Fatalf("unsupported sw_vers version %q", version)
	}
	major, minor := m[1], m[2] // "10", "12"
	*reverseType = fmt.Sprintf("host-darwin-%s_%s", major, minor)
	*coordinator = "farmer.golang.org:443"

	// guestName is set by cmd/makemac to something like
	// "mac_10_10_host01b" or "mac_10_12_host01a", which encodes
	// three things: the mac OS version of the guest VM, which
	// physical machine it's on (1 to 10, currently) and which of
	// two possible VMs on that host is running (a or b). For
	// monitoring purposes, we want stable hostnames and don't
	// care which OS version is currently running (which changes
	// constantly), so normalize these to only have the host
	// number and side (a or b), without the OS version. The
	// buildlet will report the OS version to the coordinator
	// anyway. We could in theory do this normalization in the
	// coordinator, but we don't want to put buildlet-specific
	// knowledge there, and this file already contains a bunch of
	// buildlet host-specific configuration, so normalize it here.
	guestName := vmwareGetInfo("guestinfo.name") // "mac_10_12_host01a"
	hostPos := strings.Index(guestName, "_host")
	if hostPos == -1 {
		// Assume cmd/makemac changed its conventions.
		// Maybe all this normalization belongs there anyway,
		// but normalizing here is a safer first step.
		*hostname = guestName
	} else {
		*hostname = "macstadium" + guestName[hostPos:] // "macstadium_host01a"
	}
}

func disableMacScreensaver() {
	err := exec.Command("defaults", "-currentHost", "write", "com.apple.screensaver", "idleTime", "0").Run()
	if err != nil {
		log.Printf("disabling screensaver: %v", err)
	}
}

// enableMacDeveloperMode enables developer mode on macOS for the
// runtime tests. (Issue 31123)
//
// It is best effort; errors are logged but otherwise ignored.
func enableMacDeveloperMode() {
	// Macs are configured with password-less sudo. Without sudo we get prompts
	// that "SampleTools wants to make changes" that block the buildlet from starting.
	// But oddly, not via gomote. Only during startup. The environment must be different
	// enough that in one case macOS asks for permission (because it can use the GUI?)
	// and in the gomote case (where the environment is largley scrubbed) it can't do
	// the GUI dialog somehow and must just try to do it anyway and finds that passwordless
	// sudo works. But using sudo seems to make it always work.
	// For extra paranoia, use a context to not block start-up.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "/usr/bin/sudo", "/usr/sbin/DevToolsSecurity", "-enable").CombinedOutput()
	if err != nil {
		log.Printf("Error enabling developer mode: %v, %s", err, out)
		return
	}
	log.Printf("DevToolsSecurity: %s", out)
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
	exist, err := ioutil.ReadFile(authFile)
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
	if err == nil || !os.IsPermission(err) ||
		runtime.GOOS == "windows" { // different filesystem permission model; also our windows builders are ephemeral single-use VMs anyway
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
	const iptables = "/sbin/iptables"
	const vcsTestGolangOrgIP = "35.184.38.56" // vcs-test.golang.org
	runOrLog(exec.Command(iptables, "-I", "OUTPUT", "1", "-m", "state", "--state", "NEW", "-d", vcsTestGolangOrgIP, "-p", "tcp", "-j", "ACCEPT"))
	runOrLog(exec.Command(iptables, "-I", "OUTPUT", "2", "-m", "state", "--state", "NEW", "-d", "10.0.0.0/8", "-p", "tcp", "-j", "ACCEPT"))
	runOrLog(exec.Command(iptables, "-I", "OUTPUT", "3", "-m", "state", "--state", "NEW", "-p", "tcp", "--dport", "443", "-j", "REJECT", "--reject-with", "icmp-host-prohibited"))
	runOrLog(exec.Command(iptables, "-I", "OUTPUT", "3", "-m", "state", "--state", "NEW", "-p", "tcp", "--dport", "22", "-j", "REJECT", "--reject-with", "icmp-host-prohibited"))
}

func runOrLog(cmd *exec.Cmd) {
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("failed to run %s: %v, %s", cmd.Args, err, out)
	}
}
