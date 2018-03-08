# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:latest as builder

ENV CGO_ENABLED=0

# BEGIN deps (run `make update-deps` to update)

# Repo cloud.google.com/go at b71a665 (2018-02-16)
ENV REV=b71a6650976333496f3c6596bfba1507784b5990
RUN go get -d cloud.google.com/go/compute/metadata `#and 6 other pkgs` &&\
  (cd /go/src/cloud.google.com/go && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/golang/protobuf at bbd03ef (2018-02-02)
ENV REV=bbd03ef6da3a115852eaf24c8a1c46aeb39aa175
RUN go get -d github.com/golang/protobuf/proto `#and 6 other pkgs` &&\
  (cd /go/src/github.com/golang/protobuf && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/google/go-github at 922ceac (2018-01-16)
ENV REV=922ceac0585d40f97d283d921f872fc50480e06e
RUN go get -d github.com/google/go-github/github &&\
  (cd /go/src/github.com/google/go-github && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/google/go-querystring at 53e6ce1 (2017-01-11)
ENV REV=53e6ce116135b80d037921a7fdd5138cf32d7a8a
RUN go get -d github.com/google/go-querystring/query &&\
  (cd /go/src/github.com/google/go-querystring && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/googleapis/gax-go at 317e000 (2017-09-15)
ENV REV=317e0006254c44a0ac427cc52a0e083ff0b9622f
RUN go get -d github.com/googleapis/gax-go &&\
  (cd /go/src/github.com/googleapis/gax-go && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/gregjones/httpcache at 2bcd89a (2017-11-19)
ENV REV=2bcd89a1743fd4b373f7370ce8ddc14dfbd18229
RUN go get -d github.com/gregjones/httpcache &&\
  (cd /go/src/github.com/gregjones/httpcache && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo go4.org at fba789b (2018-01-03)
ENV REV=fba789b7e39ba524b9e60c45c37a50fae63a2a09
RUN go get -d go4.org/types &&\
  (cd /go/src/go4.org && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/crypto at 13931e2 (2018-01-12)
ENV REV=13931e22f9e72ea58bb73048bc752b48c6d4d4ac
RUN go get -d golang.org/x/crypto/acme `#and 2 other pkgs` &&\
  (cd /go/src/golang.org/x/crypto && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/net at 2e7f24a (2018-02-16)
ENV REV=2e7f24ace30034db6c258ddb329e5236a68c27fa
RUN go get -d golang.org/x/net/context `#and 8 other pkgs` &&\
  (cd /go/src/golang.org/x/net && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/oauth2 at 543e378 (2018-02-07)
ENV REV=543e37812f10c46c622c9575afd7ad22f22a12ba
RUN go get -d golang.org/x/oauth2 `#and 5 other pkgs` &&\
  (cd /go/src/golang.org/x/oauth2 && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/sync at fd80eb9 (2017-11-01)
ENV REV=fd80eb99c8f653c847d294a001bdf2a3a6f768f5
RUN go get -d golang.org/x/sync/errgroup &&\
  (cd /go/src/golang.org/x/sync && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/text at 4e4a321 (2018-02-08)
ENV REV=4e4a3210bb54bb31f6ab2cdca2edcc0b50c420c1
RUN go get -d golang.org/x/text/secure/bidirule `#and 4 other pkgs` &&\
  (cd /go/src/golang.org/x/text && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo google.golang.org/api at 3acc4f9 (2018-02-15)
ENV REV=3acc4f94acb5b02441cce6db56fc82a1016eadb4
RUN go get -d google.golang.org/api/gensupport `#and 9 other pkgs` &&\
  (cd /go/src/google.golang.org/api && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo google.golang.org/genproto at 2b5a72b (2018-02-06)
ENV REV=2b5a72b8730b0b16380010cfe5286c42108d88e7
RUN go get -d google.golang.org/genproto/googleapis/api/annotations `#and 3 other pkgs` &&\
  (cd /go/src/google.golang.org/genproto && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo google.golang.org/grpc at dfa1834 (2018-02-15)
ENV REV=dfa18343df54bda471a4b53677aa7c0d0df882d1
RUN go get -d google.golang.org/grpc `#and 23 other pkgs` &&\
  (cd /go/src/google.golang.org/grpc && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Optimization to speed up iterative development, not necessary for correctness:
RUN go install cloud.google.com/go/compute/metadata \
  cloud.google.com/go/iam \
  cloud.google.com/go/internal \
  cloud.google.com/go/internal/optional \
  cloud.google.com/go/internal/version \
  cloud.google.com/go/storage \
  github.com/golang/protobuf/proto \
  github.com/golang/protobuf/protoc-gen-go/descriptor \
  github.com/golang/protobuf/ptypes \
  github.com/golang/protobuf/ptypes/any \
  github.com/golang/protobuf/ptypes/duration \
  github.com/golang/protobuf/ptypes/timestamp \
  github.com/google/go-github/github \
  github.com/google/go-querystring/query \
  github.com/googleapis/gax-go \
  github.com/gregjones/httpcache \
  go4.org/types \
  golang.org/x/crypto/acme \
  golang.org/x/crypto/acme/autocert \
  golang.org/x/net/context \
  golang.org/x/net/context/ctxhttp \
  golang.org/x/net/http2 \
  golang.org/x/net/http2/hpack \
  golang.org/x/net/idna \
  golang.org/x/net/internal/timeseries \
  golang.org/x/net/lex/httplex \
  golang.org/x/net/trace \
  golang.org/x/oauth2 \
  golang.org/x/oauth2/google \
  golang.org/x/oauth2/internal \
  golang.org/x/oauth2/jws \
  golang.org/x/oauth2/jwt \
  golang.org/x/sync/errgroup \
  golang.org/x/text/secure/bidirule \
  golang.org/x/text/transform \
  golang.org/x/text/unicode/bidi \
  golang.org/x/text/unicode/norm \
  google.golang.org/api/gensupport \
  google.golang.org/api/googleapi \
  google.golang.org/api/googleapi/internal/uritemplates \
  google.golang.org/api/googleapi/transport \
  google.golang.org/api/internal \
  google.golang.org/api/iterator \
  google.golang.org/api/option \
  google.golang.org/api/storage/v1 \
  google.golang.org/api/transport/http \
  google.golang.org/genproto/googleapis/api/annotations \
  google.golang.org/genproto/googleapis/iam/v1 \
  google.golang.org/genproto/googleapis/rpc/status \
  google.golang.org/grpc \
  google.golang.org/grpc/balancer \
  google.golang.org/grpc/balancer/base \
  google.golang.org/grpc/balancer/roundrobin \
  google.golang.org/grpc/codes \
  google.golang.org/grpc/connectivity \
  google.golang.org/grpc/credentials \
  google.golang.org/grpc/encoding \
  google.golang.org/grpc/encoding/proto \
  google.golang.org/grpc/grpclb/grpc_lb_v1/messages \
  google.golang.org/grpc/grpclog \
  google.golang.org/grpc/internal \
  google.golang.org/grpc/keepalive \
  google.golang.org/grpc/metadata \
  google.golang.org/grpc/naming \
  google.golang.org/grpc/peer \
  google.golang.org/grpc/resolver \
  google.golang.org/grpc/resolver/dns \
  google.golang.org/grpc/resolver/passthrough \
  google.golang.org/grpc/stats \
  google.golang.org/grpc/status \
  google.golang.org/grpc/tap \
  google.golang.org/grpc/transport
# END deps

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
