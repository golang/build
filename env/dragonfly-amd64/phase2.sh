#!/bin/sh
# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.


# Phase 2 of the DragonflyBSD installation: update pkg database.

set -ex

echo >&2 phase2.sh starting

# Make pfi look for CD again when booting for phase3.
# Normally /etc/pfi.conf is left behind and stops future checks.
# Edit /etc/rc.d/pfi to remove /etc/pfi.conf each time it starts running.
echo '/REQUIRE/a
rm -f /etc/pfi.conf
.
w
q' | ed /etc/rc.d/pfi

# pfi startup does not have full path that a root login does.
export PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/games:/usr/local/sbin:/usr/local/bin:/usr/pkg/sbin:/usr/pkg/bin:/root/bin

# Update pkg database and install extras we need.
pkg install -y bash curl git gdb

echo 'DONE WITH PHASE 2.'
sync
poweroff
sleep 86400
