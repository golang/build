#!/bin/sh

export HOME=/var/root
export CC=$HOME/bin/clangwrap
export GO_BUILDER_ENV=host-darwin-arm64-corellium-ios
export GOROOT_BOOTSTRAP=$HOME/go-darwin-arm64-bootstrap
export PATH=$HOME/bin:$PATH
while true; do
	$GOROOT_BOOTSTRAP/bin/go get golang.org/x/build/cmd/buildlet
	$HOME/go/bin/buildlet -reverse-type host-darwin-arm64-corellium-ios -coordinator farmer.golang.org
	sleep 1
done
