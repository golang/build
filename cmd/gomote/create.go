// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/sync/errgroup"
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
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "If there's a valid group specified, new instances are")
		fmt.Fprintln(os.Stderr, "automatically added to the group. If the group in")
		fmt.Fprintln(os.Stderr, "$GOMOTE_GROUP doesn't exist, and there's no other group")
		fmt.Fprintln(os.Stderr, "specified, it will be created and new instances will be")
		fmt.Fprintln(os.Stderr, "added to that group.")
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
	var count int
	fs.IntVar(&count, "count", 1, "number of instances to create")
	var setup bool
	fs.BoolVar(&setup, "setup", false, "set up the instance by pushing GOROOT and building the Go toolchain")
	var newGroup string
	fs.StringVar(&newGroup, "new-group", "", "also create a new group and add the new instances to it")

	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
	}
	builderType := fs.Arg(0)

	var groupMu sync.Mutex
	group := activeGroup
	var err error
	if newGroup != "" {
		group, err = doCreateGroup(newGroup)
		if err != nil {
			return err
		}
	}
	if group == nil && os.Getenv("GOMOTE_GROUP") != "" {
		group, err = doCreateGroup(os.Getenv("GOMOTE_GROUP"))
		if err != nil {
			return err
		}
	}

	var tmpOutDir string
	var tmpOutDirOnce sync.Once
	eg, ctx := errgroup.WithContext(context.Background())
	client := gomoteServerClient(ctx)
	for i := 0; i < count; i++ {
		i := i
		eg.Go(func() error {
			start := time.Now()
			stream, err := client.CreateInstance(ctx, &protos.CreateInstanceRequest{BuilderType: builderType})
			if err != nil {
				return fmt.Errorf("failed to create buildlet: %w", err)
			}
			var inst string
		updateLoop:
			for {
				update, err := stream.Recv()
				switch {
				case err == io.EOF:
					break updateLoop
				case err != nil:
					return fmt.Errorf("failed to create buildlet (%d): %w", i+1, err)
				case update.GetStatus() != protos.CreateInstanceResponse_COMPLETE && status:
					fmt.Fprintf(os.Stderr, "# still creating %s (%d) after %v; %d requests ahead of you\n", builderType, i+1, time.Since(start).Round(time.Second), update.GetWaitersAhead())
				case update.GetStatus() == protos.CreateInstanceResponse_COMPLETE:
					inst = update.GetInstance().GetGomoteId()
				}
			}
			fmt.Println(inst)
			if group != nil {
				groupMu.Lock()
				group.Instances = append(group.Instances, inst)
				groupMu.Unlock()
			}
			if !setup {
				return nil
			}

			// -setup is set, so push GOROOT and run make.bash.

			tmpOutDirOnce.Do(func() {
				tmpOutDir, err = os.MkdirTemp("", "gomote")
			})
			if err != nil {
				return fmt.Errorf("failed to create a temporary directory for setup output: %w", err)
			}

			// Push GOROOT.
			detailedProgress := count == 1
			goroot, err := getGOROOT()
			if err != nil {
				return err
			}
			if !detailedProgress {
				fmt.Fprintf(os.Stderr, "# Pushing GOROOT %q to %q...\n", goroot, inst)
			}
			if err := doPush(ctx, inst, goroot, false, detailedProgress); err != nil {
				return err
			}

			// Run make.bash or make.bat.
			cmd := "go/src/make.bash"
			if strings.Contains(builderType, "windows") {
				cmd = "go/src/make.bat"
			}

			// Create a file to write output to so it doesn't get lost.
			outf, err := os.Create(filepath.Join(tmpOutDir, fmt.Sprintf("%s.stdout", inst)))
			if err != nil {
				return err
			}
			defer func() {
				outf.Close()
				fmt.Fprintf(os.Stderr, "# Wrote results from %q to %q.\n", inst, outf.Name())
			}()
			fmt.Fprintf(os.Stderr, "# Streaming results from %q to %q...\n", inst, outf.Name())

			// If this is the only command running, print to stdout too, for convenience and
			// backwards compatibility.
			outputs := []io.Writer{outf}
			if detailedProgress {
				outputs = append(outputs, os.Stdout)
			} else {
				fmt.Fprintf(os.Stderr, "# Running %q on %q...\n", cmd, inst)
			}
			return doRun(ctx, inst, cmd, []string{}, runWriters(outputs...))
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}
	if group != nil {
		return storeGroup(group)
	}
	return nil
}
