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

// defaultDarwinDir returns a default path for a darwin VM.
//
// The directory should contain the darwin VM image, and QEMU
// (sysroot-macos-x86_64).
func defaultDarwinDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("os.UserHomeDir() = %q, %v", home, err)
		return ""
	}
	return home
}

// darwinCmd returns a qemu command for running a darwin VM, ready
// to be started.
func darwinCmd(base string) *exec.Cmd {
	if *macosVersion == 0 {
		log.Fatalf("-macos-version required")
	}
	if *osk == "" {
		log.Fatalf("-osk required")
	}

	sysroot := filepath.Join(base, "sysroot-macos-x86_64")
	ovmfCode := filepath.Join(sysroot, "share/qemu/edk2-x86_64-code.fd")

	disk := filepath.Join(base, "macos.qcow2")

	// Note that vmnet-shared requires that we run QEMU as root, so
	// runqemubuildlet must run as root.
	//
	// These arguments should be kept in sync with env/darwin/aws/qemu.sh.
	args := []string{
		// Discard disk changes on exit.
		"-snapshot",
		"-m", "10240",
		"-cpu", "host",
		"-machine", "q35",
		"-usb",
		"-device", "usb-kbd",
		"-device", "usb-tablet",
		// macOS only likes a power-of-two number of cores, but odd socket count is
		// fine.
		"-smp", "cpus=6,sockets=3,cores=2,threads=1",
		"-device", "usb-ehci,id=ehci",
		"-device", "nec-usb-xhci,id=xhci",
		"-global", "nec-usb-xhci.msi=off",
		"-device", fmt.Sprintf("isa-applesmc,osk=%s", *osk),
		"-drive", fmt.Sprintf("if=pflash,format=raw,readonly=on,file=%s", ovmfCode),
		"-smbios", "type=2",
		"-device", "ich9-intel-hda",
		"-device", "hda-duplex",
		"-device", "ich9-ahci,id=sata",
		"-drive", fmt.Sprintf("id=MacHDD,if=none,format=qcow2,file=%s", disk),
		"-device", "ide-hd,bus=sata.2,drive=MacHDD",
		"-monitor", "stdio",
		"-device", "VGA,vgamem_mb=128",
		"-M", "accel=hvf",
		"-display", fmt.Sprintf("vnc=127.0.0.1:%d", *guestIndex),
		// DHCP range is a dummy range. The actual guest IP is assigned statically
		// based on the MAC address matching an entry in /etc/bootptab.
		"-netdev", "vmnet-shared,id=net0,start-address=192.168.64.1,end-address=192.168.64.100,subnet-mask=255.255.255.0",
	}
	if *macosVersion >= 11 {
		args = append(args, "-device", fmt.Sprintf("virtio-net-pci,netdev=net0,id=net0,mac=52:54:00:c9:18:0%d", *guestIndex))
	} else {
		args = append(args, "-device", fmt.Sprintf("vmxnet3,netdev=net0,id=net0,mac=52:54:00:c9:18:0%d", *guestIndex))
	}

	cmd := exec.Command(filepath.Join(sysroot, "bin/qemu-system-x86_64"), args...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("DYLD_LIBRARY_PATH=%s", filepath.Join(sysroot, "lib")),
	)
	return cmd
}
