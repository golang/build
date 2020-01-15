# Copyright 2020 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# This is not a builder image. Rather, it's a Debian image with common
# debugging tools, useful when debugging things on Container Optimized
# OS VMs without external network access that can at least run docker
# images from gcr.io.

FROM debian:stretch
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive

RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	netbase \
	tcpdump \
	ca-certificates \
	curl \
	gdb \
	strace \
	gcc \
	libc6-dev \
	procps \
	lsof \
	psmisc \
	openssh-server \
	git \
	iptables \
	screen \
	&& rm -rf /var/lib/apt/lists/*

CMD ["/usr/bin/bash"]

