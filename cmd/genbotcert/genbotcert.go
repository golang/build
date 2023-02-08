// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command genbotcert generates a private key and CSR for a LUCI bot.
// It accepts one argument, the bot hostname, and writes the PEM-encoded
// results to the current working directory.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"os"
)

func main() {
	if err := doMain(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "fatal error: %v", err)
		os.Exit(1)
	}
}

func doMain(cn string) error {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return err
	}

	privPem := pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		},
	)

	if err := ioutil.WriteFile(cn+".key", privPem, 0600); err != nil {
		return err
	}

	subj := pkix.Name{
		CommonName:   os.Args[1] + ".bots.golang.org",
		Organization: []string{"Google"},
	}

	template := x509.CertificateRequest{
		Subject:            subj,
		DNSNames:           []string{subj.CommonName},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}

	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, &template, key)
	if err != nil {
		return err
	}
	csrPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrBytes})
	if err := ioutil.WriteFile(cn+".csr", csrPem, 0644); err != nil {
		return err
	}

	fmt.Printf("Wrote CSR to %v.csr and key to %v.key\n", cn, cn)
	return nil
}
