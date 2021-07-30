# Copyright 2018 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM {{REPO}}/linux-x86-sid:latest
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive

# A copy of https://deb.nodesource.com/setup_14.x.
COPY setup_14.x setup_14.x

RUN bash setup_14.x && \
    apt-get install -y nodejs && \
    rm -rf setup_14.x /var/lib/apt/lists/*

CMD ["/usr/local/bin/stage0"]
