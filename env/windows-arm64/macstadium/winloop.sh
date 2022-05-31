#!/bin/bash
# Copyright 2022 Go Authors All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.


while true; do
  DYLD_LIBRARY_PATH="$HOME/macmini-windows/sysroot-macos-arm64/lib" "$HOME/macmini-windows/sysroot-macos-arm64/bin/qemu-system-aarch64" \
    -L ./UTM.app/Contents/Resources/qemu \
    -device ramfb \
    -cpu max \
    -smp cpus=8,sockets=1,cores=8,threads=1 \
    -machine virt,highmem=off \
    -accel hvf \
    -accel tcg,tb-size=1536 \
    -boot menu=on \
    -m 8192 \
    -name "Virtual Machine" \
    -device qemu-xhci,id=usb-bus \
    -device usb-tablet,bus=usb-bus.0 \
    -device usb-mouse,bus=usb-bus.0 \
    -device usb-kbd,bus=usb-bus.0 \
    -bios "$HOME/macmini-windows/Images/QEMU_EFI.fd" \
    -device nvme,drive=drive0,serial=drive0,bootindex=0 \
    -drive "if=none,media=disk,id=drive0,file=$HOME/macmini-windows/Images/win10.qcow2,cache=writethrough" \
    -device usb-storage,drive=drive2,removable=true,bootindex=1 \
    -drive "if=none,media=cdrom,id=drive2,file=$HOME/macmini-windows/Images/virtio.iso,cache=writethrough" \
    -device virtio-net-pci,netdev=net0 \
    -netdev user,id=net0 \
    -uuid 41E1CBA2-8837-4224-801B-277336D58A3D \
    -snapshot \
    -vnc :3
  sleep 5
done
