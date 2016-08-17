#!/bin/bash
# Copyright 2016 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
set -e

GIT_REV=$1
if [[ -z "$GIT_REV" ]]; then
	GIT_REV=master
fi

# fetch the source
git clone https://go.googlesource.com/go /usr/src/go
(
cd /usr/src/go
# checkout the specific revision
git checkout "$GIT_REV"

cd /usr/src/go/src
# build
./bootstrap.bash

cd /
mv /usr/src/go-${GOOS}-${GOARCH}-bootstrap /go
mkdir -p /artifacts
# tarball the artifact
tar -czvf /artifacts/go-${GIT_REV}-${GOOS}-${GOARCH}.tar.gz /go
)
