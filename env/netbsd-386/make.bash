#!/bin/bash
# Copyright 2016 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# This script uses Anita (an automated NetBSD installer) for setting up
# the VM. It needs the following things on the build host:
#  - curl
#  - qemu
#  - cdrtools
#  - GNU tar (not BSD tar)
#  - Python 3
#  - python-pexpect

set -e -x

ANITA_VERSION=2.10
ARCH=i386
RELEASE=9.3
# The release that the packages have been built for.
PKG_RELEASE=9.0_2022Q2

# Must use GNU tar. On NetBSD, tar is BSD tar and gtar is GNU.
TAR=tar
if which gtar > /dev/null; then
  TAR=gtar
fi

LATEST_BUILD=`curl -Ls -w '%{url_effective}'  -o /dev/null https://nycdn.netbsd.org/pub/NetBSD-daily/netbsd-9/latest/ | xargs basename`

WORKDIR=work-NetBSD-${ARCH}
VM_IMAGE=vm-image-netbsd-${ARCH}-${LATEST_BUILD}.tar.gz

# Remove WORKDIR unless -k (keep) is given.
if [ "$1" != "-k" ]; then
  rm -rf ${WORKDIR}
fi

# Download and build anita (automated NetBSD installer).
if ! sha1sum -c anita-${ANITA_VERSION}.tar.gz.sha1; then
  curl -vO https://www.gson.org/netbsd/anita/download/anita-${ANITA_VERSION}.tar.gz
  sha1sum -c anita-${ANITA_VERSION}.tar.gz.sha1 || exit 1
fi

tar xfz anita-${ANITA_VERSION}.tar.gz
cd anita-${ANITA_VERSION}
python3 setup.py build
cd ..

env PYTHONPATH=${PWD}/anita-${ANITA_VERSION} python3 mkvm.py ${ARCH} ${RELEASE} ${PKG_RELEASE}

echo "Archiving wd0.img (this may take a while)"
${TAR} -Szcf ${VM_IMAGE} --transform s,${WORKDIR}/wd0.img,disk.raw, ${WORKDIR}/wd0.img
echo "Done. GCE image is ${VM_IMAGE}."
