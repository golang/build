#!/bin/sh
# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.


# Phase 1 of the DragonflyBSD installation: boot from installer CD and install on empty disk.

set -ex

echo >&2 phase1.sh starting

mkdir -p /root/gopherinstall
cp -a /mnt/* /root/gopherinstall
cd /root/gopherinstall

echo >&2 install.sh running

# Following Manual Installation section of https://www.dragonflybsd.org/docs/handbook/Installation/#index3h1

fdisk -IB /dev/da0
disklabel -r -w -B /dev/da0s1 auto
disklabel da0s1 >label
echo '
a: 1G 0 4.2BSD
d: * * HAMMER2
' >>label
disklabel -R -r da0s1 label

newfs /dev/da0s1a
newfs_hammer2 -L ROOT /dev/da0s1d
mount /dev/da0s1d /mnt
mkdir /mnt/boot /mnt/mnt
mount /dev/da0s1a /mnt/boot

# mount clean file system image from second copy of CD on /mnt/mnt for copying.
# the install instructions use cpdup / /mnt,
# but using this second copy of the CD avoids all the tmpfs
# that are mounted on top of /, as well as our local modifications (like the gopherinstall user).
mount_cd9660 /dev/cd2 /mnt/mnt
cpdup /mnt/mnt/boot /mnt/boot
cpdup /mnt/mnt /mnt

cat >/mnt/etc/rc.conf <<'EOF'
ifconfig_vtnet0='DHCP mtu 1460'
EOF

cat >/mnt/boot/loader.conf <<'EOF'
vfs.root.mountfrom=hammer2:da0s1d
console=comconsole
EOF

cat >/mnt/etc/fstab <<'EOF'
da0s1a    /boot           ufs      rw              1 1
da0s1d    /               hammer2  rw              1 1
EOF

umount /mnt/mnt
umount /mnt/boot

echo 'DONE WITH PHASE 1.'
sync
poweroff
sleep 86400
