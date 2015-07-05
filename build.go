// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package build contains constants for the Go continous build system.
package build

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
)

// CoordinatorInstance is either "prod", "staging", or "localhost:<port>".
type CoordinatorInstance string

const (
	ProdCoordinator    CoordinatorInstance = "prod"
	StagingCoordinator CoordinatorInstance = "staging"
)

func (ci CoordinatorInstance) TLSHostPort() (string, error) {
	switch ci {
	case ProdCoordinator:
		return "farmer.golang.org:443", nil
	case StagingCoordinator:
		return "104.154.113.235:443", nil
	}
	if ci == "" {
		return "", errors.New("build: coordinator instance is empty")
	}
	if _, _, err := net.SplitHostPort(string(ci)); err == nil {
		return string(ci), nil
	}
	return net.JoinHostPort(string(ci), "443"), nil
}

func (ci CoordinatorInstance) TLSDialer() func(network, addr string) (net.Conn, error) {
	caPool := x509.NewCertPool()
	tlsConf := &tls.Config{
		ServerName: "go", // fixed name; see build.go
		RootCAs:    caPool,
	}
	var err error
	ca := ci.CACert()
	if ci == "" {
		tlsConf.InsecureSkipVerify = true // in localhost dev mode
	} else {
		if !caPool.AppendCertsFromPEM([]byte(ca)) {
			err = fmt.Errorf("Failed to load client's TLS cert for instance %q", string(ci))
		}
	}
	return func(network, addr string) (net.Conn, error) {
		if err != nil {
			// sticky error from AppendCertsFromPEM
			return nil, err
		}
		if network != "tcp" {
			return nil, fmt.Errorf("unsupported network %q", network)
		}
		tcpConn, err := net.Dial("tcp", addr)
		if err != nil {
			return nil, err
		}
		conn := tls.Client(tcpConn, tlsConf)
		if err := conn.Handshake(); err != nil {
			return nil, fmt.Errorf("failed to handshake with coordinator: %v", err)
		}
		return conn, nil
	}
}

// CACert returns the public certificate of the CA used to sign
// this coordinator instance's certificate.
func (ci CoordinatorInstance) CACert() string {
	if ci == ProdCoordinator {
		return ProdCoordinatorCA
	} else if ci == StagingCoordinator {
		return StagingCoordinatorCA
	}
	return ""
}

/*
Certificate authority and the coordinator SSL key were created with:

openssl genrsa -out ca_key.pem 2048
openssl req -x509 -new -key ca_key.pem -out ca_cert.pem -days 1068 -subj /CN="go"
openssl genrsa -out key.pem 2048
openssl req -new -out cert_req.pem -key key.pem -subj /CN="go"
openssl x509 -req -in cert_req.pem -out cert.pem -CAkey ca_key.pem -CA ca_cert.pem -days 730 -CAcreateserial -CAserial serial
*/

// ProdCoordinatorCA is the production CA cert for farmer.golang.org.
const ProdCoordinatorCA = `-----BEGIN CERTIFICATE-----
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

// StagingCoordinatorCA is the cert used on GCE for the
// go-dashboard-dev project.
const StagingCoordinatorCA = `-----BEGIN CERTIFICATE-----
MIIC7TCCAdWgAwIBAgIJAOfawne6V7F1MA0GCSqGSIb3DQEBCwUAMA0xCzAJBgNV
BAMMAmdvMB4XDTE1MDcwNjE5MTAyMloXDTE4MDYwODE5MTAyMlowDTELMAkGA1UE
AwwCZ28wggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDBnRAfwDXJzRDf
RBolwbQHi/iQ8h70FuQCYKNpjTQWjmWX+8zT7f0C+6q3hEqaEt6gL8Ch9sTiDxOj
MeaczdXVUGGvtKMB/e4CLrpswfTZNR9Fx0BbtdcdyyNAgobphcR81CgzQgokr7FS
M6E1HsjxqBUwCQGZWnkjVxPSd2VnS7Lnz1+DCSPqAboIXyIwQXnu+OjecnrB6/Fp
WOUI0Z5PgEh8vBKhPNptCeX5o8Cl1NVdmvMw2nGIxo6M0swbzDrELfJ1LD9UtGiE
4a2dTttqGYGF0KtBUM3VsX93zPjHix6h9YEzU9zffCOZWIizAXOGMPe/jwPAdAeM
FCxJJzkfAgMBAAGjUDBOMB0GA1UdDgQWBBQGMc6uZVoT12xX2BJUESJXz1KgXzAf
BgNVHSMEGDAWgBQGMc6uZVoT12xX2BJUESJXz1KgXzAMBgNVHRMEBTADAQH/MA0G
CSqGSIb3DQEBCwUAA4IBAQCmx74P6MVgl+atDFiMxhLiDp7CiLMZXrnmgBVz9VQ6
NwDbN/kHXDCeJr1D175T7mQVEkTS4dDDP6LqCNdyP1o+xzJQd7J87jSMlWyDUtG6
Wa2n03q1mzEb6fveFs3c08mXPMZ20LE2ApMbFJUhKStuBaQFN601S/ixS37kiefZ
c2G8sF0KryoHCIlNaCSG+OdztoBg7HJ3XLPN6uO10jf9Dk+iY1QdbYN98WWljL/A
QJOrbUZeZsUJ0KnxVMNN0CgB6T0DE9qzewoiNknieXtq2vl/Nxa1AD+qAzWck/bb
yHd17CDY55cj4fworr/PayJuB7JJOrLk68yx2eUlK0Np
-----END CERTIFICATE-----`
