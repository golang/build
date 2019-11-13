#!/bin/sh

# install.sh sets up a newly created Corellium iPhone device.
# Set HOST to root@<ip> where <ip> is the device ssh
# address.
#
# Put a builder key in `buildkey`.
#
# Use `bootstrap.bash` from the Go standard distribution to build a
# darwin/arm64 bootstrap toolchain and put it in
# `go-darwin-arm64-bootstrap.tbz`.
#
# Finally, install.sh assumes an iPhone SDK in `iPhoneOS.sdk`.

ios() {
	ssh "$HOST" "$@"
}

# Replace the builtin packages sources with a list of sources
# that contain aworking toolchain.
scp files/sources.list $HOST:/etc/apt/sources.list.d/sources.list
ios rm /etc/apt/sources.list.d/electra.list
ios apt-get update
ios apt install -y --allow-unauthenticated git tmux rsync org.coolstar.iostoolchain ld64 com.linusyang.localeutf8

# Run builder at boot.
scp files/org.golang.builder.plist $HOST:/Library/LaunchDaemons/
ios launchctl load -w /Library/LaunchDaemons/org.golang.builder.plist
scp files/builder.sh $HOST:/var/root

scp go-darwin-arm64-bootstrap.tbz $HOST:/var/root
ios tar xjf go-darwin-arm64-bootstrap.tbz
scp buildkey $HOST:/var/root/.gobuildkey-host-darwin-arm64-corellium-ios
scp files/profile $HOST:/var/root/.profile
rsync -va iPhoneOS.sdk $HOST:/var/root/

# Dummy sign Go bootstrap toolchain.
ios find go-darwin-arm64-bootstrap -executable -type f| ios xargs -n1 ldid -S

ios mkdir -p /var/root/bin

# Build wrappers on the host.
CGO_ENABLED=1 GOOS=darwin CC=$(go env GOROOT)/misc/ios/clangwrap.sh GOARCH=arm64 go build -o clangwrap -ldflags="-X main.sdkpath=/var/root/iPhoneOS.sdk" files/clangwrap.go
CGO_ENABLED=1 GOOS=darwin CC=$(go env GOROOT)/misc/ios/clangwrap.sh GOARCH=arm64 go build -o arwrap files/arwrap.go
scp arwrap $HOST:/var/root/bin/ar
scp clangwrap $HOST:/var/root/bin/clangwrap
# Dummy sign them.
ios ldid -S /var/root/bin/clangwrap
ios ldid -S /var/root/bin/ar
ios reboot
