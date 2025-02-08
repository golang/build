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
	  login      create authentication credentials for the gomote services
	  ls         list the contents of a directory on a buildlet
	  ping       test whether a buildlet is alive and reachable
	  push       sync your GOROOT directory to the buildlet
	  put        put files on a buildlet
	  put14      put Go 1.4 in place
	  puttar     extract a tar.gz to a buildlet
	  rm         delete files or directories
	  rdp        RDP (Remote Desktop Protocol) to a Windows buildlet
	  repro      reproduce a build by LUCI build ID
	  run        run a command on a buildlet
	  ssh        ssh to a buildlet

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

# Groups

Instances may be managed in named groups, and commands are broadcast to all
instances in the group.

A group is specified either by the -group global flag or through the
GOMOTE_GROUP environment variable. The -group flag must always specify a
valid group, whereas GOMOTE_GROUP may contain an invalid group.
Instances may be part of more than one group.

Groups may be explicitly managed with the "group" subcommand, but there
are several short-cuts that make this unnecessary in most cases:

  - The create command can create a new group for instances with the
    -new-group flag.
  - The create command will automatically create the group in GOMOTE_GROUP
    if it does not exist and no other group is explicitly specified.
  - The destroy command can destroy a group in addition to its instances
    with the -destroy-group flag.

As a result, the easiest way to use groups is to just set the
GOMOTE_GROUP environment variable:

	$ export GOMOTE_GROUP=debug
	$ gomote create linux-amd64
	$ GOROOT=/path/to/goroot gomote create linux-amd64
	$ gomote run go/src/make.bash

As this example demonstrates, groups are useful even if the group
contains only a single instance: it can dramatically shorten most gomote
commands.

# Tips and tricks

  - The create command accepts the -setup flag which also pushes a GOROOT
    and runs the appropriate equivalent of "make.bash" for the instance.
  - The create command accepts the -count flag for creating several
    instances at once.
  - The run command accepts the -collect flag for automatically writing
    the output from the command to a file in $PWD, as well as a copy of
    the full file tree from the instance. This command is useful for
    capturing the output of long-running commands in a set-and-forget
    manner.
  - The run command accepts the -until flag for continuously executing
    a command until the output of the command matches some pattern. Useful
    for reproducing rare issues, and especially useful when used in tandem
    with -collect.
  - The run command always streams output to a temporary file regardless
    of any additional flags to avoid losing output due to terminal
    scrollback. It always prints the location of the file.

Using some of these tricks, it's straightforward to hammer at some test
to reproduce a rare failure, like so:

	$ export GOMOTE_GROUP=debug
	$ GOROOT=/path/to/goroot gomote create -setup -count=10 linux-amd64
	$ gomote run -until='unexpected return pc' -collect go/bin/go run -run="MyFlakyTest" -count=100 runtime

# Legacy Infrastructure

Setting the GOMOTEDISABLELUCI environmental variable equal to true will set the gomote client to communicate with
the coordinator instead of the gomote server.
*/
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"sort"
	"strconv"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/iapclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	buildEnv    *buildenv.Environment
	activeGroup *groupData
	usageLogger *log.Logger = log.New(os.Stderr, "", 0)
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
	usageLogger.Printf(`Usage of gomote: gomote [global-flags] <cmd> [cmd-flags]

Global flags:
`)
	flag.PrintDefaults()
	usageLogger.Printf("Commands:\n\n")
	for _, name := range sortedCommands() {
		usageLogger.Printf("  %-13s %s\n", name, commands[name].des)
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
	registerCommand("create", "create a buildlet; with no args, list types of buildlets", create)
	registerCommand("destroy", "destroy a buildlet", destroy)
	registerCommand("gettar", "extract a tar.gz from a buildlet", getTar)
	registerCommand("group", "manage groups of instances", group)
	registerCommand("list", "list active buildlets", list)
	registerCommand("login", "authenticate with the gomote service", login)
	registerCommand("ls", "list the contents of a directory on a buildlet", ls)
	registerCommand("ping", "test whether a buildlet is alive and reachable ", ping)
	registerCommand("push", "sync your GOROOT directory to the buildlet", push)
	registerCommand("put", "put files on a buildlet", put)
	registerCommand("putbootstrap", "put bootstrap toolchain in place", putBootstrap)
	registerCommand("puttar", "extract a tar.gz to a buildlet", putTar)
	registerCommand("repro", "reproduce a build environment in a new buildlet", repro)
	registerCommand("rdp", "Unimplimented: RDP (Remote Desktop Protocol) to a Windows buildlet", rdp)
	registerCommand("rm", "delete files or directories", rm)
	registerCommand("run", "run a command on a buildlet", run)
	registerCommand("ssh", "ssh to a buildlet", ssh)
}

var (
	serverAddr = flag.String("server", "gomote.golang.org:443", "Address for GRPC server")
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("# ")

	// Set up and parse global flags.
	groupName := flag.String("group", os.Getenv("GOMOTE_GROUP"), "name of the gomote group to apply commands to (default is $GOMOTE_GROUP)")
	buildlet.RegisterFlags()
	registerCommands()
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
	}
	if luciDisabled() {
		*serverAddr = "build.golang.org:443"
	}
	// Set up globals.
	buildEnv = buildenv.FromFlags()
	if *groupName != "" {
		var err error
		activeGroup, err = loadGroup(*groupName)
		if os.Getenv("GOMOTE_GROUP") != *groupName {
			// Only fail hard since it was specified by the flag.
			if err != nil {
				log.Printf("Failure: %v\n", err)
				usage()
			}
		} else {
			// With a valid group from GOMOTE_GROUP,
			// make it explicit to the user that we're going
			// ahead with it. We don't need this with the flag
			// because it's explicit.
			if err == nil {
				log.Printf("Using group %q from GOMOTE_GROUP\n", *groupName)
			}
			// Note that an invalid group in GOMOTE_GROUP is OK.
		}
	}

	cmdName := args[0]
	cmd, ok := commands[cmdName]
	if !ok {
		log.Printf("Unknown command %q\n", cmdName)
		usage()
	}
	if err := cmd.run(args[1:]); err != nil {
		logAndExitf("Error running %s: %v\n", cmdName, err)
	}
}

// gomoteServerClient returns a gomote server client which can be used to interact with the gomote GRPC server.
// It will either retrieve a previously created authentication token or attempt to create a new one.
func gomoteServerClient(ctx context.Context) protos.GomoteServiceClient {
	grpcClient, err := iapclient.GRPCClient(ctx, *serverAddr)
	if err != nil {
		var authErr iapclient.AuthenticationError
		if errors.As(err, &authErr) {
			logAndExitf("Authentication error: %s\n\tLogin via: gomote login\n", err)
		}
		logAndExitf("dialing the server=%s failed with: %s\n", *serverAddr, err)
	}
	return protos.NewGomoteServiceClient(grpcClient)
}

// logAndExitf is equivalent to Printf to Stderr followed by a call to os.Exit(1).
func logAndExitf(format string, v ...interface{}) {
	log.Printf(format, v...)
	os.Exit(1)
}

func instanceDoesNotExist(err error) bool {
	for err != nil {
		if status.Code(err) == codes.NotFound {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

func luciDisabled() bool {
	on, _ := strconv.ParseBool(os.Getenv("GOMOTEDISABLELUCI"))
	return on
}
