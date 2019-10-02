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
RUN go install github.com/google/go-github/github
RUN go install go4.org/strutil
RUN go install grpc.go4.org
COPY gerrit /go/src/golang.org/x/build/gerrit
RUN go install golang.org/x/build/gerrit
COPY maintner /go/src/golang.org/x/build/maintner
COPY cmd/pubsubhelper /go/src/golang.org/x/build/cmd/pubsubhelper
COPY internal/foreach /go/src/golang.org/x/build/internal/foreach
RUN go install golang.org/x/build/maintner/maintnerd/apipb
RUN go install golang.org/x/build/maintner/godata

COPY . /go/src/golang.org/x/build/

RUN go install golang.org/x/build/cmd/gopherbot


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

COPY --from=build /go/bin/gopherbot /
ENTRYPOINT ["/gopherbot"]
