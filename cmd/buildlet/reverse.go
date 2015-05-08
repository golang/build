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

	caCert := coordinatorCA
	addr := *coordinator
	if addr == "farmer.golang.org" {
		addr = "farmer.golang.org:443"
	}
	if devMode {
		caCert = testCoordinatorCA
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
		ServerName: "go",
		RootCAs:    caPool,
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

/*
Certificate authority and the coordinator SSL key were created with:

openssl genrsa -out ca_key.pem 2048
openssl req -x509 -new -key ca_key.pem -out ca_cert.pem -days 1068 -subj /CN="go"
openssl genrsa -out key.pem 2048
openssl req -new -out cert_req.pem -key key.pem -subj /CN="go"
openssl x509 -req -in cert_req.pem -out cert.pem -CAkey ca_key.pem -CA ca_cert.pem -days 730 -CAcreateserial -CAserial serial
*/

// coordinatorCA is the production CA cert for farmer.golang.org.
const coordinatorCA = `-----BEGIN CERTIFICATE-----
MIIDCzCCAfOgAwIBAgIJANl4KOv9Cj4UMA0GCSqGSIb3DQEBBQUAMA0xCzAJBgNV
BAMTAmdvMB4XDTE1MDQwNTIwMTE0OFoXDTE4MDMwODIwMTE0OFowDTELMAkGA1UE
AxMCZ28wggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDJ/oLb+ksvNScl
zIweMGv2ZWRdWW3o9vWIMpOhkiYuBOZjp7zvs89OuKNdC1ylJs3ENnNtD8QOG1Ze
kM3s6MTjCLVZUX4218HAenGifaunTNfbW1/q/tTnZh4Kri00vgq9jFtYnlqFLYhT
PlmDMdpgOY4ligc/1bSPWVsI7CKCbh3fAz67m++opVE0M7LFp8bhkyFv/dnhZFxo
s9ei3ZKFLjYJdZUNRMZ+HcqBzXMQR7HeCOD2pZ1yoHJw1b3Ebe4YOcQCHq4moW7W
DavISKSXl7DKZYX1QlFUmEMkl5aMIEHUJ0oI2wnL9+u5s1NU2/k8sSxbH7Y/cKio
cFPwuMt7AgMBAAGjbjBsMB0GA1UdDgQWBBS5f/j+8YL9B8THnoAXIhQty3vDZjA9
BgNVHSMENjA0gBS5f/j+8YL9B8THnoAXIhQty3vDZqERpA8wDTELMAkGA1UEAxMC
Z2+CCQDZeCjr/Qo+FDAMBgNVHRMEBTADAQH/MA0GCSqGSIb3DQEBBQUAA4IBAQBU
EOOl2ChJyxFg8b4OrG/EC0HMxic2CakRsj6GWQlAwNU8+3o2u2+zYqKhuREDazsZ
1+0f54iU4TXPgPLiOVLQT8AOM6BDDeZfugAopAf0QaIXW5AmM5hnkhW035aXZgx9
rYageMGnnkK2H7E7WlcFbGcPjZtbpZyFnGoAvxcUfOzdnm/LLuvFg6YWf1ynXsNI
aOx5LNVDhzcQlHZ26ueOLoyIpTQxqvo+hwmIOVDLlZ9bz2BS6FevFjsciJmcDL8N
cmY1/5cC/4NzpnN95cvZxp3FX8Ka7YFun03ubjXzXttoeyrxP2WFXuc2D2hkTJPE
Co9z2+Nue1JHG9JcDaeW
-----END CERTIFICATE-----`

// testCoordinatorCA is a dev mode cert, not used in production.
const testCoordinatorCA = `-----BEGIN CERTIFICATE-----
MIIDCzCCAfOgAwIBAgIJAPvaWgVSI9PaMA0GCSqGSIb3DQEBBQUAMA0xCzAJBgNV
BAMTAmdvMB4XDTE1MDQwNjAzMTc0MVoXDTE4MDMwOTAzMTc0MVowDTELMAkGA1UE
AxMCZ28wggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDJ6t6PGkTk5CnR
+ZVkHq8w9VgDutnTIED3fWQLZLlc7oyexY4wLqmB/fYxINtmtWg7tUon8Y6SMPBF
51bam7qc69iWYuSUVkhHcQSGYM/OUKXmtl5V2W9HqfHT+Kcqi8Vm2E946LPMCtKJ
JUuzSYYLkXFl8JZw0bi8CROZ23LY7FTZTK/lGUun65bDCTB9AuB/BlclBBtT7pDg
6hSc73tMDWRZZ2c4rY0LXYgqbW9Zs0E8ePrKjHGFKxwQlDu0EKhjN/v6HWwq4qXD
Zlcx8tiPdFIpUOPN5SkpJq80XiDLy1Cqxxc0gdbM1uxIxYwNzlJqwybVqx8E9H/E
y4NAdg0xAgMBAAGjbjBsMB0GA1UdDgQWBBSXjKSDNj0jnlgUsb7lQU6K7CvUGjA9
BgNVHSMENjA0gBSXjKSDNj0jnlgUsb7lQU6K7CvUGqERpA8wDTELMAkGA1UEAxMC
Z2+CCQD72loFUiPT2jAMBgNVHRMEBTADAQH/MA0GCSqGSIb3DQEBBQUAA4IBAQCl
YGLMKAAXgqr4Wj3sCOHfzeZR7fD0ngJ45eP08woXyc6Lg+2kcaOjNVIQ7k91XacP
XeoWexeVnaNNxc0B3uWGqy54AF+6ZuJ8Ybtm3KiFrjYd4iuvQUS4wYYh8Iu83chX
TjB7sEliFX8+KNSWONw3vULfggMugyTnRilW8qOWd0Xx729NlsvC+OFJc2RVkGoq
bmE4LZKjOf0SAh32d1Ye4hH1lPjWkGnVtXiBZbtqk9Ctc1bn6Vq2UxsE/BbZHBlc
0iKSFmwBiTqOyCs9q9Hpb012HqZYV+4CMBDsR21yAtecSuY8Rse9Vc+POuyRuY25
oObGb36g+BHVuGJxjbFo
-----END CERTIFICATE-----`
