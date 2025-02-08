// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !plan9 && !windows

package main

import (
	"fmt"
	"io"
	"log"
	"os/exec"

	"github.com/creack/pty"
	"github.com/gliderlabs/ssh"
	"golang.org/x/build/internal/envutil"
)

func startSSHServerSwarming() {
	buildletSSHServer = &ssh.Server{
		Addr:    "localhost:" + sshPort(),
		Handler: sshHandler,
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

func sshHandler(s ssh.Session) {
	ptyReq, winCh, isPty := s.Pty()
	if !isPty {
		fmt.Fprint(s, "scp is not supported\n")
		return
	}
	var cmd *exec.Cmd
	cmd = exec.Command(shell())

	envutil.SetEnv(cmd, "TERM="+ptyReq.Term)
	f, err := pty.Start(cmd)
	if err != nil {
		fmt.Fprintf(s, "unable to start shell %q: %s\n", shell(), err)
		log.Printf("unable to start shell: %s", err)
		return
	}
	defer f.Close()
	go func() {
		for win := range winCh {
			pty.Setsize(f, &pty.Winsize{
				Rows: uint16(win.Height),
				Cols: uint16(win.Width),
			})
		}
	}()
	go func() {
		io.Copy(f, s) // stdin
	}()
	io.Copy(s, f) // stdout
	cmd.Process.Kill()
	cmd.Wait()
}
