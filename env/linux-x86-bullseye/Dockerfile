# Copyright 2021 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang/buildlet-stage0 AS stage0

FROM debian:bullseye
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive

# fonts-droid-fallback: required by x/mobile repo
# gcc-multilib: for 32-bit builds
# gcc: for building Go's bootstrap 'dist' prog
# gdb: optionally used by runtime tests for gdb
# gfortran: for compiling cgo with fortran support (multilib for 386)
# libc6-dev-i386: for 32-bit builds
# libc6-dev: for building Go's bootstrap 'dist' prog
# libgles2-mesa-dev: required by x/mobile repo
# libopenal-dev: required by x/mobile repo
# lsof: misc basic tool
# netbase: for net package tests, issue 42750
# procps: misc basic tool
# psmisc: misc basic tool
# strace: optionally used by some net/http tests
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	ca-certificates \
	curl \
	fonts-droid-fallback \
	gcc \
	gcc-multilib \
	gdb \
	gfortran \
	gfortran-multilib \
	iptables \
	libc6-dev \
	libc6-dev-i386 \
	libgles2-mesa-dev \
	libopenal-dev \
	lsof \
	netbase \
	openssh-server \
	procps \
	psmisc \
	strace \
	&& rm -rf /var/lib/apt/lists/*

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
