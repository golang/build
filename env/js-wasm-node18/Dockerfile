# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM {{REPO}}/linux-x86-sid:20221109
LABEL maintainer="golang-dev@googlegroups.com"

ENV DEBIAN_FRONTEND noninteractive

# A copy of https://deb.nodesource.com/setup_18.x.
COPY setup_18.x setup_18.x

RUN bash setup_18.x && \
    apt-get install -y nodejs && \
    rm -rf setup_18.x /var/lib/apt/lists/*

CMD ["/usr/local/bin/stage0"]
