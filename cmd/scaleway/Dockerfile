# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.12-stretch AS build
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
RUN go install go4.org/types

COPY . /go/src/golang.org/x/build/

# Install binary to /go/bin:
RUN go install golang.org/x/build/cmd/scaleway

FROM debian:stretch

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    netbase \
    curl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /go/bin/scaleway /scaleway
