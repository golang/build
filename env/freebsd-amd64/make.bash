#!/bin/bash
# Copyright 2015 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Builds FreeBSD image based on raw disk images provided by FreeBSD.org
# This script boots the image once, side-loads GCE Go builder configuration via
# an ISO mounted as the CD-ROM, and customizes the system before powering down.
# SSH is enabled, and a user gopher, password gopher, is created.

# Only tested on Ubuntu 16.04.
# Requires packages: qemu expect mkisofs

set -e

function download_image() {
  local release_url=ftp://ftp.freebsd.org/pub/FreeBSD/releases/VM-IMAGES/${VERSION:?}-RELEASE/amd64/Latest
  local img_filename=FreeBSD-${VERSION:?}-RELEASE-amd64${VERSION_TRAILER}.raw.xz
  curl -O ${release_url}/${img_filename}
  echo "${SHA256}  ${img_filename}" | sha256sum -c -
  xz -d FreeBSD-${VERSION:?}-RELEASE-amd64${VERSION_TRAILER}.raw.xz
}

case $1 in
9.3)
  readonly VERSION=9.3
  readonly VERSION_TRAILER="-20140711-r268512"
  readonly DNS_LOOKUP=dig
  readonly SHA256=4737218995ae056207c68f3105c0fbe655c32e8b76d2160ebfb1bba56dd5196f
;;

10.3)
  readonly VERSION=10.3
  readonly VERSION_TRAILER=
  # BIND replaced by unbound on FreeBSD 10, so drill(1) is the new dig(1)
  readonly DNS_LOOKUP=drill
  readonly SHA256=1d710ba643bf6a8ce5bff5a9d69b1657ccff83dd1f2df711d9b4e02f9aab7d06
;;
11.0)
  readonly VERSION=11.0
  readonly VERSION_TRAILER=
  readonly DNS_LOOKUP=drill
  readonly SHA256=f9f7fcac1acfe210979a72e0642a70fcf9c9381cc1884e966eac8381c724158c
;;
*)
  echo "Usage: $0 <version>"
  echo " version - FreeBSD version to build. Valid choices: 9.3 10.3 11.0"
  exit 1
esac

readonly IMAGE=freebsd-amd64-${VERSION/\./}.tar.gz

if [ $(tput cols) -lt 80 ]; then
	echo "Running qemu with curses display requires a window 80 columns or larger or expect(1) won't work correctly."
	exit 1
fi

if ! [ -e FreeBSD-${VERSION:?}-RELEASE-amd64.raw ]; then
  download_image
fi

cp FreeBSD-${VERSION:?}-RELEASE-amd64${VERSION_TRAILER}.raw disk.raw

mkdir -p iso/etc iso/usr/local/etc/rc.d

cat >iso/etc/rc.conf <<EOF
hostname="buildlet"
ifconfig_vtnet0="SYNCDHCP mtu 1460"
sshd_enable="YES"
buildlet_enable="YES"
EOF

cat >iso/usr/local/etc/rc.d/buildlet <<EOF
#!/bin/sh

# PROVIDE: buildlet
# REQUIRE: sshd
# BEFORE: securelevel

. /etc/rc.subr

name="buildlet"
start_cmd="\${name}_start"
stop_cmd=""

buildlet_start()
{
	PATH=/bin:/usr/bin:/usr/local/bin; export PATH
	echo "starting buildlet script"
	netstat -rn
	cat /etc/resolv.conf
	${DNS_LOOKUP:?} metadata.google.internal
	(
	 set -e
	 export PATH="\$PATH:/usr/local/bin"
	 /usr/local/bin/curl -o /buildlet \$(/usr/local/bin/curl -H "Metadata-Flavor: Google" http://metadata.google.internal/computeMetadata/v1/instance/attributes/buildlet-binary-url)
	 chmod +x /buildlet
	 exec /buildlet
	 echo "giving up"
	 sleep 10
	)
	poweroff
}
load_rc_config \$name
run_rc_command "\$1"
EOF

cat >iso/install.sh <<EOF
set -x

mkdir -p /usr/local/etc/rc.d/
cp /mnt/usr/local/etc/rc.d/buildlet /usr/local/etc/rc.d/buildlet
chmod +x /usr/local/etc/rc.d/buildlet
cp /mnt/etc/rc.conf /etc/rc.conf
adduser -f - <<ADDUSEREOF
gopher::::::Gopher Gopherson::/bin/sh:gopher
ADDUSEREOF
pw user mod gopher -G wheel

# Enable serial console early in boot process.
echo '-h' > /boot.conf
echo 'console="comconsole"' >> /boot/loader.conf
EOF

mkisofs -r -o config.iso iso/
# TODO(wathiede): remove sleep
sleep 2

# TODO(wathiede): set serial output so we can track boot on GCE.
expect <<EOF
set timeout 600
spawn qemu-system-x86_64 -display curses -smp 2 -drive if=virtio,file=disk.raw,cache=none -cdrom config.iso -net nic,model=virtio -net user

# Speed-up boot by going in to single user mode.
expect "Welcome to FreeBSD"
sleep 2
send "\n"

expect "login:"
sleep 1
send "root\n"

expect "root@:~ # "
sleep 1
send "dhclient vtnet0\n"

expect "root@:~ # "
sleep 1
send "mount_cd9660 /dev/cd0 /mnt\nsh /mnt/install.sh\n"

expect "root@:~ # "
sleep 1
send "pkg install bash curl git\n"

expect "Do you want to fetch and install it now"
sleep 1
send "y\n"

expect "Proceed with this action"
sleep 1
send "y\n"

expect "root@:~ # "
sleep 1
send "poweroff\n"
expect "All buffers synced."
sleep 5
EOF

# Create Compute Engine disk image.
echo "Archiving disk.raw as ${IMAGE:?}... (this may take a while)"
tar -Szcf ${IMAGE:?} disk.raw

echo "Done. GCE image is ${IMAGE:?}"
