#!/usr/bin/env bash

# Copyright 2021 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

#
# Installs all dependencies for an Ubuntu Linux ARM64 qemu host.
#

set -euxo pipefail

TMP_DIR="$(mktemp -d)"

# Retry apt commands until we succeed.
#
# Metal instances are still being provisioned when we are first able
# to connect over SSH. Some parts of apt are locked or not yet
# present. Retrying a few times seems to do the trick.
for i in $(seq 1 10); do
  # Give it a chance to finish before our first try,
  # and take a break between loops.
  sleep 1
  sudo apt-add-repository universe || continue

  sudo apt-get update || continue
  sudo apt-get upgrade -y || continue

  sudo apt-get install -y apt-transport-https ca-certificates curl gnupg-agent gnupg jq software-properties-common \
    build-essential ninja-build && break
done

# QEMU Dependencies
sudo apt-get install -y git libglib2.0-dev libfdt-dev libpixman-1-dev zlib1g-dev

# QEMU Extras
sudo apt-get install -y git-email libaio-dev libbluetooth-dev libbrlapi-dev libbz2-dev libcap-dev libcap-ng-dev \
  libcurl4-gnutls-dev libgtk-3-dev libibverbs-dev libjpeg8-dev libncurses5-dev libnuma-dev librbd-dev librdmacm-dev \
  libsasl2-dev libsdl1.2-dev libseccomp-dev libsnappy-dev libssh2-1-dev libvde-dev libvdeplug-dev libvte-2.91-dev \
  libxen-dev liblzo2-dev valgrind xfslibs-dev libnfs-dev libiscsi-dev

# QEMU download & build
wget https://download.qemu.org/qemu-6.0.0.tar.xz
tar xJf qemu-6.0.0.tar.xz
cd qemu-6.0.0
./configure --target-list=arm-softmmu,aarch64-softmmu
make -j16
cd "$HOME"

# S3 CLI
sudo apt-get install -y aws-shell

# Copy pre-prepared Windows 10 image
aws s3 sync s3://go-builder-data/win10 "$HOME"/win10

chmod u+x "$HOME/win10-arm64.sh"
sudo cp /tmp/qemu.service /etc/systemd/user/qemu.service
sudo systemctl enable /etc/systemd/user/qemu.service
sudo systemctl start qemu
