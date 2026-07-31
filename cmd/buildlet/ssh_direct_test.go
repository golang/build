// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !plan9 && !windows

package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gssh "github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// testSSHClient starts an SSH server running sshHandler, like the one
// the buildlet runs on swarming bots, and returns a client connected
// to it.
func testSSHClient(t *testing.T) *ssh.Client {
	t.Helper()
	// Use a known shell: the tests below use POSIX shell syntax,
	// and shell() consults $SHELL on some systems.
	t.Setenv("SHELL", "/bin/sh")

	oldWorkDir := *workDir
	*workDir = t.TempDir()
	t.Cleanup(func() { *workDir = oldWorkDir })

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &gssh.Server{Handler: sshHandler, SubsystemHandlers: sshSubsystems}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	c, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestSSHHandlerExec tests an exec request without a pty,
// as in "gomote ssh instance cmd...".
func TestSSHHandlerExec(t *testing.T) {
	c := testSSHClient(t)
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	err = sess.Run("echo out; echo err >&2; exit 7")
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run = %v, want exit status 7", err)
	}
	if code := exitErr.ExitStatus(); code != 7 {
		t.Errorf("exit status = %d, want 7", code)
	}
	if got := stdout.String(); got != "out\n" {
		t.Errorf("stdout = %q, want %q", got, "out\n")
	}
	if got := stderr.String(); got != "err\n" {
		t.Errorf("stderr = %q, want %q", got, "err\n")
	}
}

// TestSSHHandlerShell tests a shell request without a pty,
// which reads commands from standard input.
func TestSSHHandlerShell(t *testing.T) {
	c := testSSHClient(t)
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var stdout bytes.Buffer
	sess.Stdin = strings.NewReader("echo hello\nexit 3\n")
	sess.Stdout = &stdout
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	err = sess.Wait()
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Wait = %v, want exit status 3", err)
	}
	if code := exitErr.ExitStatus(); code != 3 {
		t.Errorf("exit status = %d, want 3", code)
	}
	if got := stdout.String(); got != "hello\n" {
		t.Errorf("stdout = %q, want %q", got, "hello\n")
	}
}

// TestSSHHandlerSFTP tests the sftp subsystem that scp uses,
// including that relative paths name files in the work directory.
func TestSSHHandlerSFTP(t *testing.T) {
	c := testSSHClient(t)
	client, err := sftp.NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	f, err := client.Create("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(*workDir, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" {
		t.Errorf("hello.txt = %q, want %q", data, "hello\n")
	}

	f, err = client.Open("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err = io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" {
		t.Errorf("read back hello.txt = %q, want %q", data, "hello\n")
	}
}

// TestSSHHandlerStdinOpen tests that a command that ignores its
// standard input finishes even though the client is still holding
// standard input open, as an interactive ssh client does.
func TestSSHHandlerStdinOpen(t *testing.T) {
	c := testSSHClient(t)
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	pr, pw := io.Pipe()
	defer pw.Close()
	sess.Stdin = pr
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Start("echo done"); err != nil {
		t.Fatal(err)
	}
	// If the handler waits for standard input to close, the session
	// channel never closes and the read below never returns; closing
	// the connection makes the test fail instead of time out.
	var stuck atomic.Bool
	timer := time.AfterFunc(30*time.Second, func() {
		stuck.Store(true)
		c.Close()
	})
	defer timer.Stop()
	out, err := io.ReadAll(stdout)
	if stuck.Load() {
		t.Fatal("session did not finish while standard input was open")
	}
	if err != nil {
		t.Fatalf("reading stdout: %v", err)
	}
	if got := string(out); got != "done\n" {
		t.Errorf("stdout = %q, want %q", got, "done\n")
	}
}
