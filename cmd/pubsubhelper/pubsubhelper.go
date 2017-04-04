// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// The pubsubhelper is an SMTP server for Gerrit updates and an HTTP
// server for Github webhook updates. It then lets other clients subscribe
// to those changes.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bradfitz/go-smtpd/smtpd"
	"github.com/jellevandenhooff/dkim"
	"golang.org/x/build/cmd/pubsubhelper/pubsubtypes"
	"golang.org/x/crypto/acme/autocert"
)

var (
	botEmail   = flag.String("rcpt", "\x67\x6f\x70\x68\x65\x72\x62\x6f\x74@pubsubhelper.golang.org", "email address of bot. incoming emails must be to this address.")
	httpListen = flag.String("http", ":80", "HTTP listen address")
	acmeDomain = flag.String("autocert", "pubsubhelper.golang.org", "If non-empty, listen on port 443 and serve HTTPS with a LetsEncrypt cert for this domain.")
	smtpListen = flag.String("smtp", ":25", "SMTP listen address")
)

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	io.WriteString(w, `<html>
<body>
  This is <a href="https://godoc.org/golang.org/x/build/cmd/pubsubhelper">pubsubhelper</a>.
</body>
</html>
`)
}

func handleWaitEvent(w http.ResponseWriter, r *http.Request) {

}

func main() {
	flag.Parse()

	http.HandleFunc("/", handleRoot)
	http.HandleFunc("/waitevent", handleWaitEvent)

	errc := make(chan error)
	go func() {
		log.Printf("running pubsubhelper on %s", *smtpListen)
		s := &smtpd.Server{
			Addr:        *smtpListen,
			OnNewMail:   onNewMail,
			ReadTimeout: time.Minute,
		}
		err := s.ListenAndServe()
		errc <- fmt.Errorf("SMTP ListenAndServe: %v", err)
	}()
	go func() {
		if *acmeDomain == "" {
			return
		}
		m := autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(*acmeDomain),
		}
		if _, err := os.Stat("/autocert-cache"); err == nil {
			m.Cache = autocert.DirCache("/autocert-cache")
		} else {
			log.Printf("Warning: running acme/autocert without cache")
		}
		log.Printf("running pubsubhelper HTTPS on :443 for %s", *acmeDomain)
		s := &http.Server{
			Addr:              ":https",
			TLSConfig:         &tls.Config{GetCertificate: m.GetCertificate},
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      5 * time.Minute,
			IdleTimeout:       5 * time.Minute,
		}
		err := s.ListenAndServeTLS("", "")
		errc <- fmt.Errorf("HTTPS ListenAndServeTLS: %v", err)
	}()
	go func() {
		log.Printf("running pubsubhelper HTTP on %s", *httpListen)
		s := &http.Server{
			Addr:              *httpListen,
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      5 * time.Minute,
			IdleTimeout:       5 * time.Minute,
		}
		err := s.ListenAndServe()
		errc <- fmt.Errorf("HTTP ListenAndServe: %v", err)
	}()

	log.Fatal(<-errc)
}

type env struct {
	from    smtpd.MailAddress
	body    bytes.Buffer
	conn    smtpd.Connection
	tooBig  bool
	hasRcpt bool
}

func (e *env) BeginData() error {
	if !e.hasRcpt {
		return smtpd.SMTPError("554 5.5.1 Error: no valid recipients")
	}
	return nil
}

func (e *env) AddRecipient(rcpt smtpd.MailAddress) error {
	if e.hasRcpt {
		return smtpd.SMTPError("554 5.5.1 Error: dup recipients")
	}
	to := rcpt.Email()
	if to != *botEmail {
		return errors.New("bogus recipient")
	}
	e.hasRcpt = true
	return nil
}

func (e *env) Write(line []byte) error {
	const maxSize = 5 << 20
	if e.body.Len() > maxSize {
		e.tooBig = true
		return nil
	}
	e.body.Write(line)
	return nil
}

var (
	headerSep           = []byte("\r\n\r\n")
	dkimSignatureHeader = []byte("\nDKIM-Signature:")
)

func (e *env) Close() error {
	if e.tooBig {
		log.Printf("ignoring too-large email from %q", e.from)
		return nil
	}
	from := e.from.Email()
	bodyBytes := e.body.Bytes()
	if !bytes.Contains(e.body.Bytes(), dkimSignatureHeader) {
		log.Printf("ignoring unsigned (~spam) email from %q", from)
		return nil
	}
	if e.from.Hostname() != "gerritcodereview.bounces.google.com" {
		log.Printf("ignoring signed, non-Gerrit email from %q", from)
		return nil
	}

	headerBytes := bodyBytes
	if i := bytes.Index(headerBytes, headerSep); i == -1 {
		log.Printf("Ignoring email without header separator from %q", from)
		return nil
	} else {
		headerBytes = headerBytes[:i+len(headerSep)]
	}

	ve, err := dkim.ParseAndVerify(string(headerBytes), dkim.HeadersOnly, dnsClient{})
	if err != nil {
		log.Printf("Email from %q didn't pass DKIM verifications: %v", from, err)
		return nil
	}
	if ve.Signature.Domain != "google.com" {
		log.Printf("ignoring DKIM-verified gerrit email from non-Google domain %q", ve.Signature.Domain)
		return nil
	}
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(headerBytes)))
	hdr, err := tp.ReadMIMEHeader()
	if err != nil {
		log.Printf("Ignoring ReadMIMEHeader error: %v    from email:\n%s", headerBytes)
		return nil
	}
	changeNum, _ := strconv.Atoi(hdr.Get("X-Gerrit-Change-Number"))
	publish(&pubsubtypes.Event{
		Gerrit: &pubsubtypes.GerritEvent{
			URL:          strings.Trim(hdr.Get("X-Gerrit-ChangeURL"), "<>"),
			CommitHash:   hdr.Get("X-Gerrit-Commit"),
			ChangeNumber: changeNum,
		},
	})
	return nil
}

func publish(e *pubsubtypes.Event) {
	je, err := json.MarshalIndent(e, "", "\t")
	if err != nil {
		log.Printf("JSON error: %v", err)
		return
	}
	log.Printf("Event: %s", je)

}

type dnsClient struct{}

var resolver = &net.Resolver{PreferGo: true}

func (dnsClient) LookupTxt(hostname string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return resolver.LookupTXT(ctx, hostname)
}

func onNewMail(c smtpd.Connection, from smtpd.MailAddress) (smtpd.Envelope, error) {
	return &env{
		from: from,
		conn: c,
	}, nil
}
