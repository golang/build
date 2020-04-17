#!/usr/bin/env bash

# Copyright 2020 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

#
# Installs all dependencies for a Debian 10 linux arm64 Host.
#

set -euxo pipefail

TMP_DIR="$(mktemp -d)"
GO_PATH="$TMP_DIR/gopath"

sudo apt-get update && sudo apt-get upgrade -y

sudo apt-get install -y \
	 apt-transport-https \
	 ca-certificates \
	 curl \
	 gnupg-agent \
	 gnupg2 \
	 jq \
	 software-properties-common

curl -fsSL https://download.docker.com/linux/debian/gpg | sudo apt-key add -

sudo add-apt-repository "deb [arch=arm64] https://download.docker.com/linux/debian $(lsb_release -cs) stable"

sudo apt-get update

sudo apt-get install -y \
	 docker-ce \
	 docker-ce-cli \
	 containerd.io

# retrieve the latest version of Go
GO_VERSION="$(curl -s https://golang.org/dl/?mode=json | jq --raw-output '.[0].version')"
GO_PACKAGE="$GO_VERSION.linux-arm64.tar.gz"
GO_SHA="$(curl -s https://golang.org/dl/?mode=json | jq --raw-output '.[0].files | map(select(.arch == "arm64")) | .[0].sha256')"

# download Go package
curl -o $TMP_DIR/$GO_PACKAGE" -L "https://golang.org/dl/$GO_PACKAGE"

# verify sha256 shasum"
echo "$GO_SHA $TMP_DIR/$GO_PACKAGE" | sha256sum --check --status

# unzip Go package
tar -xvf "$TMP_DIR/$GO_PACKAGE" -C "$TMP_DIR"

# build rundockerbuildlet
mkdir -p "$GO_PATH"
GOPATH="$GO_PATH" "$TMP_DIR/go/bin/go" get -u golang.org/x/build/cmd/rundockerbuildlet
GOPATH="$GO_PATH" "$TMP_DIR/go/bin/go" build -o "$TMP_DIR/rundockerbuildlet" golang.org/x/build/cmd/rundockerbuildlet
sudo mv "$TMP_DIR/rundockerbuildlet" /usr/local/bin/rundockerbuildlet

sudo mv /tmp/rundockerbuildlet.service /etc/systemd/user/rundockerbuildlet.service
sudo systemctl enable /etc/systemd/user/rundockerbuildlet.service
sudo systemctl start rundockerbuildlet

# remove temporary directory
rm -fr "$TMP_DIR"
