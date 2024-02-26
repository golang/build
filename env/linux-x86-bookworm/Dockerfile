# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang/buildlet-stage0 AS stage0

FROM debian:bookworm
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
# less: misc basic tool
# libc6-dev-i386: for 32-bit builds
# libc6-dev: for building Go's bootstrap 'dist' prog
# libgles2-mesa-dev: required by x/mobile repo
# libopenal-dev: required by x/mobile repo
# linux-perf: for performance analysis on perf builders
# lsof: misc basic tool
# make: used for setting up benchmarks in the x/benchmark builders
# mercurial: mercurial VCS supported by cmd/go
# netbase: for net package tests, issue 42750
# procps: misc basic tool
# psmisc: misc basic tool
# strace: optionally used by some net/http tests
# subversion: subversion VCS supported by cmd/go
# swig: used for c/c++ interop related tests
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	bzr \
	ca-certificates \
	curl \
	fonts-droid-fallback \
	fossil \
	gcc \
	gcc-multilib \
	gdb \
	gfortran \
	gfortran-multilib \
	git \
	iptables \
	iproute2 \
	less \
	libc6-dev \
	libc6-dev-i386 \
	libgles2-mesa-dev \
	libopenal-dev \
	linux-perf \
	lsof \
	make \
	mercurial \
	netbase \
	openssh-server \
	procps \
	psmisc \
	strace \
	subversion \
	sudo \
	swig \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=stage0 /go/bin/* /usr/local/bin/

CMD ["/usr/local/bin/run-worker.sh"]
