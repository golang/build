#!/usr/bin/env bash
# Copyright 2014 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -e
set -u

# Update to the version listed on https://openbsd.org
readonly VERSION="${VERSION:-7.6}"
readonly RELNO="${VERSION/./}"
readonly SNAPSHOT=false

readonly ARCH="${ARCH:-amd64}"
readonly MIRROR="${MIRROR:-cdn.openbsd.org}"

readonly WORK="$(mktemp -d)"
readonly SITE="${WORK}/site"

if [[ "${ARCH}" != "amd64" && "${ARCH}" != "i386" ]]; then
  echo "ARCH must be amd64 or i386"
  exit 1
fi

readonly ISO="install${RELNO}-${ARCH}.iso"
readonly ISO_PATCHED="install${RELNO}-${ARCH}-patched.iso"

if [[ ! -f "${ISO}" ]]; then
  DIR="${VERSION}"
  if [[ "$SNAPSHOT" = true ]]; then
    DIR="snapshots"
  fi
  curl -o "${ISO}" "https://${MIRROR}/pub/OpenBSD/${DIR}/${ARCH}/install${RELNO}.iso"
fi

function cleanup() {
	rm -rf "${WORK}"
}

trap cleanup EXIT INT

# Create custom siteXX.tgz set.
PKG_ADD_OPTIONS="-I"
if [[ "$SNAPSHOT" = true ]]; then
  PKG_ADD_OPTIONS="-I -D snap"
fi
mkdir -p ${SITE}/etc
cat >${SITE}/install.site <<EOF
#!/bin/sh
echo 'set tty com0' > boot.conf
EOF

cat >${SITE}/etc/installurl <<EOF
https://${MIRROR}/pub/OpenBSD
EOF
cat >${SITE}/etc/rc.firsttime <<EOF
set -x
cat > /etc/login.conf.d/moreres <<'EOLOGIN'
moreres:\
  :datasize-max=infinity: \
  :datasize-cur=infinity: \
  :vmemoryuse-max=infinity: \
  :vmemoryuse-cur=infinity: \
  :memoryuse-max=infinity: \
  :memoryuse-cur=infinity: \
  :maxproc-max=2048: \
  :maxproc-cur=2048: \
  :openfiles-max=4096: \
  :openfiles-cur=4096: \
  :tc=default:
EOLOGIN
usermod -L moreres swarming
syspatch
# Run syspatch twice in case syspatch itself needs patching (this has been needed previously).
syspatch
pkg_add -iv ${PKG_ADD_OPTIONS} bash curl git python%3 sudo--gettext
chown root:wheel /etc/sudoers
halt -p
EOF

