# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Note that OpenSSH 6.5+ is required for the Github SSH private key, which requires
# at least Debian Jessie (not Wheezy). This uses Jessie:
FROM golang:1.8
LABEL maintainer "golang-dev@googlegroups.com"

# BEGIN deps (run `make update-deps` to update)

# Repo cloud.google.com/go at 76d607c (2017-07-20)
ENV REV=76d607c4e7a2b9df49f1d1a58a3f3d2dd2614704
RUN go get -d cloud.google.com/go/compute/metadata &&\
    (cd /go/src/cloud.google.com/go && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/golang/protobuf at 0a4f71a (2017-07-11)
ENV REV=0a4f71a498b7c4812f64969510bcb4eca251e33a
RUN go get -d github.com/golang/protobuf/proto `#and 5 other pkgs` &&\
    (cd /go/src/github.com/golang/protobuf && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/google/go-github at e062a8c (2017-07-19)
ENV REV=e062a8cd852f0fb981c8c9f837c214c4d1031870
RUN go get -d github.com/google/go-github/github &&\
    (cd /go/src/github.com/google/go-github && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/google/go-querystring at 53e6ce1 (2017-01-11)
ENV REV=53e6ce116135b80d037921a7fdd5138cf32d7a8a
RUN go get -d github.com/google/go-querystring/query &&\
    (cd /go/src/github.com/google/go-querystring && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/gregjones/httpcache at 0d2297f (2017-04-24)
ENV REV=0d2297f241a3503b4d464cd434e6d1490ec76e9a
RUN go get -d github.com/gregjones/httpcache &&\
    (cd /go/src/github.com/gregjones/httpcache && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo go4.org at 034d17a (2017-05-25)
ENV REV=034d17a462f7b2dcd1a4a73553ec5357ff6e6c6e
RUN go get -d go4.org/types &&\
    (cd /go/src/go4.org && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/net at ab54850 (2017-07-21)
ENV REV=ab5485076ff3407ad2d02db054635913f017b0ed
RUN go get -d golang.org/x/net/context `#and 2 other pkgs` &&\
    (cd /go/src/golang.org/x/net && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/oauth2 at b53b38a (2017-07-19)
ENV REV=b53b38ad8a6435bd399ea76d0fa74f23149cca4e
RUN go get -d golang.org/x/oauth2 `#and 2 other pkgs` &&\
    (cd /go/src/golang.org/x/oauth2 && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/sync at 457c582 (2016-07-15)
ENV REV=457c5828408160d6a47e17645169cf8fa20218c4
RUN go get -d golang.org/x/sync/errgroup &&\
    (cd /go/src/golang.org/x/sync && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Optimization to speed up iterative development, not necessary for correctness:
RUN go install cloud.google.com/go/compute/metadata \
	github.com/golang/protobuf/proto \
	github.com/golang/protobuf/ptypes \
	github.com/golang/protobuf/ptypes/any \
	github.com/golang/protobuf/ptypes/duration \
	github.com/golang/protobuf/ptypes/timestamp \
	github.com/google/go-github/github \
	github.com/google/go-querystring/query \
	github.com/gregjones/httpcache \
	go4.org/types \
	golang.org/x/net/context \
	golang.org/x/net/context/ctxhttp \
	golang.org/x/oauth2 \
	golang.org/x/oauth2/internal \
	golang.org/x/sync/errgroup
# END deps

COPY . /go/src/golang.org/x/build/

RUN go install golang.org/x/build/cmd/gitmirror
