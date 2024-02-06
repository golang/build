// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command genbotcert can both generate a CSR and private key for a LUCI bot
// and generate a certificate from a CSR. It accepts two arguments, the
// bot hostname, and the path to the CSR. If it only receives the hostname then
// it writes the PEM-encoded CSR to the current working directory along with
// a private key. If it receives both the hostname and CSR path then it
// validates that the hostname is what is what is expected in the CSR and
// generates a certificate. The certificate is written to the current working
// directory.
package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	privateca "cloud.google.com/go/security/privateca/apiv1"
	"cloud.google.com/go/security/privateca/apiv1/privatecapb"
	"golang.org/x/build/buildenv"
	"google.golang.org/protobuf/types/known/durationpb"
)

var (
	csrPath     = flag.String("csr-path", "", "Path to the certificate signing request (required for certificate)")
	botHostname = flag.String("bot-hostname", "", "Hostname for the bot (required)")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: genbotcert -bot-hostname <bot-hostname>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *botHostname == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	if *csrPath == "" {
		err = doMain(ctx, *botHostname)
	} else {
		err = generateCert(ctx, *botHostname, *csrPath)
	}
	if err != nil {
		log.Fatalln(err)
	}
}

func doMain(ctx context.Context, cn string) error {
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
	if err := os.WriteFile(cn+".key", privPem, 0600); err != nil {
		return err
	}

	subj := pkix.Name{
		CommonName:   cn + ".bots.golang.org",
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
	if err := os.WriteFile(cn+".csr", csrPem, 0600); err != nil {
		return err
	}

	fmt.Printf("Wrote CSR to %v.csr and key to %v.key\n", cn, cn)
	return nil
}

func generateCert(ctx context.Context, hostname, csrPath string) error {
	csr, err := os.ReadFile(csrPath)
	if err != nil {
		return fmt.Errorf("unable to read file %q: %s", csrPath, err)
	}
	// validate hostname
	pb, _ := pem.Decode(csr)
	cr, err := x509.ParseCertificateRequest(pb.Bytes)
	if err != nil {
		return fmt.Errorf("unable to parse certificate request: %w", err)
	}
	if cr.Subject.CommonName != fmt.Sprintf("%s.bots.golang.org", hostname) {
		return fmt.Errorf("certificate signing request hostname does not match the expected hostname: expected %q, csr hostname: %q", hostname, strings.TrimSuffix(cr.Subject.CommonName, ".bots.golang.org"))
	}
	certID := fmt.Sprintf("%s-%d", hostname, time.Now().Unix()) // A unique name for the certificate.
	caClient, err := privateca.NewCertificateAuthorityClient(ctx)
	if err != nil {
		return fmt.Errorf("NewCertificateAuthorityClient creation failed: %w", err)
	}
	defer caClient.Close()
	fullCaPoolName := fmt.Sprintf("projects/%s/locations/%s/caPools/%s", buildenv.LUCIProduction.ProjectName, "us-central1", "default-pool")
	// Create the CreateCertificateRequest.
	// See https://pkg.go.dev/cloud.google.com/go/security/privateca/apiv1/privatecapb#CreateCertificateRequest.
	req := &privatecapb.CreateCertificateRequest{
		Parent:        fullCaPoolName,
		CertificateId: certID,
		Certificate: &privatecapb.Certificate{
			CertificateConfig: &privatecapb.Certificate_PemCsr{
				PemCsr: string(csr),
			},
			Lifetime: &durationpb.Duration{
				Seconds: 315360000, // Seconds in 10 years.
			},
		},
		IssuingCertificateAuthorityId: "luci-bot-ca", // The name of the certificate authority which issues the certificate.
	}
	resp, err := caClient.CreateCertificate(ctx, req)
	if err != nil {
		return fmt.Errorf("CreateCertificate failed: %w", err)
	}
	log.Printf("Certificate %s created", certID)
	if err := os.WriteFile(certID+".cert", []byte(resp.PemCertificate), 0600); err != nil {
		return fmt.Errorf("unable to write certificate to disk: %s", err)
	}
	fmt.Printf("Wrote certificate to %s.cert\n", certID)
	return nil
}
