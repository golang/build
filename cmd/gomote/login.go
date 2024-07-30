// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"golang.org/x/build/internal/iapclient"
)

// login triggers the authentication workflow for the gomote service and
// LUCI.
func login(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "login usage: gomote login")
		fmt.Fprintln(os.Stderr)
		fs.PrintDefaults()
		os.Exit(1)
	}
	fs.Parse(args)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	log.Print("Authenticating with the gomote service.")
	if _, err := iapclient.TokenSourceForceLogin(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "unable to authenticate into gomoteserver: %s\n", err)
	}
	auth := createLUCIAuthenticator(ctx)
	log.Print("Authenticating with the LUCI service.")
	if err := auth.Login(); err != nil {
		fmt.Fprintf(os.Stderr, "unable to authenticate into LUCI: %s\n", err)
	}
	return nil
}
