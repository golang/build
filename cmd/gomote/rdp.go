// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"golang.org/x/build/buildlet"
	"golang.org/x/sync/errgroup"
)

const rdpPort = 3389

func rdp(args []string) error {
	fs := flag.NewFlagSet("rdp", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "rdp usage: gomote rdp [--listen=...] <instance>")
		fs.PrintDefaults()
		os.Exit(1)
	}
	var listen string
	fs.StringVar(&listen, "listen", "localhost:"+fmt.Sprint(rdpPort), "local address to listen on")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
	}
	name := fs.Arg(0)
	bc, err := remoteClient(name)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	log.Printf("Listening on %v to proxy RDP.", ln.Addr())
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go handleRDPConn(bc, c)
	}
}

func handleRDPConn(bc *buildlet.Client, c net.Conn) {
	const Lmsgprefix = 64 // new in Go 1.14, harmless before
	log := log.New(os.Stderr, c.RemoteAddr().String()+": ", log.LstdFlags|Lmsgprefix)
	log.Printf("accepted connection, dialing buildlet via coordinator proxy...")
	rwc, err := bc.ProxyTCP(rdpPort)
	if err != nil {
		c.Close()
		log.Printf("failed to connect to buildlet via coordinator: %v", err)
		return
	}

	log.Printf("connected to buildlet; proxying data.")

	grp, ctx := errgroup.WithContext(context.Background())
	grp.Go(func() error {
		_, err := io.Copy(rwc, c)
		if err == nil {
			return errors.New("local client closed")
		}
		return fmt.Errorf("error copying from local to remote: %v", err)
	})
	grp.Go(func() error {
		_, err := io.Copy(c, rwc)
		if err == nil {
			return errors.New("remote server closed")
		}
		return fmt.Errorf("error copying from remote to local: %v", err)
	})
	<-ctx.Done()
	rwc.Close()
	c.Close()
	log.Printf("closing RDP connection: %v", grp.Wait())
}
