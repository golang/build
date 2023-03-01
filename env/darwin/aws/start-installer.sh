#!/bin/bash
# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Start macOS installer to install to disk image.

if [[ $# != 4 ]]; then
  echo "Usage: $0 <disk-image.qcow2> <opencore.img> <macos-recovery.dmg> <OSK value>"
  exit 1
fi

DISK=$1
OPENCORE=$2
RECOVERY=$3
OSK=$4
PORT=1

args=(
  "$DISK"
  "$OSK"
  "$PORT"
  -drive id=OpenCoreBoot,if=none,format=raw,file="$OPENCORE"
  -device ide-hd,bus=sata.3,drive=OpenCoreBoot
  -drive id=InstallMedia,if=none,format=raw,file="$RECOVERY"
  -device ide-hd,bus=sata.4,drive=InstallMedia
)

$HOME/qemu.sh "${args[@]}"
