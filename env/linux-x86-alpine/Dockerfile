# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Alpine Linux builder
# Docker tag gcr.io/go-dashboard-dev/linux-x86-alpine (staging)
# and gcr.io/symbolic-datum-552/linux-x86-alpine (prod)

FROM golang/buildlet-stage0 AS stage0

FROM alpine:3.10
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
	openssh \
	procps \
	strace

RUN ssh-keygen -A
RUN bash -c "(echo ChallengeResponseAuthentication no; echo PasswordAuthentication no; echo PermitRootLogin yes) > /etc/ssh/sshd_config"
RUN passwd -u root

COPY --from=stage0 /go/bin/stage0.static /usr/local/bin/stage0

ENV GOROOT_BOOTSTRAP=/usr/lib/go

CMD ["/usr/local/bin/stage0"]
