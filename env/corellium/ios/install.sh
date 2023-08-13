#!/bin/sh
# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.


# Setting up a Corellium iPhone device
#
# install.sh sets up a newly created Corellium iPhone device. Before executing
# install.sh, one must prepare the following steps:
#
#   1. A public key must be added to the device's /root/.ssh/authorized_keys file.
#   2. A builder key file `buildkey` in the same folder as `install.sh`.
#   3. Use `bootstrap.bash` from the Go standard distribution and build a
#      ios/arm64 bootstrap toolchain with cgo enabled and the compiler set
#      to the clang wrapper from $GOROOT/misc/ios:
#
#      	GOOS=ios GOARCH=arm64 CGO_ENABLED=1 CC_FOR_TARGET=$(pwd)/../misc/ios/clangwrap.sh ./bootstrap.bash
#
#   4. Put `go-ios-arm64-bootstrap.tbz` in the same folder as `install.sh`.
#   5. Finally, put an iPhone SDK in `iPhoneOS.sdk` in the same folder as `install.sh`.
#      It can be found from Xcode.app/Contents/Developer/Platforms/iPhoneOS.platform/Developer/SDKs/iPhoneOS.sdk.

# Set HOST to root@10.11.1.1 where 10.11.1.1 is the device ssh address in Corellium.
HOST=root@10.11.1.1

ios() {
	ssh "$HOST" "$@"
}

# Install necessary toolchains.
ios apt-get update
ios apt-get upgrade
ios apt install -y --allow-unauthenticated git tmux rsync org.coolstar.iostoolchain ld64 com.linusyang.localeutf8

# Codesign necessary binaries.
ios ldid -S /usr/bin/git
ios ldid -S /usr/bin/tmux
ios ldid -S /usr/bin/rsync

# Upload Go bootstrap toolchain and related files.
scp go-ios-arm64-bootstrap.tbz $HOST:/var/root
ios tar xjf go-ios-arm64-bootstrap.tbz
scp buildkey $HOST:/var/root/.gobuildkey-host-ios-arm64-corellium-ios
scp files/profile $HOST:/var/root/.profile
rsync -va iPhoneOS.sdk $HOST:/var/root/

# Build wrappers on the host, and sign them.
CGO_ENABLED=1 GOOS=ios CC=$(go env GOROOT)/misc/ios/clangwrap.sh GOARCH=arm64 go build -o clangwrap -ldflags="-X main.sdkpath=/var/root/iPhoneOS.sdk" files/clangwrap.go
CGO_ENABLED=1 GOOS=ios CC=$(go env GOROOT)/misc/ios/clangwrap.sh GOARCH=arm64 go build -o arwrap files/arwrap.go
ios mkdir -p /var/root/bin
scp arwrap $HOST:/var/root/bin/ar
scp clangwrap $HOST:/var/root/bin/clangwrap

# Codesign everything.
ios find go-ios-arm64-bootstrap -executable -type f | ios xargs -n1 ldid -S
ios ldid -S /var/root/bin/clangwrap
ios ldid -S /var/root/bin/ar

# Run builder at boot.
scp files/org.golang.builder.plist $HOST:/Library/LaunchDaemons/
ios launchctl load -w /Library/LaunchDaemons/org.golang.builder.plist
scp files/builder.sh $HOST:/var/root

# Install buildlet.
ios HOME=/var/root \
	CC=$HOME/bin/clangwrap \
	GO_BUILDER_ENV=host-ios-arm64-corellium-ios \
	GOROOT_BOOTSTRAP=$HOME/go-ios-arm64-bootstrap \
	PATH=$HOME/bin:$PATH $GOROOT_BOOTSTRAP/bin/go install -v golang.org/x/build/cmd/buildlet@latest

ios reboot
