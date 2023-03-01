#!/bin/bash
# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Start macOS VM from installed disk image. Changes written back to disk image.

if [[ $# != 2 ]]; then
  echo "Usage: $0 <disk-image.qcow2> <OSK value>"
  exit 1
fi

DISK=$1
OSK=$2
PORT=1

args=(
  "$DISK"
  "$OSK"
  "$PORT"
)

$HOME/qemu.sh "${args[@]}"
