// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/build/internal/gomote/protos"
)

func list(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.Usage = func() {
		usageLogger.Print("list usage: gomote list")
		fs.PrintDefaults()
		os.Exit(1)
	}
	fs.Parse(args)
	if fs.NArg() != 0 {
		fs.Usage()
	}
	groups, err := loadAllGroups()
	if err != nil {
		return fmt.Errorf("loading groups: %w", err)
	}
	ctx := context.Background()
	client := gomoteServerClient(ctx)
	resp, err := client.ListInstances(ctx, &protos.ListInstancesRequest{})
	if err != nil {
		return fmt.Errorf("unable to list instance: %w", err)
	}
	for _, inst := range resp.GetInstances() {
		var groupList strings.Builder
		for _, g := range groups {
			if !g.has(inst.GetGomoteId()) {
				continue
			}
			if groupList.Len() == 0 {
				groupList.WriteString(" (")
			} else {
				groupList.WriteString(", ")
			}
			groupList.WriteString(g.Name)
		}
		if groupList.Len() != 0 {
			groupList.WriteString(")")
		}
		fmt.Printf("%s%s\t%s\t%s\texpires in %v\n", inst.GetGomoteId(), groupList.String(), inst.GetBuilderType(), inst.GetHostType(), time.Unix(inst.GetExpires(), 0).Sub(time.Now()))
	}
	return nil
}
