# Copyright 2014 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Linux builder VM with clang instead of gcc.
# Docker tag gobuilders/linux-x86-clang

FROM golang/buildlet-stage0 AS stage0

FROM debian:buster
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive

# strace: optionally used by some net/http tests
# libc6-dev-i386 gcc-multilib: for 32-bit builds
# procps lsof psmisc: misc basic tools
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	ca-certificates \
	curl \
	clang \
	strace \
	libc6-dev-i386 \
	gcc-multilib \
	procps \
	lsof \
	psmisc \
	openssh-server \
	netbase \
	&& rm -rf /var/lib/apt/lists/* \
	&& rm -f /usr/bin/gcc

RUN mkdir --mode=0700 /run/sshd

RUN mkdir -p /go1.4-amd64 \
	&& ( \
		curl --silent https://storage.googleapis.com/golang/go1.4.linux-amd64.tar.gz | tar -C /go1.4-amd64 -zxv \
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

COPY --from=stage0 /go/bin/stage0 /usr/local/bin/stage0

CMD ["/usr/local/bin/stage0"]
