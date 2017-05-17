// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The maintq command queries a maintnerd gRPC server.
// This tool is mostly for debugging.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log"
	"net/http"
	"strings"

	"go4.org/grpc"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/net/http2"
)

var (
	server = flag.String("server", "maintnerd.golang.org", "maintnerd server")
)

func main() {
	flag.Parse()

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			NextProtos:         []string{"h2"},
			InsecureSkipVerify: strings.HasPrefix(*server, "localhost:"),
		},
	}
	hc := &http.Client{Transport: tr}
	http2.ConfigureTransport(tr)

	cc, err := grpc.NewClient(hc, "https://"+*server)
	if err != nil {
		log.Fatal(err)
	}
	mc := apipb.NewMaintnerServiceClient(cc)
	ctx := context.Background()

	res, err := mc.HasAncestor(ctx, &apipb.HasAncestorRequest{
		Commit:   "f700f89b0be0eda0cda20427fbdae4ff1cb7e6a8",
		Ancestor: "2dc27839df7d51b0544c0ac8b2a0b8f030b7a90c",
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Got: %+v", res)
}
