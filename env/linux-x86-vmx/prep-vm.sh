#!/bin/false
# Copyright 2019 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# This runs on the Debian Stretch template VM to turn it into the
# buildlet image we want. This isn't for running on the developer's
# host machine.

set -e
set -x

apt-get update
apt-get install --yes apt-transport-https ca-certificates curl gnupg2 software-properties-common
curl -fsSL https://download.docker.com/linux/debian/gpg | apt-key add -
add-apt-repository "deb [arch=amd64] https://download.docker.com/linux/debian $(lsb_release -cs) stable"
apt-get update
apt-get install --yes docker-ce docker-ce-cli containerd.io

git clone https://github.com/GoogleCloudPlatform/konlet.git
mkdir -p /usr/share/google
install konlet/scripts/get_metadata_value /usr/share/google
mkdir -p /usr/share/gce-containers
install konlet/scripts/konlet-startup /usr/share/gce-containers/konlet-startup
install konlet/scripts/konlet-startup.service /etc/systemd/system
chmod -x /etc/systemd/system/konlet-startup.service
systemctl enable /etc/systemd/system/konlet-startup.service
systemctl start konlet-startup

# Pre-pull some common images/layers to speed up future boots:
gcloud auth configure-docker --quiet
docker pull gcr.io/symbolic-datum-552/linux-x86-stretch:latest
docker pull gcr.io/gce-containers/konlet:v.0.9-latest

apt-get dist-upgrade --yes
