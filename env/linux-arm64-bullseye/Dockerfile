# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM arm64v8/golang:1.19 AS build
LABEL maintainer="golang-dev@googlegroups.com"

RUN mkdir /gocache
ENV GOCACHE /gocache

COPY ./go.mod /go/src/golang.org/x/build/go.mod
COPY ./go.sum /go/src/golang.org/x/build/go.sum

WORKDIR /go/src/golang.org/x/build

# Optimization for iterative docker build speed, not necessary for correctness:
# TODO: write a tool to make writing Go module-friendly Dockerfiles easier.
RUN go install cloud.google.com/go/compute/metadata

COPY . /go/src/golang.org/x/build/

# Install binary to /go/bin/stage0
ENV CGO_ENABLED=0
RUN go install golang.org/x/build/cmd/buildlet/stage0
RUN go install golang.org/x/build/cmd/bootstrapswarm

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
# unzip: used by racebuild
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
	iptables \
	iproute2 \
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
	sudo \
	swig \
	unzip \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /go/bin/* /usr/local/bin/
COPY cmd/buildlet/stage0/run-worker.sh /usr/local/bin/

CMD ["/usr/local/bin/run-worker.sh"]
