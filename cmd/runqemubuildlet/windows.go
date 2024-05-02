// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

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
		"-L", filepath.Join(base, "UTM.app/Contents/Resources/qemu"),
		"-cpu", "max",
		"-smp", "cpus=8,sockets=1,cores=8,threads=1", // This works well with M1 Mac Minis.
		"-machine", "virt,highmem=off",
		"-accel", "hvf",
		"-accel", "tcg,tb-size=1536",
		"-boot", "menu=on",
		"-m", "12288",
		"-name", "Virtual Machine",
		"-device", "qemu-xhci,id=usb-bus",
		"-device", "ramfb",
		"-device", "usb-tablet,bus=usb-bus.0",
		"-device", "usb-mouse,bus=usb-bus.0",
		"-device", "usb-kbd,bus=usb-bus.0",
		"-device", "virtio-net-pci,netdev=net0",
		"-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:8080-:8080",
		"-bios", filepath.Join(base, "Images/QEMU_EFI.fd"),
		"-device", "nvme,drive=drive0,serial=drive0,bootindex=0",
		"-drive", fmt.Sprintf("if=none,media=disk,id=drive0,file=%s,cache=writethrough", filepath.Join(base, "Images/win10.qcow2")),
		"-device", "usb-storage,drive=drive2,removable=true,bootindex=1",
		"-drive", fmt.Sprintf("if=none,media=cdrom,id=drive2,file=%s,cache=writethrough", filepath.Join(base, "Images/virtio.iso")),
		"-snapshot", // critical to avoid saving state between runs.
		"-vnc", ":3",
	)
	c.Env = append(os.Environ(),
		fmt.Sprintf("DYLD_LIBRARY_PATH=%s", filepath.Join(base, "sysroot-macos-arm64/lib")),
	)
	return c
}
