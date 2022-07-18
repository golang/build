# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.18 AS build
LABEL maintainer="golang-dev@googlegroups.com"

RUN mkdir /gocache
ENV GOCACHE /gocache

COPY go.mod /go/src/golang.org/x/build/go.mod
COPY go.sum /go/src/golang.org/x/build/go.sum

WORKDIR /go/src/golang.org/x/build

# Download module dependencies to improve speed of re-building the
# Docker image during minor code changes.
RUN go mod download

COPY . /go/src/golang.org/x/build/

RUN go install golang.org/x/build/vcs-test/vcweb

FROM debian:stable
LABEL maintainer="golang-dev@googlegroups.com"

# Commands necessary to run various VCS servers.
# Subversion is handled by the Apache sidecar pod.
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
        ca-certificates \
        mercurial \
        fossil \
        bzr \
        git \
        tini \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /go/bin/vcweb /
ENTRYPOINT ["/usr/bin/tini", "--", "/vcweb"]
