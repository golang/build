// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"golang.org/x/build/internal/gomote/protos"
)

func destroy(args []string) error {
	fs := flag.NewFlagSet("destroy", flag.ContinueOnError)
	fs.Usage = func() {
		log := usageLogger
		log.Print("destroy usage: gomote destroy [instance]")
		fmt.Fprintln(os.Stderr)
		log.Print("Destroys a single instance, or all instances in a group.")
		log.Print("Instance argument is optional with a group.")
		fs.PrintDefaults()
		if fs.NArg() == 0 {
			// List buildlets that you might want to destroy.
			client := gomoteServerClient(context.Background())
			resp, err := client.ListInstances(context.Background(), &protos.ListInstancesRequest{})
			if err != nil {
				log.Fatalf("unable to list possible instances to destroy: %v", err)
			}
			if len(resp.GetInstances()) > 0 {
				fmt.Printf("possible instances:\n")
				for _, inst := range resp.GetInstances() {
					fmt.Printf("\t%s\n", inst.GetGomoteId())
				}
			}
		}
		os.Exit(1)
	}
	var destroyGroup bool
	fs.BoolVar(&destroyGroup, "destroy-group", false, "if a group is used, destroy the group too")

	fs.Parse(args)

	var destroySet []string
	if fs.NArg() == 1 {
		destroySet = append(destroySet, fs.Arg(0))
	} else if activeGroup != nil {
		for _, inst := range activeGroup.Instances {
			destroySet = append(destroySet, inst)
		}
	} else {
		fs.Usage()
	}
	for _, name := range destroySet {
		log.Printf("Destroying %s\n", name)
		ctx := context.Background()
		client := gomoteServerClient(ctx)
		if _, err := client.DestroyInstance(ctx, &protos.DestroyInstanceRequest{
			GomoteId: name,
		}); err != nil {
			return fmt.Errorf("unable to destroy instance: %w", err)
		}
	}
	if activeGroup != nil {
		if destroyGroup {
			if err := deleteGroup(activeGroup.Name); err != nil {
				return err
			}
		} else {
			activeGroup.Instances = nil
			if err := storeGroup(activeGroup); err != nil {
				return err
			}
		}
	}
	return nil
}
