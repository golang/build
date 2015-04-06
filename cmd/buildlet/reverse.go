package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"log"
	"net"
	"net/http"
	"strings"
)

func dialCoordinator() {
	caCert := testCoordinatorCA
	addr := *coordinator
	if strings.HasPrefix(addr, "farmer.golang.org") {
		if addr == "farmer.golang.org" {
			addr = "farmer.golang.org:443"
		}
		caCert = coordinatorCA
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM([]byte(caCert)) {
		log.Fatal("failed to append coordinator CA certificate")
	}

	log.Printf("Dialing coordinator %s...", addr)
	tcpConn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Fatalf("Could not dial %s: %v", addr, err)
	}
	config := &tls.Config{
		ServerName: "go",
		RootCAs:    caPool,
	}
	conn := tls.Client(tcpConn, config)
	if err := conn.Handshake(); err != nil {
		log.Fatalf("failed to handshake with coordinator: %v", err)
	}
	bufr := bufio.NewReader(conn)

	// TODO(crawshaw): include build key as part of initial request.
	req, err := http.NewRequest("GET", "/reverse", nil)
	if err != nil {
		log.Fatal(err)
	}
	if err := req.Write(conn); err != nil {
		log.Fatalf("coordinator /reverse request failed: %v", err)
	}
	if _, err := http.ReadResponse(bufr, req); err != nil {
		log.Fatalf("coordinator /reverse response failed: %v", err)
	}

	// The client becomes the simple http server.
	log.Printf("Connected to coordinator, serving HTTP back at them.")
	srv := &http.Server{}
	srv.Serve(newReverseListener(conn))
}

func newReverseListener(c net.Conn) net.Listener {
	rl := &reverseListener{c: c}
	return rl
}

// reverseListener serves out a single underlying conn, once.
// After that it blocks. A one-connection, boring net.Listener.
type reverseListener struct {
	c net.Conn
}

func (rl *reverseListener) Accept() (net.Conn, error) {
	if rl.c != nil {
		c := rl.c
		rl.c = nil
		return c, nil
	}
	// TODO(crawshaw): return error when the connection is closed and
	// make sure the function calling srv.Serve redials the communicator.
	select {}
}

func (rl *reverseListener) Close() error   { return nil }
func (rl *reverseListener) Addr() net.Addr { return reverseAddr("buildlet") }

// reverseAddr implements net.Addr for reverseListener.
type reverseAddr string

func (a reverseAddr) Network() string { return "reverse" }
func (a reverseAddr) String() string  { return "reverse:" + string(a) }

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
