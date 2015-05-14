# Copyright 2015 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM armbuild/ubuntu:trusty

MAINTAINER golang-dev <golang-dev@googlegroups.com>
ENV DEBIAN_FRONTEND noninteractive

RUN apt-get update
RUN apt-get install -y --no-install-recommends curl

RUN echo "607573c55dc89d135c3c9c84bba6ba6095a37a1e  go.tar.gz" > /tmp/go.tar.gz.sha1
RUN cd /tmp && \
     curl --silent -o go.tar.gz http://dave.cheney.net/paste/go1.4.2.linux-arm~multiarch-armv7-1.tar.gz && \
     sha1sum -c go.tar.gz.sha1 && \
     tar -C /usr/local -zxvf go.tar.gz && \
     rm -rf go.tar.gz

RUN apt-get install -y --no-install-recommends ca-certificates

RUN mkdir /usr/local/gomaster
ENV GO_MASTER_VERSION d4bb72b4
RUN curl https://go.googlesource.com/go/+archive/$GO_MASTER_VERSION.tar.gz  | tar -C /usr/local/gomaster -zxv
ENV GOROOT /usr/local/gomaster
RUN echo "devel $GO_MASTER_VERSION" > $GOROOT/VERSION

RUN apt-get install -y --no-install-recommends gcc
RUN apt-get install -y --no-install-recommends libc6-dev

ENV GOROOT_BOOTSTRAP /usr/local/go
RUN cd $GOROOT/src && ./make.bash

RUN apt-get install -y --no-install-recommends git-core

ENV GOPATH /gopath
RUN mkdir /gopath
RUN $GOROOT/bin/go get golang.org/x/build/cmd/buildlet

ADD run-buildlet.sh /usr/local/bin/run-buildlet.sh

# Environment variables to be passed to "docker run"
# for linux-arm and linux-arm-arm5:
ENV BUILDKEY_ARM="" BUILDKEY_ARM5=""

ENTRYPOINT ["/usr/local/bin/run-buildlet.sh"]
