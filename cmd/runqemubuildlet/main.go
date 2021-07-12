// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

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
	"path/filepath"
	"time"

	"golang.org/x/build/internal"
)

var (
	windows10Path = flag.String("windows-10-path", defaultWindowsDir(), "Path to Windows image and QEMU dependencies.")
)

func main() {
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	for ctx.Err() == nil {
		cmd := windows10Cmd(*windows10Path)
		log.Printf("Starting VM: %s", cmd.String())
		if err := cmd.Start(); err != nil {
			log.Printf("cmd.Start() = %v. Retrying in 10 seconds.", err)
			time.Sleep(10 * time.Second)
			continue
		}
		if err := internal.WaitOrStop(ctx, cmd, os.Interrupt, time.Minute); err != nil {
			log.Printf("waitOrStop(_, %v, %v, %v) = %v", cmd, os.Interrupt, time.Minute, err)
		}
	}
}

// defaultWindowsDir returns a default path for a Windows VM.
//
// The directory should contain the Windows VM image, and UTM
// components (UTM.app and sysroot-macos-arm64).
func defaultWindowsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("os.UserHomeDir() = %q, %v", home, err)
		return ""
	}
	return filepath.Join(home, "macmini-windows")
}

// windows10Cmd returns a qemu command for running a Windows VM, ready
// to be started.
func windows10Cmd(base string) *exec.Cmd {
	c := exec.Command(filepath.Join(base, "sysroot-macos-arm64/bin/qemu-system-aarch64"),
		fmt.Sprintf("-L %s", filepath.Join(base, "UTM.app/Contents/Resources/qemu")),
		"-device ramfb",
		"-cpu max",
		"-smp cpus=8,sockets=1,cores=8,threads=1", // This works well with M1 Mac Minis.
		"-machine virt,highmem=off",
		"-accel hvf",
		"-accel tcg,tb-size=1536",
		"-boot menu=on",
		"-m 12288",
		`-name "Virtual Machine"`,
		"-device qemu-xhci,id=usb-bus",
		"-device usb-tablet,bus=usb-bus.0",
		"-device usb-mouse,bus=usb-bus.0",
		"-device usb-kbd,bus=usb-bus.0",
		fmt.Sprintf("-bios %s", filepath.Join(base, "Images/QEMU_EFI.fd")),
		"-device nvme,drive=drive0,serial=drive0,bootindex=0",
		fmt.Sprintf(`-drive "if=none,media=disk,id=drive0,file=%s,cache=writethrough"`, filepath.Join(base, "Images/win10.qcow2")),
		"-device usb-storage,drive=drive2,removable=true,bootindex=1",
		fmt.Sprintf(`-drive "if=none,media=cdrom,id=drive2,file=%s,cache=writethrough"`, filepath.Join(base, "Images/virtio.iso")),
		"-device virtio-net-pci,netdev=net0",
		"-netdev user,id=net0",
		"-snapshot", // critical to avoid saving state between runs.
		"-vnc :3",
	)
	c.Env = append(os.Environ(),
		fmt.Sprintf("DYLD_LIBRARY_PATH=%s", filepath.Join(base, "sysroot-macos-arm64/lib")),
	)
	return c
}
