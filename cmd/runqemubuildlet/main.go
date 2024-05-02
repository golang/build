// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Binary runqemubuildlet runs a single VM-based buildlet in a loop.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"golang.org/x/build/internal"
)

var (
	// Common flags
	guestOS    = flag.String("guest-os", "windows", "Guest OS to run (one of: windows or darwin)")
	healthzURL = flag.String("buildlet-healthz-url", "http://localhost:8080/healthz", "URL to buildlet /healthz endpoint.")

	// -guest-os=windows flags
	windows10Path = flag.String("windows-10-path", defaultWindowsDir(), "Path to Windows image and QEMU dependencies.")

	// -guest-os=darwin flags
	darwinPath = flag.String("darwin-path", defaultDarwinDir(), "Path to darwin image and QEMU dependencies.")
	// Using an int for this isn't great, but the only thing we need to do
	// is check if the version is >= 11.
	macosVersion = flag.Int("macos-version", 0, "macOS major version of guest image (e.g., 10, 11, 12, or 13)")
	guestIndex   = flag.Int("guest-index", 1, "Index indicating which of the two instances on this host that this is (one of: 1 or 2)")
	osk          = flag.String("osk", "", "Apple OSK key value")
)

func main() {
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	for ctx.Err() == nil {
		var cmd *exec.Cmd
		switch *guestOS {
		case "darwin":
			cmd = darwinCmd(*darwinPath)
		case "windows":
			cmd = windows10Cmd(*windows10Path)
		default:
			log.Fatalf("Unknown guest OS %q", *guestOS)
		}

		if err := runGuest(ctx, cmd); err != nil {
			log.Printf("runGuest() = %v. Retrying in 10 seconds.", err)
			time.Sleep(10 * time.Second)
			continue
		}
	}
}

func runGuest(ctx context.Context, cmd *exec.Cmd) error {
	log.Printf("Starting VM: %s", cmd.String())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cmd.Start() = %w", err)
	}
	ctx, cancel := heartbeatContext(ctx, 30*time.Second, 10*time.Minute, func(ctx context.Context) error {
		return checkBuildletHealth(ctx, *healthzURL)
	})
	defer cancel()
	if err := internal.WaitOrStop(ctx, cmd, os.Interrupt, time.Minute); err != nil {
		return fmt.Errorf("WaitOrStop(_, %v, %v, %v) = %w", cmd, os.Interrupt, time.Minute, err)
	}
	return nil
}
