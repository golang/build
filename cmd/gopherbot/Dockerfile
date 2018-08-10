# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
FROM golang:1.10 AS build
LABEL maintainer "golang-dev@googlegroups.com"

ENV CGO_ENABLED=0

# BEGIN deps (run `make update-deps` to update)

# Repo cloud.google.com/go at d0195c2 (2018-08-16)
ENV REV=d0195c2919dd698efd742e4a4c92cfcab64252a0
RUN go get -d cloud.google.com/go/compute/metadata &&\
    (cd /go/src/cloud.google.com/go && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/golang/protobuf at 89a0c16 (2018-08-04)
ENV REV=89a0c16f4dc2a70c0ed864d8ef64878f24fdaa51
RUN go get -d github.com/golang/protobuf/proto `#and 5 other pkgs` &&\
    (cd /go/src/github.com/golang/protobuf && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/google/go-github at dae5b87 (2018-08-10)
ENV REV=dae5b8767170392dd51fafc89da6605faa51a1c2
RUN go get -d github.com/google/go-github/github &&\
    (cd /go/src/github.com/google/go-github && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/google/go-querystring at 53e6ce1 (2017-01-11)
ENV REV=53e6ce116135b80d037921a7fdd5138cf32d7a8a
RUN go get -d github.com/google/go-querystring/query &&\
    (cd /go/src/github.com/google/go-querystring && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/gregjones/httpcache at 9cad4c3 (2018-03-06)
ENV REV=9cad4c3443a7200dd6400aef47183728de563a38
RUN go get -d github.com/gregjones/httpcache &&\
    (cd /go/src/github.com/gregjones/httpcache && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo go4.org at 417644f (2018-08-09)
ENV REV=417644f6feb5ed3a356ca5d6d8e3a3fac7dfd33f
RUN go get -d go4.org/strutil `#and 2 other pkgs` &&\
    (cd /go/src/go4.org && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/net at aaf6012 (2018-08-16)
ENV REV=aaf60122140d3fcf75376d319f0554393160eb50
RUN go get -d golang.org/x/net/context `#and 2 other pkgs` &&\
    (cd /go/src/golang.org/x/net && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/oauth2 at 3d292e4 (2018-07-24)
ENV REV=3d292e4d0cdc3a0113e6d207bb137145ef1de42f
RUN go get -d golang.org/x/oauth2 `#and 2 other pkgs` &&\
    (cd /go/src/golang.org/x/oauth2 && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/sync at 1d60e46 (2018-03-14)
ENV REV=1d60e4601c6fd243af51cc01ddf169918a5407ca
RUN go get -d golang.org/x/sync/errgroup &&\
    (cd /go/src/golang.org/x/sync && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/time at fbb02b2 (2018-04-12)
ENV REV=fbb02b2291d28baffd63558aa44b4b56f178d650
RUN go get -d golang.org/x/time/rate &&\
    (cd /go/src/golang.org/x/time && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

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
	go4.org/strutil \
	go4.org/types \
	golang.org/x/net/context \
	golang.org/x/net/context/ctxhttp \
	golang.org/x/oauth2 \
	golang.org/x/oauth2/internal \
	golang.org/x/sync/errgroup \
	golang.org/x/time/rate
# END deps

COPY . /go/src/golang.org/x/build/

RUN go install golang.org/x/build/cmd/gopherbot


FROM scratch
LABEL maintainer "golang-dev@googlegroups.com"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /go/bin/gopherbot /
ENTRYPOINT ["/gopherbot"]
