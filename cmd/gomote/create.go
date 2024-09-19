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

func swarmingBuilders() ([]string, error) {
	ctx := context.Background()
	client := gomoteServerClient(ctx)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := client.ListSwarmingBuilders(ctx, &protos.ListSwarmingBuildersRequest{})
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve swarming builders: %s", err)
	}
	return resp.Builders, nil
}

func create(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.Usage = func() {
		log := usageLogger
		log.Print("create usage: gomote create [create-opts] <type>")
		fmt.Fprintln(os.Stderr)
		log.Print("If there's a valid group specified, new instances are")
		log.Print("automatically added to the group. If the group in")
		log.Print("$GOMOTE_GROUP doesn't exist, and there's no other group")
		log.Print("specified, it will be created and new instances will be")
		log.Print("added to that group.")
		fmt.Fprintln(os.Stderr)
		log.Print("Run 'gomote create -list' to see a list of valid builder")
		log.Print("types.")
		fmt.Fprintln(os.Stderr)
		log.Print("Flags:")
		fs.PrintDefaults()
		if luciDisabled() {
			log.Print("\nValid types:")
			for _, bt := range builders() {
				var warn string
				if bt.IsReverse {
					if bt.ExpectNum > 0 {
						warn = fmt.Sprintf("   [limited capacity: %d machines]", bt.ExpectNum)
					} else {
						warn = "   [limited capacity]"
					}
				}
				log.Printf("  * %s%s\n", bt.Name, warn)
			}
		}
		os.Exit(1)
	}
	var listBuilders bool
	fs.BoolVar(&listBuilders, "list", false, "list builder types and exit")
	var cfg createConfig
	fs.BoolVar(&cfg.printStatus, "status", true, "print regular status updates while waiting")
	fs.IntVar(&cfg.count, "count", 1, "number of instances to create")
	fs.BoolVar(&cfg.setup, "setup", false, "set up the instance by pushing GOROOT and building the Go toolchain")
	fs.StringVar(&cfg.newGroup, "new-group", "", "also create a new group and add the new instances to it")
	fs.BoolVar(&cfg.useGolangbuild, "use-golangbuild", true, "disable the installation of build dependencies installed by golangbuild")

	fs.Parse(args)
	if listBuilders {
		swarmingBuilders, err := swarmingBuilders()
		if err != nil {
			return err
		}
		for _, builder := range swarmingBuilders {
			fmt.Fprintln(os.Stdout, builder)
		}
		return nil
	}
	if fs.NArg() != 1 {
		fs.Usage()
	}
	builderType := fs.Arg(0)
	_, _, err := createInstances(context.Background(), builderType, &cfg)
	return err
}

type createConfig struct {
	printStatus    bool
	count          int
	setup          bool
	newGroup       string
	useGolangbuild bool
}

func createInstances(ctx context.Context, builderType string, cfg *createConfig) ([]string, *groupData, error) {
	var groupMu sync.Mutex
	group := activeGroup
	var err error
	if cfg.newGroup != "" {
		group, err = doCreateGroup(cfg.newGroup)
		if err != nil {
			return nil, nil, err
		}
	}
	if group == nil && os.Getenv("GOMOTE_GROUP") != "" {
		group, err = doCreateGroup(os.Getenv("GOMOTE_GROUP"))
		if err != nil {
			return nil, nil, err
		}
	}

	var instancesMu sync.Mutex
	var instances []string
	var tmpOutDir string
	var tmpOutDirOnce sync.Once
	eg, ctx := errgroup.WithContext(ctx)
	client := gomoteServerClient(ctx)
	for i := 0; i < cfg.count; i++ {
		i := i
		eg.Go(func() error {
			start := time.Now()
			var exp []string
			if !cfg.useGolangbuild {
				exp = append(exp, "disable-golang-build")
			}
			stream, err := client.CreateInstance(ctx, &protos.CreateInstanceRequest{BuilderType: builderType, ExperimentOption: exp})
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
				case update.GetStatus() != protos.CreateInstanceResponse_COMPLETE && cfg.printStatus:
					log.Printf("still creating %s (%d) after %v; %d requests ahead of you\n", builderType, i+1, time.Since(start).Round(time.Second), update.GetWaitersAhead())
				case update.GetStatus() == protos.CreateInstanceResponse_COMPLETE:
					inst = update.GetInstance().GetGomoteId()
				}
			}
			fmt.Println(inst)

			instancesMu.Lock()
			instances = append(instances, inst)
			instancesMu.Unlock()

			if group != nil {
				groupMu.Lock()
				group.Instances = append(group.Instances, inst)
				groupMu.Unlock()
			}
			if !cfg.setup {
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
			detailedProgress := cfg.count == 1
			goroot, err := getGOROOT()
			if err != nil {
				return err
			}
			if !detailedProgress {
				log.Printf("Pushing GOROOT %q to %q...\n", goroot, inst)
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
				log.Printf("Wrote results from %q to %q.\n", inst, outf.Name())
			}()
			log.Printf("Streaming results from %q to %q...\n", inst, outf.Name())

			// If this is the only command running, print to stdout too, for convenience and
			// backwards compatibility.
			outputs := []io.Writer{outf}
			if detailedProgress {
				outputs = append(outputs, os.Stdout)
			} else {
				log.Printf("Running %q on %q...\n", cmd, inst)
			}
			return doRun(ctx, inst, cmd, []string{}, runWriters(outputs...))
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, nil, err
	}
	if group != nil {
		if err := storeGroup(group); err != nil {
			return nil, nil, err
		}
	}
	return instances, group, nil
}
