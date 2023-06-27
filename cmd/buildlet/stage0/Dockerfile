# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.19 AS build
LABEL maintainer="golang-dev@googlegroups.com"

RUN mkdir /gocache
ENV GOCACHE /gocache

COPY go.mod /go/src/golang.org/x/build/go.mod
COPY go.sum /go/src/golang.org/x/build/go.sum

WORKDIR /go/src/golang.org/x/build

# Optimization for iterative docker build speed, not necessary for correctness:
# TODO: write a tool to make writing Go module-friendly Dockerfiles easier.
RUN go install cloud.google.com/go/compute/metadata

COPY . /go/src/golang.org/x/build/

# Install binary to /go/bin/stage0
ENV CGO_ENABLED=0
RUN go install golang.org/x/build/cmd/buildlet/stage0
RUN go install golang.org/x/build/cmd/bootstrapswarm

FROM golang:1.19
COPY --from=build /go/bin/* /go/bin/
COPY cmd/buildlet/stage0/run-worker.sh /go/bin