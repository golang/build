# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM debian:buster

ENV DEBIAN_FRONTEND noninteractive

RUN apt-get update && \
    apt-get install --yes \
          gcc curl strace \
          ca-certificates netbase \
          procps lsof psmisc \
          openssh-server

RUN mkdir /usr/local/go-bootstrap && \
    curl --silent https://storage.googleapis.com/go-builder-data/gobootstrap-linux-arm64.tar.gz | \
    tar -C /usr/local/go-bootstrap -zxv

ENV GOROOT_BOOTSTRAP /usr/local/go-bootstrap
RUN curl -o  /usr/local/bin/stage0 https://storage.googleapis.com/go-builder-data/buildlet-stage0.linux-arm64 && \
    chmod +x /usr/local/bin/stage0

ENV GO_BUILD_KEY_DELETE_AFTER_READ true
ENV GO_BUILD_KEY_PATH /buildkey/gobuildkey
