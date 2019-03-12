# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.11
LABEL maintainer "golang-dev@googlegroups.com"

ENV CGO_ENABLED=0

# BEGIN deps (run `make update-deps` to update)

# Repo cloud.google.com/go at 5c31045 (2018-05-08)
ENV REV=5c31045bc3f4855c97f997a1940dfefc1598aa2d
RUN go get -d cloud.google.com/go/compute/metadata &&\
    (cd /go/src/cloud.google.com/go && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/net at d11bb6c (2018-05-07)
ENV REV=d11bb6cd8e3c4e60239c9cb20ef68586d74500d0
RUN go get -d golang.org/x/net/context `#and 2 other pkgs` &&\
    (cd /go/src/golang.org/x/net && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Optimization to speed up iterative development, not necessary for correctness:
RUN go install cloud.google.com/go/compute/metadata \
	golang.org/x/net/context \
	golang.org/x/net/context/ctxhttp
# END deps.

COPY . /go/src/golang.org/x/build/

# Install static binary to /go/bin/stage0
RUN go install golang.org/x/build/cmd/buildlet/stage0


