# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.12 AS build
LABEL maintainer "golang-dev@googlegroups.com"

ENV GO111MODULE=on
ENV GOPROXY=https://proxy.golang.org

RUN mkdir /gocache
ENV GOCACHE /gocache

COPY go.mod /go/src/golang.org/x/build/go.mod
COPY go.sum /go/src/golang.org/x/build/go.sum

WORKDIR /go/src/golang.org/x/build

# Optimization for iterative docker build speed, not necessary for correctness:
# TODO: write a tool to make writing Go module-friendly Dockerfiles easier.
RUN go install cloud.google.com/go/compute/metadata
RUN go install github.com/bradfitz/go-smtpd/smtpd
RUN go install github.com/jellevandenhooff/dkim
RUN go install go4.org/types
RUN go install golang.org/x/crypto/acme/autocert

COPY . /go/src/golang.org/x/build/

# Install binary to /go/bin:
RUN go install golang.org/x/build/cmd/pubsubhelper

FROM debian:stretch
LABEL maintainer "golang-dev@googlegroups.com"

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

COPY --from=build /go/bin/pubsubhelper /
ENTRYPOINT ["/pubsubhelper"]
