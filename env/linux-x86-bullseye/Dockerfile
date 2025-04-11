# Copyright 2021 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang/buildlet-stage0 AS stage0

FROM debian:bullseye
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive

# bzr: Bazaar VCS supported by cmd/go
# fonts-droid-fallback: required by x/mobile repo
# fossil: Fossil VCS supported by cmd/go
# gcc-multilib: for 32-bit builds
# gcc: for building Go's bootstrap 'dist' prog
# gdb: optionally used by runtime tests for gdb
# gfortran: for compiling cgo with fortran support (multilib for 386)
# git: git VCS supported by cmd/go
# g++: used for swig tests and building some benchmarks
# g++-multilib: used for 32-bit swig tests
# libc6-dev-i386: for 32-bit builds
# libc6-dev: for building Go's bootstrap 'dist' prog
# libgles2-mesa-dev: required by x/mobile repo
# libopenal-dev: required by x/mobile repo
# lsof: misc basic tool
# make: used for setting up benchmarks in the x/benchmark builders
# mercurial: mercurial VCS supported by cmd/go
# nano: for interactive debugging convenience
# netbase: for net package tests, issue 42750
# patch: used for building some benchmarks
# procps: misc basic tool
# psmisc: misc basic tool
# strace: optionally used by some net/http tests
# subversion: subversion VCS supported by cmd/go
# swig: used for c/c++ interop related tests
# zlib1g: used for ASAN symbolization with clang
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	bzr \
	ca-certificates \
	curl \
	ed \
	fonts-droid-fallback \
	fossil \
	gcc \
	gcc-multilib \
	gdb \
	gfortran \
	gfortran-multilib \
	git \
	g++ \
	g++-multilib \
	iptables \
	iproute2 \
	libc6-dev \
	libc6-dev-i386 \
	libgles2-mesa-dev \
	libopenal-dev \
	lsof \
	make \
	mercurial \
	nano \
	netbase \
	openssh-server \
	patch \
	procps \
	psmisc \
	strace \
	subversion \
	sudo \
	swig \
	zlib1g \
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

COPY --from=stage0 /go/bin/* /usr/local/bin/

CMD ["/usr/local/bin/run-worker.sh"]
