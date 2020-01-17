# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.13 AS build
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

COPY . /go/src/golang.org/x/build/

# Install binary to /go/bin/stage0
RUN go install golang.org/x/build/cmd/buildlet/stage0
RUN CGO_ENABLED=0 go build -o /go/bin/stage0.static golang.org/x/build/cmd/buildlet/stage0

FROM golang:1.13
COPY --from=build /go/bin/stage0 /go/bin/stage0
COPY --from=build /go/bin/stage0.static /go/bin/stage0.static
