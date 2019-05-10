# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:latest as builder

# Because alpine is used below... TODO: just use Debian below and re-enable this?
ENV CGO_ENABLED=0

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
RUN go install golang.org/x/oauth2
COPY autocertcache /go/src/golang.org/x/build/autocertcache
COPY gerrit /go/src/golang.org/x/build/gerrit
COPY maintner /go/src/golang.org/x/build/maintner
COPY internal /go/src/golang.org/x/build/internal
COPY cmd/pubsubhelper/pubsubtypes /go/src/golang.org/x/build/cmd/pubsubhelper/pubsubtypes
RUN go install golang.org/x/build/gerrit
RUN go install golang.org/x/build/internal/https
RUN go install golang.org/x/build/maintner
RUN go install golang.org/x/build/maintner/godata

COPY . /go/src/golang.org/x/build/
RUN go install golang.org/x/build/cmd/gerritbot

FROM alpine
LABEL maintainer "golang-dev@googlegroups.com"
# See https://github.com/golang/go/issues/23705 for why tini is needed
RUN apk add --no-cache git tini
RUN git config --global user.email "letsusegerrit@gmail.com"
RUN git config --global user.name "GerritBot"
RUN git config --global http.cookiefile "/gitcookies"
COPY --from=builder /go/bin/gerritbot /
ENTRYPOINT ["/sbin/tini", "--", "/gerritbot"]
