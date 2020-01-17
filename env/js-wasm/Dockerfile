# Copyright 2018 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM {{REPO}}/linux-x86-sid:latest
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive


RUN (curl -sL https://deb.nodesource.com/setup_13.x | bash -) && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*

CMD ["/usr/local/bin/stage0"]
