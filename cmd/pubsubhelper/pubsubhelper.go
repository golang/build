// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// The pubsubhelper is an SMTP server for Gerrit updates and an HTTP
// server for Github webhook updates. It then lets other clients subscribe
// to those changes.
package main

import (
	"bytes"
	"flag"
	"log"

	"github.com/bradfitz/go-smtpd/smtpd"
)

type env struct {
	*smtpd.BasicEnvelope
	from smtpd.MailAddress
	body bytes.Buffer
}

func (e *env) AddRecipient(rcpt smtpd.MailAddress) error {
	return e.BasicEnvelope.AddRecipient(rcpt)
}

func (e *env) Write(line []byte) error {
	const maxSize = 5 << 20
	if e.body.Len() > maxSize {
		return nil
	}
	e.body.Write(line)
	return nil
}

func (e *env) Close() error {
	log.Printf("Got email: %s\n", e.body.Bytes())
	return nil
}

func onNewMail(c smtpd.Connection, from smtpd.MailAddress) (smtpd.Envelope, error) {
	log.Printf("new MAIL FROM %q", from)
	return &env{
		from:          from,
		BasicEnvelope: new(smtpd.BasicEnvelope),
	}, nil
}

func main() {
	flag.Parse()

	log.Printf("running pubsubhelper on port 25")
	s := &smtpd.Server{
		Addr:      ":25",
		OnNewMail: onNewMail,
	}
	err := s.ListenAndServe()
	if err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}
