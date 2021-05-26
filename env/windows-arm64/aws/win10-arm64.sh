#!/bin/bash

# Copyright 2021 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

/home/ubuntu/qemu-6.0.0/build/aarch64-softmmu/qemu-system-aarch64 \
  -name "Windows 10 ARM64" \
  -machine virt \
  -cpu host \
  --accel kvm \
  -smp 4 \
  -m 24G \
  -drive file=/home/ubuntu/win10/QEMU_EFI.fd,format=raw,if=pflash,readonly=on \
  -drive file=/home/ubuntu/win10/QEMU_VARS.fd,format=raw,if=pflash \
  -device nec-usb-xhci \
  -device usb-kbd,id=kbd0 \
  -device usb-mouse,id=tab0 \
  -device virtio-net,disable-legacy=on,netdev=net0,mac=54:91:05:C5:73:29,addr=08 \
  -netdev 'user,id=net0,hostfwd=tcp::443-:443,guestfwd=tcp:10.0.2.100:8173-cmd:netcat 169.254.169.254 80' \
  -device nvme,drive=hdd0,serial=hdd0 \
  -vnc :3 \
  -drive file=/home/ubuntu/win10/win10.qcow2,if=none,id=hdd0,cache=writethrough \
  -drive file=/home/ubuntu/win10/virtio.iso,media=cdrom,if=none,id=drivers,readonly=on \
  -device usb-storage,drive=drivers \
  -chardev file,path=/var/log/qemu-serial.log,id=char0 \
  -serial chardev:char0 \
  -device ramfb