cat >${SITE}/etc/rc.local <<EOF
(
  set -x

  echo "Remounting root with softdep,noatime..."
  mount -o softdep,noatime,update /

  echo "starting buildlet script"
  netstat -rn
  cat /etc/resolv.conf
  dig metadata.google.internal
  (
    set -e
    export PATH="\$PATH:/usr/local/bin"
    project=\$(curl -s -H "Metadata-Flavor: Google" http://metadata.google.internal/computeMetadata/v1/project/project-id)
    case "\$project" in
      *luci*)
        gcehost=\$(curl -s -H "Metadata-Flavor: Google" http://metadata.google.internal/computeMetadata/v1/instance/hostname | cut -d . -f 1)
        swarming=\$(curl -s -H "Metadata-Flavor: Google" http://metadata.google.internal/computeMetadata/v1/instance/attributes/swarming | cut -d . -f 1)
        su -l swarming -c "/usr/local/bin/bootstrapswarm --hostname \$gcehost --swarming \${swarming}.appspot.com"
      ;;
      *)
        /usr/local/bin/curl -o /buildlet \$(/usr/local/bin/curl --fail -H "Metadata-Flavor: Google" http://metadata.google.internal/computeMetadata/v1/instance/attributes/buildlet-binary-url)
        chmod +x /buildlet
        exec /buildlet
      ;;
    esac
  )
  echo "giving up"
  (
    sleep 60
    halt -p
  )&
)
EOF
cat >${SITE}/etc/sysctl.conf <<EOF
hw.smt=1
kern.timecounter.hardware=tsc
EOF
cat >${SITE}/etc/sudoers <<EOF
root ALL=(ALL:ALL) ALL
swarming ALL=NOPASSWD:/sbin/shutdown -r now
EOF
chmod +x ${SITE}/install.site
mkdir -p ${SITE}/usr/local/bin
CGO_ENABLED=0 GOOS=openbsd GOARCH=${ARCH/i386/386} go build -o ${SITE}/usr/local/bin/bootstrapswarm golang.org/x/build/cmd/bootstrapswarm
tar --mode a=rx,u=rwx --owner root:0 --group wheel:0 -C ${SITE} -zcf ${WORK}/site${RELNO}.tgz .

# Autoinstall script.
cat >${WORK}/auto_install.conf <<EOF
System hostname = openbsd-amd64
Which network interface = vio0
IPv4 address for vio0 = dhcp
IPv6 address for vio0 = none
Password for root account = root
Do you expect to run the X Window System = no
Change the default console to com0 = yes
Which speed should com0 use = 115200
Setup a user = swarming
Full name for user swarming = Swarming Gopher Gopherson
Password for user swarming = swarming
Allow root ssh login = no
What timezone = US/Pacific
Which disk = sd0
Use (W)hole disk or (E)dit the MBR = whole
Use (A)uto layout, (E)dit auto layout, or create (C)ustom layout = auto
URL to autopartitioning template for disklabel = file://disklabel.template
Location of sets = cd0
Set name(s) = +* -x* -game* -man* done
Directory does not contain SHA256.sig. Continue without verification = yes
EOF

# Disklabel template.
cat >${WORK}/disklabel.template <<EOF
/	5G-*	95%
swap	1G
EOF

# Hack install CD a bit.
echo 'set tty com0' > ${WORK}/boot.conf
dd if=/dev/urandom of=${WORK}/random.seed bs=4096 count=1
cp "${ISO}" "${ISO_PATCHED}"
growisofs -M "${ISO_PATCHED}" -l -R -graft-points \
  /${VERSION}/${ARCH}/site${RELNO}.tgz=${WORK}/site${RELNO}.tgz \
  /auto_install.conf=${WORK}/auto_install.conf \
  /disklabel.template=${WORK}/disklabel.template \
  /etc/boot.conf=${WORK}/boot.conf \
  /etc/random.seed=${WORK}/random.seed

# Initialize disk image.
rm -f ${WORK}/disk.raw
qemu-img create -f raw ${WORK}/disk.raw 30G

# Run the installer to create the disk image.
expect <<EOF
set timeout 1800

spawn qemu-system-x86_64 -nographic -smp 2 \
  -drive if=virtio,file=${WORK}/disk.raw,format=raw -cdrom "${ISO_PATCHED}" \
  -net nic,model=virtio -net user -boot once=d

expect timeout { exit 1 } "boot>"
send "\n"

# Need to wait for the kernel to boot.
expect timeout { exit 1 } "\(I\)nstall, \(U\)pgrade, \(A\)utoinstall or \(S\)hell\?"
send "s\n"

expect timeout { exit 1 } "# "
send "mount /dev/cd0c /mnt\n"
send "cp /mnt/auto_install.conf /mnt/disklabel.template /\n"
send "chmod a+r /disklabel.template\n"
send "umount /mnt\n"
send "exit\n"

expect timeout { exit 1 } "CONGRATULATIONS!"
expect timeout { exit 1 } eof
EOF

# Create Compute Engine disk image.
echo "Archiving disk.raw... (this may take a while)"
tar -C ${WORK} -Szcf "openbsd-${VERSION}-${ARCH}-gce.tar.gz" disk.raw

echo "Done. GCE image is openbsd-${VERSION}-${ARCH}-gce.tar.gz."
