// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package https contains helpers for starting an HTTP/HTTPS server.
package https // import "golang.org/x/build/internal/https"

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/build/autocertcache"
	"golang.org/x/build/internal/secret"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

type Options struct {
	// Specifies the ACME directory to use to retrieve certificates.
	AutocertDirectory string
	// Specifies the ACME external account binding token to use. This should be
	// a JSON-encoded acme.ExternalAccountBinding struct.
	AutocertEAB string
	// Specifies the GCS bucket to use with AutocertAddr.
	AutocertBucket string
	// If non-empty, listen on this address and serve HTTPS using a cert stored in AutocertBucket.
	AutocertAddr string
	// Specifies the email address to use when creating an ACME account.
	AutocertEmail string
	// If non-empty, listen on this address and serve HTTPS using a self-signed cert.
	SelfSignedAddr string
	// If non-empty, listen on this address and serve HTTP.
	HTTPAddr string
	// If non-empty, respond unconditionally with 200 OK to requests on this path.
	HealthPath string
}

var DefaultOptions = &Options{}

// RegisterFlags registers flags that control DefaultOptions, which will be
// used with ListenAndServe below.
// Typical usage is to call RegisterFlags at the beginning of main, then
// ListenAndServe at the end.
func RegisterFlags(set *flag.FlagSet) {
	// This is slightly hacky, but necessary since some callers may have already
	// called secret.InitFlagSupport, and others may not have, and there is not
	// a perfect way to know (and calling it twice would stomp on already defined
	// secret flags).
	if secret.DefaultResolver.Context == nil {
		if err := secret.InitFlagSupport(context.Background()); err != nil {
			log.Fatalln(err)
		}
	}
	secret.DefaultResolver.FlagVar(set, &DefaultOptions.AutocertEAB, "autocert-eab", "specifies the ACME external account binding to use")

	set.StringVar(&DefaultOptions.AutocertDirectory, "autocert-directory", "", "specifies the ACME directory to use")
	set.StringVar(&DefaultOptions.AutocertBucket, "autocert-bucket", "", "specifies the GCS bucket to use with autocert-addr")
	set.StringVar(&DefaultOptions.AutocertAddr, "listen-https-autocert", "", "if non-empty, listen on this address and serve HTTPS using a cert stored in autocert-bucket")
	set.StringVar(&DefaultOptions.SelfSignedAddr, "listen-https-selfsigned", "", "if non-empty, listen on this address and serve HTTPS using a self-signed cert")
	set.StringVar(&DefaultOptions.HTTPAddr, "listen-http", "", "if non-empty, listen on this address and serve HTTP")
	set.StringVar(&DefaultOptions.HealthPath, "health-path", "/healthz", "if non-empty, respond unconditionally with 200 OK to requests on this path")
	set.StringVar(&DefaultOptions.AutocertEmail, "autocert-email", "", "specifies the contact email to use when creating an ACME account")
}

// ListenAndServe runs the servers configured by DefaultOptions. It always
// returns a non-nil error.
func ListenAndServe(ctx context.Context, handler http.Handler) error {
	return ListenAndServeOpts(ctx, handler, DefaultOptions)
}

// ListenAndServeOpts runs the servers configured by opts. It always
// returns a non-nil error.
func ListenAndServeOpts(ctx context.Context, handler http.Handler, opts *Options) error {
	errc := make(chan error, 3)

	if opts.HealthPath != "" {
		wrapped := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == opts.HealthPath {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			} else {
				wrapped.ServeHTTP(w, r)
			}
		})
	}

	if opts.HTTPAddr != "" {
		server := &http.Server{Addr: opts.HTTPAddr, Handler: handler}
		defer server.Close()
		go func() { errc <- server.ListenAndServe() }()
	}

	if opts.AutocertAddr != "" {
		if opts.AutocertBucket == "" {
			return fmt.Errorf("must specify autocert-bucket with listen-https-autocert")
		}
		server, err := autocertServer(ctx, opts, handler)
		if err != nil {
			return err
		}
		defer server.Close()
		go func() { errc <- server.ListenAndServeTLS("", "") }()
	}

	if opts.SelfSignedAddr != "" {
		server, err := selfSignedServer(opts.SelfSignedAddr, handler)
		if err != nil {
			return err
		}
		defer server.Close()
		go func() { errc <- server.ListenAndServeTLS("", "") }()
	}

	return <-errc
}

// autocertServer returns an http.Server that is configured to serve HTTPS on
// addr using a certificate cached in the GCS bucket specified by bucket.
func autocertServer(ctx context.Context, opts *Options, handler http.Handler) (*http.Server, error) {
	sc, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage.NewClient: %v", err)
	}
	const hostSuffix = ".golang.org"
	m := autocert.Manager{
		Prompt: autocert.AcceptTOS,
		HostPolicy: func(ctx context.Context, host string) error {
			if !strings.HasSuffix(host, hostSuffix) {
				return fmt.Errorf("refusing to serve autocert on provided domain (%q), must have the suffix %q",
					host, hostSuffix)
			}
			return nil
		},
		Cache: autocertcache.NewGoogleCloudStorageCache(sc, opts.AutocertBucket),
		Email: opts.AutocertEmail,
	}
	if opts.AutocertDirectory != "" {
		m.Client = new(acme.Client)
		m.Client.DirectoryURL = opts.AutocertDirectory
	}
	if opts.AutocertEAB != "" {
		var eab acme.ExternalAccountBinding
		if err := json.Unmarshal([]byte(opts.AutocertEAB), &eab); err != nil {
			return nil, fmt.Errorf("failed to unmarshal -autocert-eab: %s", err)
		}
		m.ExternalAccountBinding = &eab
	}
	server := &http.Server{
		Addr:      opts.AutocertAddr,
		Handler:   handler,
		TLSConfig: m.TLSConfig(),
	}
	return server, nil
}

// selfSignedServer returns an http.Server that is configured to serve
// self-signed HTTPS on addr.
func selfSignedServer(addr string, handler http.Handler) (*http.Server, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to generate serial number: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Go build system"},
		},
		NotBefore:   time.Now().Add(-time.Minute),
		NotAfter:    time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:        true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	s := &http.Server{
		Addr:    addr,
		Handler: handler,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{{
				Certificate: [][]byte{derBytes},
				PrivateKey:  priv,
			}},
		},
	}
	return s, nil
}
