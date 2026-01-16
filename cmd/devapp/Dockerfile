# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.25-trixie AS builder
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
# Install binary to /go/bin:
RUN go install golang.org/x/build/devapp

FROM debian:trixie
LABEL maintainer="golang-dev@googlegroups.com"

# netbase and ca-certificates are needed for dialing TLS.
# The rest are useful for debugging if somebody needs to exec into the container.
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	netbase \
	ca-certificates \
	curl \
	strace \
	procps \
	lsof \
	psmisc \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=builder /go/bin/devapp /
COPY devapp/static /static
COPY devapp/templates /templates
ENTRYPOINT ["/devapp"]
