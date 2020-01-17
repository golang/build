// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/types"
)

type builderType struct {
	Name      string
	IsReverse bool
	ExpectNum int
}

func builders() (bt []builderType) {
	type builderInfo struct {
		HostType string
	}
	type hostInfo struct {
		IsReverse      bool
		ExpectNum      int
		ContainerImage string
		VMImage        string
	}
	// resj is the response JSON from the builders.
	var resj struct {
		Builders map[string]builderInfo
		Hosts    map[string]hostInfo
	}
	res, err := http.Get("https://farmer.golang.org/builders?mode=json")
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		log.Fatalf("fetching builder types: %s", res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(&resj); err != nil {
		log.Fatalf("decoding builder types: %v", err)
	}
	for b, bi := range resj.Builders {
		if strings.HasPrefix(b, "misc-compile") {
			continue
		}
		hi, ok := resj.Hosts[bi.HostType]
		if !ok {
			continue
		}
		if !hi.IsReverse && hi.ContainerImage == "" && hi.VMImage == "" {
			continue
		}
		bt = append(bt, builderType{
			Name:      b,
			IsReverse: hi.IsReverse,
			ExpectNum: hi.ExpectNum,
		})
	}
	sort.Slice(bt, func(i, j int) bool {
		return bt[i].Name < bt[j].Name
	})
	return
}

func create(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "create usage: gomote create [create-opts] <type>")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nValid types:")
		for _, bt := range builders() {
			var warn string
			if bt.IsReverse {
				if bt.ExpectNum > 0 {
					warn = fmt.Sprintf("   [limited capacity: %d machines]", bt.ExpectNum)
				} else {
					warn = "   [limited capacity]"
				}
			}
			fmt.Fprintf(os.Stderr, "  * %s%s\n", bt.Name, warn)
		}
		os.Exit(1)
	}
	var status bool
	fs.BoolVar(&status, "status", true, "print regular status updates while waiting")

	// TODO(bradfitz): restore this option, and send it to the coordinator:
	// For now, comment it out so it's not misleading.
	// var timeout time.Duration
	// fs.DurationVar(&timeout, "timeout", 60*time.Minute, "how long the VM will live before being deleted.")

	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
	}
	builderType := fs.Arg(0)

	t := time.Now()
	cc, err := buildlet.NewCoordinatorClientFromFlags()
	if err != nil {
		return fmt.Errorf("failed to create coordinator client: %v", err)
	}
	client, err := cc.CreateBuildletWithStatus(builderType, func(st types.BuildletWaitStatus) {
		if status {
			if st.Message != "" {
				fmt.Fprintf(os.Stderr, "# %s\n", st.Message)
				return
			}
			fmt.Fprintf(os.Stderr, "# still creating %s after %v; %d requests ahead of you\n", builderType, time.Since(t).Round(time.Second), st.Ahead)
		}
	})
	if err != nil {
		return fmt.Errorf("failed to create buildlet: %v", err)
	}
	fmt.Println(client.RemoteName())
	return nil
}
