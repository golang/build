# Copyright 2016 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Linux builder VM running Debian stretch (i.e. Debian testing)
# Docker tag gobuilders/linux-s390x-stretch

FROM debian:stretch
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive

# curl: for getting and unpacking Go 1.4 source
# git-core: for interacting with the Go source & subrepos
# gcc, libc-dev: for building Go's bootstrap 'dist' prog
# gcc-s390x-linux-gnu, libc6-dev-s390x-cross: for s390x builds
# systemd: for init when image is used with docker2boot
# net-tools, ifupdown, isc-dhcp-client: required for networking in a VM on GCE
# procps, lsof, psmisc: misc tools
RUN apt-get update && apt-get install -y \
	bzip2 \
	ca-certificates \
	curl \
	git-core \
	gcc \
	libc6-dev \
	gcc-s390x-linux-gnu \
	libc6-dev-s390x-cross \
	systemd \
	net-tools \
	ifupdown \
	isc-dhcp-client \
	procps \
	lsof \
	psmisc \
	--no-install-recommends \
	&& rm -rf /var/lib/apt/lists/*

RUN mkdir -p /go1.4-amd64 \
	&& ( \
		curl --silent https://storage.googleapis.com/golang/go1.4.3.linux-amd64.tar.gz | tar -C /go1.4-amd64 -zxv \
	) \
	&& mv /go1.4-amd64/go /go1.4 \
	&& rm -rf /go1.4-amd64 \
	&& rm -rf /go1.4/pkg/linux_amd64_race \
		/go1.4/api \
		/go1.4/blog \
		/go1.4/doc \
		/go1.4/misc \
		/go1.4/test \
	&& find /go1.4 -type d -name testdata | xargs rm -rf

RUN curl -o /usr/local/bin/stage0 https://storage.googleapis.com/go-builder-data/stage0.linux-amd64.92ebb4ec \
	&& chmod +x /usr/local/bin/stage0

COPY buildlet.service /etc/systemd/system/buildlet.service
RUN systemctl enable /etc/systemd/system/buildlet.service

RUN rm -f /lib/systemd/system/multi-user.target.wants/systemd-logind.service
RUN (echo "[Journal]"; echo "ForwardToConsole=yes") > /etc/systemd/journald.conf

RUN (echo "auto lo"; echo "iface lo inet loopback"; echo "auto eth0"; echo "iface eth0 inet dhcp") > /etc/network/interfaces

ENV GOROOT_BOOTSTRAP=/go1.4 GOOS=linux GOARCH=s390x CC_FOR_TARGET=s390x-linux-gnu-gcc
COPY build-release.sh /usr/bin/
