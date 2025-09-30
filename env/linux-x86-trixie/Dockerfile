# Copyright 2025 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang/buildlet-stage0 AS stage0

# Build perf_to_profile to make go tool pprof work with perf.data files.
FROM debian:trixie AS perftoprofile
LABEL maintainer="golang-dev@googlegroups.com"

ENV DEBIAN_FRONTEND noninteractive

RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	ca-certificates \
	curl \
	g++ \
	git \
	libelf-dev \
	libcap-dev

RUN curl -L "https://github.com/bazelbuild/bazelisk/releases/download/v1.19.0/bazelisk-linux-amd64" > bazelisk && \
	chmod +x bazelisk

RUN git clone https://github.com/google/perf_data_converter && \
	cd perf_data_converter && \
	/bazelisk build //src:perf_to_profile && \
	cp bazel-bin/src/perf_to_profile /perf_to_profile.exe # COPY --from won't be able to resolve symlink, so do it now.

# Actual image build.
FROM debian:trixie
LABEL org.opencontainers.image.authors="golang-dev@googlegroups.com"

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
# less: misc basic tool
# libc6-dev-i386: for 32-bit builds
# libc6-dev: for building Go's bootstrap 'dist' prog
# libgles2-mesa-dev: required by x/mobile repo
# libopenal-dev: required by x/mobile repo
# linux-perf: for performance analysis on perf builders
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
# unzip: used by racebuild
# vim-tiny: for interactive debugging convenience
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
	less \
	libc6-dev \
	libc6-dev-i386 \
	libgles2-mesa-dev \
	libopenal-dev \
	linux-perf \
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
	unzip \
	vim-tiny \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=stage0 /go/bin/* /usr/local/bin/
COPY --from=perftoprofile /perf_to_profile.exe /usr/local/bin/perf_to_profile

CMD ["/usr/local/bin/run-worker.sh"]
