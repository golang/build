// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/md5"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/build"
)

func repeatDialCoordinator() {
	for {
		if err := dialCoordinator(); err != nil {
			log.Print(err)
		}
		log.Printf("Waiting 30 seconds and dialing again.")
		time.Sleep(30 * time.Second)
	}
}

func dialCoordinator() error {
	devMode := !strings.HasPrefix(*coordinator, "farmer.golang.org")

	modes := strings.Split(*reverse, ",")
	var keys []string
	for _, m := range modes {
		if devMode {
			keys = append(keys, string(devBuilderKey(m)))
			continue
		}
		keyPath := filepath.Join(homedir(), ".gobuildkey-"+m)
		key, err := ioutil.ReadFile(keyPath)
		if os.IsNotExist(err) && len(modes) == 1 {
			globalKeyPath := filepath.Join(homedir(), ".gobuildkey")
			key, err = ioutil.ReadFile(globalKeyPath)
			if err != nil {
				log.Fatalf("cannot read either key file %q or %q: %v", keyPath, globalKeyPath, err)
			}
		} else if err != nil {
			log.Fatalf("cannot read key file %q: %v", keyPath, err)
		}
		keys = append(keys, string(key))
	}

	caCert := build.ProdCoordinatorCA
	addr := *coordinator
	if addr == "farmer.golang.org" {
		addr = "farmer.golang.org:443"
	}
	if devMode {
		caCert = build.DevCoordinatorCA
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM([]byte(caCert)) {
		log.Fatal("failed to append coordinator CA certificate")
	}

	log.Printf("Dialing coordinator %s...", addr)
	tcpConn, err := net.Dial("tcp", addr)
	if err != nil {
		return err // try again
	}
	config := &tls.Config{
		ServerName:         "go",
		RootCAs:            caPool,
		InsecureSkipVerify: devMode,
	}
	conn := tls.Client(tcpConn, config)
	if err := conn.Handshake(); err != nil {
		return fmt.Errorf("failed to handshake with coordinator: %v", err)
	}
	bufr := bufio.NewReader(conn)

	req, err := http.NewRequest("GET", "/reverse", nil)
	if err != nil {
		log.Fatal(err)
	}
	req.Header["X-Go-Builder-Type"] = modes
	req.Header["X-Go-Builder-Key"] = keys
	if err := req.Write(conn); err != nil {
		return fmt.Errorf("coordinator /reverse request failed: %v", err)
	}
	resp, err := http.ReadResponse(bufr, req)
	if err != nil {
		return fmt.Errorf("coordinator /reverse response failed: %v", err)
	}
	if resp.StatusCode != 200 {
		msg, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("coordinator registration failed:\n\t%s", msg)
	}
	resp.Body.Close()

	// The client becomes the simple http server.
	log.Printf("Connected to coordinator, serving HTTP back at them.")
	stateCh := make(chan http.ConnState, 1)
	srv := &http.Server{
		ConnState: func(_ net.Conn, state http.ConnState) { stateCh <- state },
	}
	return srv.Serve(&reverseListener{
		conn:    conn,
		stateCh: stateCh,
	})
}

// reverseListener serves out a single underlying conn, once.
//
// It is designed to be passed to a *http.Server, which loops
// continually calling Accept. As this reverse connection only
// ever has one connection to hand out, it responds to the first
// Accept, and then blocks on the second Accept.
//
// While blocking on the second Accept, this listener takes on the
// job of checking the health of the original net.Conn it handed out.
// If it goes unused for a while, it closes the original net.Conn
// and returns an error, ending the life of the *http.Server
type reverseListener struct {
	done    bool
	conn    net.Conn
	stateCh <-chan http.ConnState
}

func (rl *reverseListener) Accept() (net.Conn, error) {
	if !rl.done {
		// First call to Accept, return our one net.Conn.
		rl.done = true
		return rl.conn, nil
	}
	// Second call to Accept, block until we decide the entire
	// server should be torn down.
	defer rl.conn.Close()
	const timeout = 1 * time.Minute
	timer := time.NewTimer(timeout)
	var state http.ConnState
	for {
		select {
		case state = <-rl.stateCh:
			if state == http.StateClosed {
				return nil, errors.New("coordinator connection closed")
			}
		// The coordinator sends a health check every 30 seconds
		// when buildlets are idle. If we go a minute without
		// seeing anything, assume the coordinator is in a bad way
		// (probably restarted) and close the connection.
		case <-timer.C:
			if state == http.StateIdle {
				return nil, errors.New("coordinator connection unhealthy")
			}
		}
		timer.Reset(timeout)
	}
}

func (rl *reverseListener) Close() error   { return nil }
func (rl *reverseListener) Addr() net.Addr { return reverseAddr("buildlet") }

// reverseAddr implements net.Addr for reverseListener.
type reverseAddr string

func (a reverseAddr) Network() string { return "reverse" }
func (a reverseAddr) String() string  { return "reverse:" + string(a) }

func devBuilderKey(builder string) string {
	h := hmac.New(md5.New, []byte("gophers rule"))
	io.WriteString(h, builder)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func homedir() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
	}
	return os.Getenv("HOME")
}

// TestDialCoordinator dials the coordinator. Exported for testing.
func TestDialCoordinator() {
	// TODO(crawshaw): move some of this logic out of main to simplify testing hook.
	http.Handle("/status", http.HandlerFunc(handleStatus))
	dialCoordinator()
}
