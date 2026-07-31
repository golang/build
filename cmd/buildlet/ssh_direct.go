// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !plan9

package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"

	"github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
)

// sshSubsystems are the subsystem handlers for the buildlet's SSH server.
var sshSubsystems = map[string]ssh.SubsystemHandler{
	"sftp": sshHandlerSFTP,
}

// sshHandlerSFTP serves the sftp subsystem, which scp and sftp use to
// copy files to and from the buildlet (go.dev/issue/21140). Relative
// paths name files in the buildlet's work directory.
func sshHandlerSFTP(s ssh.Session) {
	srv, err := sftp.NewServer(s, sftp.WithServerWorkingDirectory(*workDir))
	if err != nil {
		log.Printf("starting sftp server: %s", err)
		fmt.Fprintf(s.Stderr(), "starting sftp server: %s\n", err)
		s.Exit(255)
		return
	}
	defer srv.Close()
	if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
		log.Printf("sftp server: %s", err)
		fmt.Fprintf(s.Stderr(), "sftp server: %s\n", err)
		s.Exit(255)
		return
	}
	s.Exit(0)
}

// sshHandlerDirect handles a session that did not request a pty: an
// exec request ("gomote ssh instance cmd..."), a shell reading
// commands from piped standard input, or the legacy scp protocol
// (go.dev/issue/21140). It connects the command to the session's own
// streams instead of a pty, so data passes through byte for byte, and
// it propagates the command's exit status.
func sshHandlerDirect(s ssh.Session) {
	fail := func(format string, args ...any) {
		fmt.Fprintf(s.Stderr(), format, args...)
		s.Exit(255)
	}
	cmd := shellCommand(s.Context(), s.RawCommand())
	cmd.Dir = *workDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fail("%v\n", err)
		return
	}
	cmd.Stdout = s
	cmd.Stderr = s.Stderr()
	if err := cmd.Start(); err != nil {
		log.Printf("unable to start shell: %s", err)
		fail("unable to start shell %q: %s\n", shell(), err)
		return
	}
	// Copy session input on the side: the copy blocks until the client
	// sends EOF or disconnects, which must not keep Wait from returning
	// once the command exits.
	go func() {
		io.Copy(stdin, s)
		stdin.Close()
	}()
	err = cmd.Wait()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code = exitErr.ExitCode(); code < 0 {
			code = 255
		}
	} else if err != nil {
		fail("running shell %q: %s\n", shell(), err)
		return
	}
	s.Exit(code)
}
