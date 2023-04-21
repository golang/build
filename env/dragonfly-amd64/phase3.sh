#!/usr/local/bin/bash
# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.


# Phase 3 of the DragonflyBSD installation: apply buildlet customizations.

set -ex
set -o pipefail

echo >&2 phase3.sh starting
rm -f /etc/pfi.conf

# pfi startup does not have full path that a root login does.
export PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/games:/usr/local/sbin:/usr/local/bin:/usr/pkg/sbin:/usr/pkg/bin:/root/bin

# Add a gopher user.
pw useradd gopher -g 0 -c 'Gopher Gopherson' -s /bin/sh

# Disable keyboard console that won't exist on Google Cloud,
# to silence errors about trying to run getty on them.
# Serial console will remain enabled.
perl -pi -e 's/^ttyv/# ttyv/' /etc/ttys

# Set up buildlet service.
cp /mnt/buildlet /etc/rc.d/buildlet
chmod +x /etc/rc.d/buildlet

# Update rc.conf to run buildlet.
cat >>/etc/rc.conf <<'EOF'
hostname="buildlet"
rc_info="YES"
rc_startmsgs="YES"
sshd_enable="YES"
buildlet_enable="YES"
buildlet_env="PATH=/bin:/sbin:/usr/bin:/usr/local/bin"
EOF

# Update loader.conf to disable checksum offload.
cat >>/boot/loader.conf <<'EOF'
hw.vtnet.csum_disable="1"
EOF

# Generate ssh keys if needed.
service sshd keygen

echo 'DONE WITH PHASE 3.'
sync
poweroff
sleep 86400
