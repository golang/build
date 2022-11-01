# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM arm64v8/debian:bullseye
LABEL org.opencontainers.image.authors="golang-dev@googlegroups.com"

ENV DEBIAN_FRONTEND noninteractive

# bzr: Bazaar VCS supported by cmd/go
# fonts-droid-fallback: required by x/mobile repo
# fossil: Fossil VCS supported by cmd/go
# gcc: for building Go's bootstrap 'dist' prog
# gdb: optionally used by runtime tests for gdb
# gfortran: for compiling cgo with fortran support (multilib for 386)
# git: git VCS supported by cmd/go
# gpg: the presence of a gpg binary can influence git behavior for cmd/go tests
# libc6-dev: for building Go's bootstrap 'dist' prog
# libgles2-mesa-dev: required by x/mobile repo
# libopenal-dev: required by x/mobile repo
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
	gdb \
	gfortran \
	git \
	gpg \
	iptables \
	libc6-dev \
	libgles2-mesa-dev \
	libopenal-dev \
	lsof \
	make \
	mercurial \
	netbase \
	openssh-server \
	procps \
	psmisc \
	strace \
	subversion \
	swig \
	&& rm -rf /var/lib/apt/lists/*

RUN mkdir /usr/local/go-bootstrap && \
	curl --silent https://storage.googleapis.com/go-builder-data/gobootstrap-linux-arm64.tar.gz | \
	tar -C /usr/local/go-bootstrap -zx

RUN curl -o  /usr/local/bin/stage0 https://storage.googleapis.com/go-builder-data/buildlet-stage0.linux-arm64 && \
	chmod +x /usr/local/bin/stage0

CMD ["/usr/local/bin/stage0"]
