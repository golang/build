// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The maintwatch commands tails the maintner mutation log.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"google.golang.org/protobuf/encoding/prototext"
)

var server = flag.String("server", godata.Server, "maintner server's /logs URL")

func main() {
	flag.Parse()

	for {
		err := maintner.TailNetworkMutationSource(context.Background(), *server, func(e maintner.MutationStreamEvent) error {
			if e.Err != nil {
				log.Printf("# ignoring err: %v\n", e.Err)
				time.Sleep(5 * time.Second)
				return nil
			}
			fmt.Println()
			fmt.Print(prototext.Format(e.Mutation))
			return nil
		})
		log.Printf("tail error: %v; restarting\n", err)
		time.Sleep(time.Second)
	}
}
