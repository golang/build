// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
The gomote command is a client for the Go builder infrastructure.
It's a remote control for remote Go builder machines.

See https://golang.org/wiki/Gomote

Usage:

	gomote [global-flags] cmd [cmd-flags]

	For example,
	$ gomote create openbsd-amd64-60
	user-username-openbsd-amd64-60-0
	$ gomote push user-username-openbsd-amd64-60-0
	$ gomote run user-username-openbsd-amd64-60-0 go/src/make.bash
	$ gomote run user-username-openbsd-amd64-60-0 go/bin/go test -v -short os

To list the subcommands, run "gomote" without arguments:

	Commands:

	  create     create a buildlet; with no args, list types of buildlets
	  destroy    destroy a buildlet
	  gettar     extract a tar.gz from a buildlet
	  list       list active buildlets
	  ls         list the contents of a directory on a buildlet
	  ping       test whether a buildlet is alive and reachable
	  push       sync your GOROOT directory to the buildlet
	  put        put files on a buildlet
	  put14      put Go 1.4 in place
	  puttar     extract a tar.gz to a buildlet
	  rm         delete files or directories
	  rdp        RDP (Remote Desktop Protocol) to a Windows buildlet
	  run        run a command on a buildlet
	  ssh        ssh to a buildlet
	  v2         version 2 of the gomote API

To list all the builder types available, run "create" with no arguments:

	$ gomote create
	(list tons of buildlet types)

The "gomote run" command has many of its own flags:

	$ gomote run -h
	run usage: gomote run [run-opts] <instance> <cmd> [args...]
	  -builderenv string
	        Optional alternate builder to act like. Must share the same
	        underlying buildlet host type, or it's an error. For
	        instance, linux-amd64-race is compatible
	        with linux-amd64, but openbsd-amd64 and openbsd-386 are
	        different hosts.
	  -debug
	        write debug info about the command's execution before it begins
	  -dir string
	        Directory to run from. Defaults to the directory of the
	        command, or the work directory if -system is true.
	  -e value
	        Environment variable KEY=value. The -e flag may be repeated
	        multiple times to add multiple things to the environment.
	  -path string
	        Comma-separated list of ExecOpts.Path elements. The special
	        string 'EMPTY' means to run without any $PATH. The empty
	        string (default) does not modify the $PATH. Otherwise, the
	        following expansions apply: the string '$PATH' expands to
	        the current PATH element(s), the substring '$WORKDIR'
	        expands to the buildlet's temp workdir.
	  -system
	        run inside the system, and not inside the workdir; this is implicit if cmd starts with '/'

# Debugging buildlets directly

Using "gomote create" contacts the build coordinator
(farmer.golang.org) and requests that it create the buildlet on your
behalf. All subsequent commands (such as "gomote run" or "gomote ls")
then proxy your request via the coordinator.  To access a buildlet
directly (for example, when working on the buildlet code), you can
skip the "gomote create" step and use the special builder name
"<build-config-name>@ip[:port>", such as "windows-amd64-2008@10.1.5.3".
*/
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/iapclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/oauth"
	"google.golang.org/grpc/status"
)

var (
	buildEnv *buildenv.Environment
)

type command struct {
	name string
	des  string
	run  func([]string) error
}

var commands = map[string]command{}

func sortedCommands() []string {
	s := make([]string, 0, len(commands))
	for name := range commands {
		s = append(s, name)
	}
	sort.Strings(s)
	return s
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage of gomote: gomote [global-flags] <cmd> [cmd-flags]

Global flags:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "Commands:\n\n")
	for _, name := range sortedCommands() {
		fmt.Fprintf(os.Stderr, "  %-10s %s\n", name, commands[name].des)
	}
	os.Exit(1)
}

func registerCommand(name, des string, run func([]string) error) {
	if _, dup := commands[name]; dup {
		panic("duplicate registration of " + name)
	}
	commands[name] = command{
		name: name,
		des:  des,
		run:  run,
	}
}

func registerCommands() {
	registerCommand("create", "create a buildlet; with no args, list types of buildlets", legacyCreate)
	registerCommand("destroy", "destroy a buildlet", legacyDestroy)
	registerCommand("gettar", "extract a tar.gz from a buildlet", legacyGetTar)
	registerCommand("ls", "list the contents of a directory on a buildlet", legacyLs)
	registerCommand("list", "list active buildlets", legacyList)
	registerCommand("ping", "test whether a buildlet is alive and reachable ", legacyPing)
	registerCommand("push", "sync your GOROOT directory to the buildlet", legacyPush)
	registerCommand("put", "put files on a buildlet", legacyPut)
	registerCommand("put14", "put Go 1.4 in place", put14)
	registerCommand("puttar", "extract a tar.gz to a buildlet", legacyPutTar)
	registerCommand("rdp", "RDP (Remote Desktop Protocol) to a Windows buildlet", rdp)
	registerCommand("rm", "delete files or directories", legacyRm)
	registerCommand("run", "run a command on a buildlet", legacyRun)
	registerCommand("ssh", "ssh to a buildlet", legacySSH)
	registerCommand("v2", "version 2 of the gomote commands", version2)
}

var (
	serverAddr = flag.String("server", "build.golang.org:443", "Address for GRPC server")
)

func main() {
	buildlet.RegisterFlags()
	registerCommands()
	flag.Usage = usage
	flag.Parse()
	buildEnv = buildenv.FromFlags()
	args := flag.Args()
	if len(args) == 0 {
		usage()
	}
	cmdName := args[0]
	cmd, ok := commands[cmdName]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown command %q\n", cmdName)
		usage()
	}
	err := cmd.run(args[1:])
	if err != nil {
		logAndExitf("Error running %s: %v\n", cmdName, err)
	}
}

// gomoteServerClient returns a gomote server client which can be used to interact with the gomote GRPC server.
// It will either retrieve a previously created authentication token or attempt to create a new one.
func gomoteServerClient(ctx context.Context) protos.GomoteServiceClient {
	ts, err := iapclient.TokenSource(ctx)
	if err != nil {
		logAndExitf("failed to retrieve oauth token: %s", err)
	}
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: strings.HasPrefix(*serverAddr, "localhost:")})),
		grpc.WithDefaultCallOptions(grpc.PerRPCCredentials(oauth.TokenSource{TokenSource: ts})),
		grpc.WithBlock(),
	}
	grpcClient, err := grpc.DialContext(ctx, *serverAddr, opts...)
	if err != nil {
		logAndExitf("dialing the server=%s failed with: %s", *serverAddr, err)
	}
	return protos.NewGomoteServiceClient(grpcClient)
}

type subCommand func([]string) error

// version2 manages how version 2 subcommands are called.
func version2(args []string) error {
	cm := map[string]subCommand{

		"create":       create,
		"destroy":      destroy,
		"list":         list,
		"ls":           ls,
		"run":          run,
		"ping":         ping,
		"ssh":          ssh,
		"rm":           rm,
		"gettar":       getTar,
		"put":          put,
		"puttar":       putTar,
		"putbootstrap": putBootstrap,
		"push":         push,
	}
	if len(args) == 0 {
		usage()
	}
	subCmd := args[0]
	sc, ok := cm[subCmd]
	if !ok {
		return fmt.Errorf("unknown sub-command %q\n", subCmd)
	}
	return sc(args[1:])
}

// logAndExitf is equivalent to Printf to Stderr followed by a call to os.Exit(1).
func logAndExitf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, format, v...)
	os.Exit(1)
}

// statusFromError returns the message portion of a GRPC error.
func statusFromError(err error) string {
	return status.Convert(err).Message()
}
