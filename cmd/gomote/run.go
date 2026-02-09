// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stringSlice implements flag.Value, specifically for storing environment
// variable key=value pairs.
type stringSlice []string

func (*stringSlice) String() string { return "" } // default value

func (ss *stringSlice) Set(v string) error {
	if v != "" {
		if !strings.Contains(v, "=") {
			return fmt.Errorf("-e argument %q doesn't contains an '=' sign.", v)
		}
		*ss = append(*ss, v)
	}
	return nil
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Usage = func() {
		usageLogger.Print("run usage: gomote run [run-opts] <instance> <cmd> [args...]")
		fs.PrintDefaults()
		os.Exit(1)
	}
	var sys bool
	fs.BoolVar(&sys, "system", false, "run inside the system, and not inside the workdir; this is implicit if cmd starts with '/'")
	var debug bool
	fs.BoolVar(&debug, "debug", false, "write debug info about the command's execution before it begins")
	var env stringSlice
	fs.Var(&env, "e", "Environment variable KEY=value. The -e flag may be repeated multiple times to add multiple things to the environment.")
	var firewall bool
	fs.BoolVar(&firewall, "firewall", false, "Enable outbound firewall on machine. This is on by default on many builders (where supported) but disabled by default on gomote for ease of debugging. Once any command has been run with the -firewall flag on, it's on for the lifetime of that gomote instance.")
	var path string
	fs.StringVar(&path, "path", "", "Comma-separated list of ExecOpts.Path elements. The special string 'EMPTY' means to run without any $PATH. The empty string (default) does not modify the $PATH. Otherwise, the following expansions apply: the string '$PATH' expands to the current PATH element(s), the substring '$WORKDIR' expands to the buildlet's temp workdir.")

	var dir string
	fs.StringVar(&dir, "dir", "", "Directory to run from. Defaults to the directory of the command, or the work directory if -system is true.")
	var builderEnv string
	fs.StringVar(&builderEnv, "builderenv", "", "Optional alternate builder to act like. Must share the same underlying buildlet host type, or it's an error. For instance, linux-amd64-race or linux-386-387 are compatible with linux-amd64, but openbsd-amd64 and openbsd-386 are different hosts.")

	var collect bool
	fs.BoolVar(&collect, "collect", false, "Collect artifacts (stdout, work dir .tar.gz) into $PWD once complete.")

	var untilPattern string
	fs.StringVar(&untilPattern, "until", "", "Run command repeatedly until the output matches the provided regexp.")

	fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
	}

	var until *regexp.Regexp
	var err error
	if untilPattern != "" {
		until, err = regexp.Compile(untilPattern)
		if err != nil {
			return fmt.Errorf("bad regexp %q for 'until': %w", untilPattern, err)
		}
	}

	var cmd string
	var cmdArgs []string
	var runSet []string

	// First check if the instance name refers to a live instance.
	ctx := context.Background()
	if err := doPing(ctx, fs.Arg(0)); instanceDoesNotExist(err) {
		// When there's no active group, this is just an error.
		if activeGroup == nil {
			return fmt.Errorf("instance %q: %w", fs.Arg(0), err)
		}
		// When there is an active group, this just means that we're going
		// to use the group instead and assume the rest is a command.
		for _, inst := range activeGroup.Instances {
			runSet = append(runSet, inst)
		}
		cmd = fs.Arg(0)
		cmdArgs = fs.Args()[1:]
	} else if err == nil {
		runSet = append(runSet, fs.Arg(0))
		if fs.NArg() == 1 {
			log.Print("missing command")
			fs.Usage()
		}
		cmd = fs.Arg(1)
		cmdArgs = fs.Args()[2:]
	} else {
		return fmt.Errorf("checking instance %q: %w", fs.Arg(0), err)
	}

	var pathOpt []string
	if path == "EMPTY" {
		pathOpt = []string{} // non-nil
	} else if path != "" {
		pathOpt = strings.Split(path, ",")
	}

	// Create temporary directory for output.
	// This is useful even if we don't have multiple gomotes running, since
	// it's easy to accidentally lose the output.
	var outDir string
	if collect {
		outDir, err = os.Getwd()
		if err != nil {
			return err
		}
	} else {
		outDir, err = os.MkdirTemp("", "gomote")
		if err != nil {
			return err
		}
	}

	var cmdsFailedMu sync.Mutex
	var cmdsFailed []*cmdFailedError
	eg, ctx := errgroup.WithContext(context.Background())
	for _, inst := range runSet {
		if len(runSet) > 1 {
			// There's more than one instance running the command, so let's
			// be explicit about that.
			log.Printf("Running command on %q...\n", inst)
		}
		eg.Go(func() error {
			// Create a file to write output to so it doesn't get lost.
			outf, err := os.Create(filepath.Join(outDir, fmt.Sprintf("%s.stdout", inst)))
			if err != nil {
				return err
			}
			defer func() {
				outf.Close()
				log.Printf("Wrote results from %q to %q.\n", inst, outf.Name())
			}()
			log.Printf("Streaming results from %q to %q...\n", inst, outf.Name())

			outputs := []io.Writer{outf}
			// If this is the only command running, print to stdout too, for convenience and
			// backwards compatibility.
			if len(runSet) == 1 {
				outputs = append(outputs, os.Stdout)
			}
			// Give ourselves the output too so that we can match against it.
			var outBuf bytes.Buffer
			if until != nil {
				outputs = append(outputs, &outBuf)
			}
			var ce *cmdFailedError
			for {
				err := doRun(
					ctx,
					inst,
					cmd,
					cmdArgs,
					runDir(dir),
					runBuilderEnv(builderEnv),
					runEnv(env),
					runPath(pathOpt),
					runSystem(sys),
					runDebug(debug),
					runFirewall(firewall),
					runWriters(outputs...),
				)
				// If it's just that the command failed, don't exit just yet, and don't return
				// an error to the errgroup because we want the other commands to keep going.
				if err != nil {
					var ok bool
					ce, ok = err.(*cmdFailedError)
					if !ok {
						return err
					}
				}
				if until == nil || until.Match(outBuf.Bytes()) {
					break
				}
				// Reset the output file and our buffer for the next run.
				outBuf.Reset()
				if err := outf.Truncate(0); err != nil {
					return fmt.Errorf("failed to truncate output file %q: %w", outf.Name(), err)
				}

				log.Printf("No match found on %q, running again...\n", inst)
			}
			if until != nil {
				log.Printf("Match found on %q.\n", inst)
			}
			if ce != nil {
				// N.B. If err this wasn't a cmdFailedError
				cmdsFailedMu.Lock()
				cmdsFailed = append(cmdsFailed, ce)
				cmdsFailedMu.Unlock()
				// Write out the error.
				_, err := io.MultiWriter(outputs...).Write([]byte(ce.Error() + "\n"))
				if err != nil {
					log.Printf("failed to write error to output: %v", err)
				}
			}
			if collect {
				f, err := os.Create(fmt.Sprintf("%s.tar.gz", inst))
				if err != nil {
					log.Printf("failed to create file to write instance tarball: %v", err)
					return nil
				}
				defer f.Close()
				log.Printf("Downloading work dir tarball for %q to %q...\n", inst, f.Name())
				if err := doGetTar(ctx, inst, ".", f); err != nil {
					log.Printf("failed to retrieve instance tarball: %v", err)
					return nil
				}
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}
	// Handle failed commands separately so that we can let all the instances finish
	// running. We still want to handle them, though, because we want to make sure
	// we exit with a non-zero exit code to reflect the command failure.
	for _, ce := range cmdsFailed {
		log.Printf("Command %q failed on %q: %v\n", ce.cmd, ce.inst, err)
	}
	if len(cmdsFailed) > 0 {
		return errors.New("one or more commands failed")
	}
	return nil
}

func doRun(ctx context.Context, inst, cmd string, cmdArgs []string, opts ...runOpt) error {
	cfg := &runCfg{
		req: protos.ExecuteCommandRequest{
			AppendEnvironment: []string{},
			Args:              cmdArgs,
			Command:           cmd,
			Path:              []string{},
			GomoteId:          inst,
		},
	}
	for _, opt := range opts {
		opt(cfg)
	}
	if !cfg.req.SystemLevel {
		cfg.req.SystemLevel = strings.HasPrefix(cmd, "/")
	}

	outWriter := io.MultiWriter(cfg.outputs...)
	client := gomoteServerClient(ctx)
	stream, err := client.ExecuteCommand(ctx, &cfg.req)
	if err != nil {
		return fmt.Errorf("unable to execute %s: %w", cmd, err)
	}
	for {
		update, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			// execution error
			if status.Code(err) == codes.Aborted {
				return &cmdFailedError{inst: inst, cmd: cmd, err: err}
			}
			// remote error
			return fmt.Errorf("unable to execute %s: %w", cmd, err)
		}
		fmt.Fprint(outWriter, string(update.GetOutput()))
	}
}

type cmdFailedError struct {
	inst, cmd string
	err       error
}

func (e *cmdFailedError) Error() string {
	return fmt.Sprintf("Error trying to execute %s: %v", e.cmd, e.err)
}

func (e *cmdFailedError) Unwrap() error {
	return e.err
}

type runCfg struct {
	outputs []io.Writer
	req     protos.ExecuteCommandRequest
}

type runOpt func(*runCfg)

func runBuilderEnv(builderEnv string) runOpt {
	return func(r *runCfg) {
		r.req.ImitateHostType = builderEnv
	}
}

func runDir(dir string) runOpt {
	return func(r *runCfg) {
		r.req.Directory = dir
	}
}

func runEnv(env []string) runOpt {
	return func(r *runCfg) {
		r.req.AppendEnvironment = append(r.req.AppendEnvironment, env...)
	}
}

func runPath(path []string) runOpt {
	return func(r *runCfg) {
		r.req.Path = append(r.req.Path, path...)
	}
}

func runDebug(debug bool) runOpt {
	return func(r *runCfg) {
		r.req.Debug = debug
	}
}

func runSystem(sys bool) runOpt {
	return func(r *runCfg) {
		r.req.SystemLevel = sys
	}
}

func runFirewall(firewall bool) runOpt {
	return func(r *runCfg) {
		r.req.AppendEnvironment = append(r.req.AppendEnvironment, "GO_DISABLE_OUTBOUND_NETWORK="+fmt.Sprint(firewall))
	}
}

func runWriters(writers ...io.Writer) runOpt {
	return func(r *runCfg) {
		r.outputs = writers
	}
}
