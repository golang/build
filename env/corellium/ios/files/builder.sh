#!/bin/sh
# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.


export HOME=/var/root
export CC=$HOME/bin/clangwrap
export GO_BUILDER_ENV=host-ios-arm64-corellium-ios
export GOROOT_BOOTSTRAP=$HOME/go-ios-arm64-bootstrap
export PATH=$HOME/bin:$PATH
while true; do
	$GOROOT_BOOTSTRAP/bin/go install golang.org/x/build/cmd/buildlet@latest
	$HOME/go/bin/buildlet -reverse-type host-ios-arm64-corellium-ios -coordinator farmer.golang.org
	sleep 1
done
