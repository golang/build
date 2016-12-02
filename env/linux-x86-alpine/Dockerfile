# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Alpine Linux builder
# Docker tag gcr.io/go-dashboard-dev/linux-x86-alpine (staging)
# and gcr.io/symbolic-datum-552/linux-x86-alpine (prod)

FROM alpine:3.5
MAINTAINER golang-dev <golang-dev@googlegroups.com>

RUN apk add --no-cache \
	bash \
	binutils \
	build-base \
	ca-certificates \
	curl \
	gcc \
	gdb \
	gfortran \
	git \
	go \
	libc-dev \
	lsof \
	procps \
	strace

RUN curl -o /usr/local/bin/stage0 https://storage.googleapis.com/go-builder-data/buildlet-stage0.linux-amd64-static
RUN chmod +x /usr/local/bin/stage0

ENV GOROOT_BOOTSTRAP=/usr/lib/go
