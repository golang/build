# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.8
LABEL maintainer "golang-dev@googlegroups.com"

# BEGIN deps (run `make update-deps` to update)

# Repo cloud.google.com/go at ef305da (2017-07-26)
ENV REV=ef305dafe1fb55d8ee5fb61dd1e7b8f6a7d691e8
RUN go get -d cloud.google.com/go/compute/metadata `#and 15 other pkgs` &&\
    (cd /go/src/cloud.google.com/go && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/anmitsu/go-shlex at 648efa6 (2016-10-02)
ENV REV=648efa622239a2f6ff949fed78ee37b48d499ba4
RUN go get -d github.com/anmitsu/go-shlex &&\
    (cd /go/src/github.com/anmitsu/go-shlex && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/gliderlabs/ssh at cff9b0c (2017-07-26)
ENV REV=cff9b0cc853b8ecc208880f0f6b0701625d58f80
RUN go get -d github.com/gliderlabs/ssh &&\
    (cd /go/src/github.com/gliderlabs/ssh && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/golang/protobuf at 748d386 (2017-07-26)
ENV REV=748d386b5c1ea99658fd69fe9f03991ce86a90c1
RUN go get -d github.com/golang/protobuf/proto `#and 9 other pkgs` &&\
    (cd /go/src/github.com/golang/protobuf && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/googleapis/gax-go at 84ed267 (2017-06-10)
ENV REV=84ed26760e7f6f80887a2fbfb50db3cc415d2cea
RUN go get -d github.com/googleapis/gax-go &&\
    (cd /go/src/github.com/googleapis/gax-go && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/kr/pty at 2c10821 (2017-03-07)
ENV REV=2c10821df3c3cf905230d078702dfbe9404c9b23
RUN go get -d github.com/kr/pty &&\
    (cd /go/src/github.com/kr/pty && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo go4.org at 034d17a (2017-05-25)
ENV REV=034d17a462f7b2dcd1a4a73553ec5357ff6e6c6e
RUN go get -d go4.org/syncutil &&\
    (cd /go/src/go4.org && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/crypto at 558b687 (2017-07-28)
ENV REV=558b6879de74bc843225cde5686419267ff707ca
RUN go get -d golang.org/x/crypto/acme `#and 6 other pkgs` &&\
    (cd /go/src/golang.org/x/crypto && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/net at f5079bd (2017-07-26)
ENV REV=f5079bd7f6f74e23c4d65efa0f4ce14cbd6a3c0f
RUN go get -d golang.org/x/net/context `#and 8 other pkgs` &&\
    (cd /go/src/golang.org/x/net && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/oauth2 at b53b38a (2017-07-19)
ENV REV=b53b38ad8a6435bd399ea76d0fa74f23149cca4e
RUN go get -d golang.org/x/oauth2 `#and 5 other pkgs` &&\
    (cd /go/src/golang.org/x/oauth2 && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/perf at 4979bd1 (2017-07-06)
ENV REV=4979bd159b01a7695a1b277f4ea76cab354f278c
RUN go get -d golang.org/x/perf/storage `#and 2 other pkgs` &&\
    (cd /go/src/golang.org/x/perf && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/sync at f52d181 (2017-05-17)
ENV REV=f52d1811a62927559de87708c8913c1650ce4f26
RUN go get -d golang.org/x/sync/semaphore &&\
    (cd /go/src/golang.org/x/sync && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/text at 836efe4 (2017-07-14)
ENV REV=836efe42bb4aa16aaa17b9c155d8813d336ed720
RUN go get -d golang.org/x/text/secure/bidirule `#and 4 other pkgs` &&\
    (cd /go/src/golang.org/x/text && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/time at 8be79e1 (2017-04-24)
ENV REV=8be79e1e0910c292df4e79c241bb7e8f7e725959
RUN go get -d golang.org/x/time/rate &&\
    (cd /go/src/golang.org/x/time && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo google.golang.org/api at 295e4bb (2017-07-18)
ENV REV=295e4bb0ade057ae2cfb9876ab0b54635dbfcea4
RUN go get -d google.golang.org/api/compute/v1 `#and 15 other pkgs` &&\
    (cd /go/src/google.golang.org/api && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo google.golang.org/genproto at b0a3dcf (2017-07-12)
ENV REV=b0a3dcfcd1a9bd48e63634bd8802960804cf8315
RUN go get -d google.golang.org/genproto/googleapis/api/annotations `#and 13 other pkgs` &&\
    (cd /go/src/google.golang.org/genproto && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo google.golang.org/grpc at 3ddcdc2 (2017-07-26)
ENV REV=3ddcdc268d88595eb2f3721f7dc87970a6c3ab6e
RUN go get -d google.golang.org/grpc `#and 15 other pkgs` &&\
    (cd /go/src/google.golang.org/grpc && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo gopkg.in/inf.v0 at 3887ee9 (2015-09-11)
ENV REV=3887ee99ecf07df5b447e9b00d9c0b2adaa9f3e4
RUN go get -d gopkg.in/inf.v0 &&\
    (cd /go/src/gopkg.in/inf.v0 && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo grpc.go4.org at 11d0a25 (2017-06-09)
ENV REV=11d0a25b491971beb5a4625ea7856a3c4afaafa5
RUN go get -d grpc.go4.org `#and 11 other pkgs` &&\
    (cd /go/src/grpc.go4.org && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Optimization to speed up iterative development, not necessary for correctness:
RUN go install cloud.google.com/go/compute/metadata \
	cloud.google.com/go/datastore \
	cloud.google.com/go/errorreporting/apiv1beta1 \
	cloud.google.com/go/errors \
	cloud.google.com/go/iam \
	cloud.google.com/go/internal \
	cloud.google.com/go/internal/atomiccache \
	cloud.google.com/go/internal/fields \
	cloud.google.com/go/internal/optional \
	cloud.google.com/go/internal/version \
	cloud.google.com/go/logging \
	cloud.google.com/go/logging/apiv2 \
	cloud.google.com/go/logging/internal \
	cloud.google.com/go/monitoring/apiv3 \
	cloud.google.com/go/storage \
	github.com/anmitsu/go-shlex \
	github.com/gliderlabs/ssh \
	github.com/golang/protobuf/proto \
	github.com/golang/protobuf/protoc-gen-go/descriptor \
	github.com/golang/protobuf/ptypes \
	github.com/golang/protobuf/ptypes/any \
	github.com/golang/protobuf/ptypes/duration \
	github.com/golang/protobuf/ptypes/empty \
	github.com/golang/protobuf/ptypes/struct \
	github.com/golang/protobuf/ptypes/timestamp \
	github.com/golang/protobuf/ptypes/wrappers \
	github.com/googleapis/gax-go \
	github.com/kr/pty \
	go4.org/syncutil \
	golang.org/x/crypto/acme \
	golang.org/x/crypto/acme/autocert \
	golang.org/x/crypto/curve25519 \
	golang.org/x/crypto/ed25519 \
	golang.org/x/crypto/ed25519/internal/edwards25519 \
	golang.org/x/crypto/ssh \
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
	golang.org/x/perf/storage \
	golang.org/x/perf/storage/benchfmt \
	golang.org/x/sync/semaphore \
	golang.org/x/text/secure/bidirule \
	golang.org/x/text/transform \
	golang.org/x/text/unicode/bidi \
	golang.org/x/text/unicode/norm \
	golang.org/x/time/rate \
	google.golang.org/api/compute/v1 \
	google.golang.org/api/container/v1 \
	google.golang.org/api/gensupport \
	google.golang.org/api/googleapi \
	google.golang.org/api/googleapi/internal/uritemplates \
	google.golang.org/api/googleapi/transport \
	google.golang.org/api/internal \
	google.golang.org/api/iterator \
	google.golang.org/api/oauth2/v2 \
	google.golang.org/api/option \
	google.golang.org/api/storage/v1 \
	google.golang.org/api/support/bundler \
	google.golang.org/api/transport \
	google.golang.org/api/transport/grpc \
	google.golang.org/api/transport/http \
	google.golang.org/genproto/googleapis/api/annotations \
	google.golang.org/genproto/googleapis/api/distribution \
	google.golang.org/genproto/googleapis/api/label \
	google.golang.org/genproto/googleapis/api/metric \
	google.golang.org/genproto/googleapis/api/monitoredres \
	google.golang.org/genproto/googleapis/datastore/v1 \
	google.golang.org/genproto/googleapis/devtools/clouderrorreporting/v1beta1 \
	google.golang.org/genproto/googleapis/iam/v1 \
	google.golang.org/genproto/googleapis/logging/type \
	google.golang.org/genproto/googleapis/logging/v2 \
	google.golang.org/genproto/googleapis/monitoring/v3 \
	google.golang.org/genproto/googleapis/rpc/status \
	google.golang.org/genproto/googleapis/type/latlng \
	google.golang.org/grpc \
	google.golang.org/grpc/codes \
	google.golang.org/grpc/credentials \
	google.golang.org/grpc/credentials/oauth \
	google.golang.org/grpc/grpclb/grpc_lb_v1 \
	google.golang.org/grpc/grpclog \
	google.golang.org/grpc/internal \
	google.golang.org/grpc/keepalive \
	google.golang.org/grpc/metadata \
	google.golang.org/grpc/naming \
	google.golang.org/grpc/peer \
	google.golang.org/grpc/stats \
	google.golang.org/grpc/status \
	google.golang.org/grpc/tap \
	google.golang.org/grpc/transport \
	gopkg.in/inf.v0 \
	grpc.go4.org \
	grpc.go4.org/codes \
	grpc.go4.org/credentials \
	grpc.go4.org/grpclog \
	grpc.go4.org/internal \
	grpc.go4.org/metadata \
	grpc.go4.org/naming \
	grpc.go4.org/peer \
	grpc.go4.org/stats \
	grpc.go4.org/tap \
	grpc.go4.org/transport
# END deps.

# Makefile passes a string with --build-arg version
# This becomes part of the cache key for all subsequent instructions,
# so it must not be placed above the "go get" commands above.
ARG version=unknown

COPY . /go/src/golang.org/x/build/

RUN go install -ldflags "-linkmode=external -extldflags '-static -pthread' -X 'main.Version=$version'" golang.org/x/build/cmd/coordinator
