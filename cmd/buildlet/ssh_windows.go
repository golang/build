// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"syscall"

	"github.com/UserExistsError/conpty"
	"github.com/gliderlabs/ssh"
)

func startSSHServerSwarming() {
	buildletSSHServer = &ssh.Server{
		Addr:              "localhost:" + sshPort(),
		Handler:           sshHandler,
		SubsystemHandlers: sshSubsystems,
		PublicKeyHandler: func(ctx ssh.Context, key ssh.PublicKey) bool {
			allowed, _, _, _, err := ssh.ParseAuthorizedKey(buldletAuthKeys)
			if err != nil {
				log.Printf("error parsing authorized key: %s", err)
				return false
			}
			return ssh.KeysEqual(key, allowed)
		},
	}
	go func() {
		err := buildletSSHServer.ListenAndServe()
		if err != nil {
			log.Printf("buildlet SSH Server stopped: %s", err)
		}
	}()
	teardownFuncs = append(teardownFuncs, func() {
		buildletSSHServer.Close()
		log.Println("shutting down SSH Server")
	})
}

// shellCommand returns a command that runs rawCmd using the shell,
// or the shell itself reading commands from standard input
// if rawCmd is empty.
func shellCommand(ctx context.Context, rawCmd string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, shell())
	if rawCmd != "" {
		// cmd.exe does not parse its command line using the standard
		// Windows quoting rules that os/exec applies to Args, so set
		// the command line directly and pass rawCmd through verbatim.
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CmdLine: `"` + shell() + `" /c ` + rawCmd,
		}
	}
	return cmd
}

func sshHandler(s ssh.Session) {
	ptyReq, winCh, isPty := s.Pty()
	if !isPty {
		sshHandlerDirect(s)
		return
	}
	f, err := conpty.Start(shell(), conpty.ConPtyDimensions(ptyReq.Window.Width, ptyReq.Window.Height), conpty.ConPtyWorkDir(*workDir))
	if err != nil {
		fmt.Fprintf(s, "unable to start shell %q: %s\n", shell(), err)
		log.Printf("unable to start shell: %s", err)
		return
	}
	defer f.Close()
	go func() {
		for win := range winCh {
			err := f.Resize(win.Width, win.Height)
			if err != nil {
				log.Printf("error resizing pty: %s", err)
			}
		}
	}()
	go io.Copy(f, s) // stdin
	go io.Copy(s, f) // stdout
	_, err = f.Wait(s.Context())
	if err != nil {
		log.Printf("Error: %s", err)
		return
	}
}
