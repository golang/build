# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.12-stretch AS build
LABEL maintainer "golang-dev@googlegroups.com"

# BEGIN deps (run `make update-deps` to update)

# Repo go4.org at ce4c26f (2019-02-17)
ENV REV=ce4c26f7be8eb27dc77f996b08d286dd80bc4a01
RUN go get -d go4.org/types &&\
    (cd /go/src/go4.org && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Optimization to speed up iterative development, not necessary for correctness:
RUN go install go4.org/types
# END deps.

COPY . /go/src/golang.org/x/build/

RUN go install -ldflags "-X 'main.Version=$version'" golang.org/x/build/cmd/scaleway


FROM debian:stretch

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    netbase \
    curl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /go/bin/scaleway /scaleway
