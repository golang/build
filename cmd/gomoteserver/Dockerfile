# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.23-bookworm AS build
LABEL maintainer="golang-dev@googlegroups.com"

COPY go.mod /go/src/golang.org/x/build/go.mod
COPY go.sum /go/src/golang.org/x/build/go.sum

WORKDIR /go/src/golang.org/x/build

# Download module dependencies to improve speed of re-building the
# Docker image during minor code changes.
RUN go mod download

# Makefile passes a string with --build-arg version
# This becomes part of the cache key for all subsequent instructions,
# so it must not be placed above the "go mod download" command above.
ARG version=unknown

COPY . /go/src/golang.org/x/build/

RUN go install -ldflags "-X 'main.Version=$version'" golang.org/x/build/cmd/gomoteserver

FROM debian:bookworm AS build_drawterm

RUN apt-get update && apt-get install -y --no-install-recommends \
    git-core ca-certificates make gcc libc6-dev libx11-dev

# drawterm connects to plan9 instances like:
#    echo glenda123 | ./drawterm -a <addr> -c <addr> -u glenda -k user=glenda
# Where <addr> is the IP address of the Plan 9 instance on GCE,
# "glenda" is the username and "glenda123" is the password.
RUN git clone https://github.com/0intro/conterm /tmp/conterm && \
    cd /tmp/conterm && \
    CONF=unix make && mv /tmp/conterm/drawterm /usr/local/bin && \
    rm -rf /tmp/conterm

FROM debian:bookworm

# openssh client is for the gomote ssh proxy client.
# telnet is for the gomote ssh proxy to windows. (no ssh server there)
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	ca-certificates \
	openssh-client \
	telnet \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /go/bin/gomoteserver /
COPY --from=build_drawterm /usr/local/bin/drawterm /usr/local/bin/

ENTRYPOINT ["/gomoteserver"]
