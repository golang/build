// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func init() {
	runCmdContext = runCmdContextLinux
}

// runCmdContextLinux runs cmd controlled by ctx, killing it and all its
// children if necessary. cmd.SysProcAttr must be unset.
func runCmdContextLinux(ctx context.Context, cmd *exec.Cmd) error {
	if cmd.SysProcAttr != nil {
		return fmt.Errorf("cmd.SysProcAttr must be nil")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	resChan := make(chan error, 1)
	go func() {
		resChan <- cmd.Wait()
	}()

	select {
	case err := <-resChan:
		return err
	case <-ctx.Done():
	}
	// Canceled. Interrupt and see if it ends voluntarily.
	cmd.Process.Signal(os.Interrupt)
	select {
	case <-resChan:
		return ctx.Err()
	case <-time.After(time.Second):
	}
	// Didn't shut down in response to interrupt. It may have child processes
	// holding stdout/sterr open. Kill its process group hard.
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-resChan
	return ctx.Err()
}
