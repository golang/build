// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/tls"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/revdial/v2"
)

// mode is either a BuildConfig or HostConfig name (map key in x/build/dashboard/builders.go)
func keyForMode(mode string) (string, error) {
	if isDevReverseMode() {
		return devBuilderKey(mode), nil
	}
	if os.Getenv("GO_BUILDER_ENV") == "macstadium_vm" {
		infoKey := "guestinfo.key-" + mode
		key := vmwareGetInfo(infoKey)
		if key == "" {
			return "", fmt.Errorf("no build key found for VMWare info-get key %q", infoKey)
		}
		return key, nil
	}
	keyPath := filepath.Join(homedir(), ".gobuildkey-"+mode)
	if v := os.Getenv("GO_BUILD_KEY_PATH"); v != "" {
		keyPath = v
	}
	key, err := ioutil.ReadFile(keyPath)
	if ok, _ := strconv.ParseBool(os.Getenv("GO_BUILD_KEY_DELETE_AFTER_READ")); ok {
		os.Remove(keyPath)
	}
	if err != nil {
		if len(key) == 0 || err != nil {
			return "", fmt.Errorf("cannot read key file %q: %v", keyPath, err)
		}
	}
	return strings.TrimSpace(string(key)), nil
}

func isDevReverseMode() bool {
	return !strings.HasPrefix(*coordinator, "farmer.golang.org")
}

func dialCoordinator() error {
	devMode := isDevReverseMode()

	if *hostname == "" {
		*hostname = os.Getenv("HOSTNAME")
		if *hostname == "" {
			*hostname, _ = os.Hostname()
		}
		if *hostname == "" {
			*hostname = "buildlet"
		}
	}

	key, err := keyForMode(*reverseType)
	if err != nil {
		log.Fatalf("failed to find key for %s: %v", *reverseType, err)
	}

	addr := *coordinator
	if addr == "farmer.golang.org" {
		addr = "farmer.golang.org:443"
	}

	dial := func(ctx context.Context) (net.Conn, error) {
		log.Printf("Dialing coordinator %s ...", addr)
		t0 := time.Now()
		tcpConn, err := dialCoordinatorTCP(ctx, addr)
		if err != nil {
			log.Printf("buildlet: reverse dial coordinator (%q) error after %v: %v", addr, time.Since(t0).Round(time.Second/100), err)
			return nil, err
		}
		log.Printf("Dialed coordinator %s.", addr)
		serverName := strings.TrimSuffix(addr, ":443")
		log.Printf("Doing TLS handshake with coordinator (verifying hostname %q)...", serverName)
		tcpConn.SetDeadline(time.Now().Add(30 * time.Second))
		config := &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: devMode,
		}
		conn := tls.Client(tcpConn, config)
		if err := conn.Handshake(); err != nil {
			return nil, fmt.Errorf("failed to handshake with coordinator: %v", err)
		}
		tcpConn.SetDeadline(time.Time{})
		return conn, nil
	}
	conn, err := dial(context.Background())
	if err != nil {
		return err
	}

	bufr := bufio.NewReader(conn)
	bufw := bufio.NewWriter(conn)

	log.Printf("Registering reverse mode with coordinator...")
	req, err := http.NewRequest("GET", "/reverse", nil)
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("X-Go-Host-Type", *reverseType)
	req.Header.Set("X-Go-Builder-Key", key)
	req.Header.Set("X-Go-Builder-Hostname", *hostname)
	req.Header.Set("X-Go-Builder-Version", strconv.Itoa(buildletVersion))
	req.Header.Set("X-Revdial-Version", "2")
	if err := req.Write(bufw); err != nil {
		return fmt.Errorf("coordinator /reverse request failed: %v", err)
	}
	if err := bufw.Flush(); err != nil {
		return fmt.Errorf("coordinator /reverse request flush failed: %v", err)
	}
	resp, err := http.ReadResponse(bufr, req)
	if err != nil {
		return fmt.Errorf("coordinator /reverse response failed: %v", err)
	}
	if resp.StatusCode != 101 {
		msg, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("coordinator registration failed; want HTTP status 101; got %v:\n\t%s", resp.Status, msg)
	}

	log.Printf("Connected to coordinator; reverse dialing active")
	srv := &http.Server{}
	ln := revdial.NewListener(conn, dial)
	err = srv.Serve(ln)
	if ln.Closed() { // TODO: this actually wants to know whether an error-free Close was called
		return nil
	}
	return fmt.Errorf("http.Serve on reverse connection complete: %v", err)
}

var coordDialer = &net.Dialer{
	Timeout:   10 * time.Second,
	KeepAlive: 15 * time.Second,
}

// dialCoordinatorTCP returns a TCP connection to the coordinator, making
// a CONNECT request to a proxy as a fallback.
func dialCoordinatorTCP(ctx context.Context, addr string) (net.Conn, error) {
	tcpConn, err := coordDialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		// If we had problems connecting to the TCP addr
		// directly, perhaps there's a proxy in the way. See
		// if they have an HTTPS_PROXY environment variable
		// defined and try to do a CONNECT request to it.
		req, _ := http.NewRequest("GET", "https://"+addr, nil)
		proxyURL, _ := http.ProxyFromEnvironment(req)
		if proxyURL != nil {
			return dialCoordinatorViaCONNECT(ctx, addr, proxyURL)
		}
		return nil, err
	}
	return tcpConn, nil
}

func dialCoordinatorViaCONNECT(ctx context.Context, addr string, proxy *url.URL) (net.Conn, error) {
	proxyAddr := proxy.Host
	if proxy.Port() == "" {
		proxyAddr = net.JoinHostPort(proxyAddr, "80")
	}
	log.Printf("dialing proxy %q ...", proxyAddr)
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("dialing proxy %q failed: %v", proxyAddr, err)
	}
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", addr, proxy.Hostname())
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, fmt.Errorf("reading HTTP response from CONNECT to %s via proxy %s failed: %v",
			addr, proxyAddr, err)
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("proxy error from %s while dialing %s: %v", proxyAddr, addr, res.Status)
	}

	// It's safe to discard the bufio.Reader here and return the
	// original TCP conn directly because we only use this for
	// TLS, and in TLS the client speaks first, so we know there's
	// no unbuffered data. But we can double-check.
	if br.Buffered() > 0 {
		return nil, fmt.Errorf("unexpected %d bytes of buffered data from CONNECT proxy %q",
			br.Buffered(), proxyAddr)
	}
	return c, nil
}

func devBuilderKey(builder string) string {
	h := hmac.New(md5.New, []byte("gophers rule"))
	io.WriteString(h, builder)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func homedir() string {
	switch runtime.GOOS {
	case "windows":
		return os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
	case "plan9":
		return os.Getenv("home")
	}
	home := os.Getenv("HOME")
	if home != "" {
		return home
	}
	if os.Getuid() == 0 {
		return "/root"
	}
	return "/"
}

// TestDialCoordinator dials the coordinator. Exported for testing.
func TestDialCoordinator() {
	// TODO(crawshaw): move some of this logic out of main to simplify testing hook.
	http.Handle("/status", http.HandlerFunc(handleStatus))
	dialCoordinator()
}
