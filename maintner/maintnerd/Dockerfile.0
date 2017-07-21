# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
FROM golang:1.8
LABEL maintainer "golang-dev@googlegroups.com"

# BEGIN deps (run `make update-deps` to update)

# Repo cloud.google.com/go at 76d607c (2017-07-20)
ENV REV=76d607c4e7a2b9df49f1d1a58a3f3d2dd2614704
RUN go get -d cloud.google.com/go/compute/metadata `#and 6 other pkgs` &&\
    (cd /go/src/cloud.google.com/go && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/golang/protobuf at 0a4f71a (2017-07-11)
ENV REV=0a4f71a498b7c4812f64969510bcb4eca251e33a
RUN go get -d github.com/golang/protobuf/proto `#and 6 other pkgs` &&\
    (cd /go/src/github.com/golang/protobuf && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/google/go-github at e062a8c (2017-07-19)
ENV REV=e062a8cd852f0fb981c8c9f837c214c4d1031870
RUN go get -d github.com/google/go-github/github &&\
    (cd /go/src/github.com/google/go-github && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/google/go-querystring at 53e6ce1 (2017-01-11)
ENV REV=53e6ce116135b80d037921a7fdd5138cf32d7a8a
RUN go get -d github.com/google/go-querystring/query &&\
    (cd /go/src/github.com/google/go-querystring && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/googleapis/gax-go at 84ed267 (2017-06-10)
ENV REV=84ed26760e7f6f80887a2fbfb50db3cc415d2cea
RUN go get -d github.com/googleapis/gax-go &&\
    (cd /go/src/github.com/googleapis/gax-go && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo github.com/gregjones/httpcache at 0d2297f (2017-04-24)
ENV REV=0d2297f241a3503b4d464cd434e6d1490ec76e9a
RUN go get -d github.com/gregjones/httpcache &&\
    (cd /go/src/github.com/gregjones/httpcache && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo go4.org at 034d17a (2017-05-25)
ENV REV=034d17a462f7b2dcd1a4a73553ec5357ff6e6c6e
RUN go get -d go4.org/types &&\
    (cd /go/src/go4.org && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/crypto at 94c6142 (2017-07-20)
ENV REV=94c6142ae57b8dc154f6e1813c921a6c85f505cd
RUN go get -d golang.org/x/crypto/acme `#and 2 other pkgs` &&\
    (cd /go/src/golang.org/x/crypto && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/net at ab54850 (2017-07-21)
ENV REV=ab5485076ff3407ad2d02db054635913f017b0ed
RUN go get -d golang.org/x/net/context `#and 8 other pkgs` &&\
    (cd /go/src/golang.org/x/net && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/oauth2 at b53b38a (2017-07-19)
ENV REV=b53b38ad8a6435bd399ea76d0fa74f23149cca4e
RUN go get -d golang.org/x/oauth2 `#and 5 other pkgs` &&\
    (cd /go/src/golang.org/x/oauth2 && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/sync at 457c582 (2016-07-15)
ENV REV=457c5828408160d6a47e17645169cf8fa20218c4
RUN go get -d golang.org/x/sync/errgroup &&\
    (cd /go/src/golang.org/x/sync && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo golang.org/x/text at 836efe4 (2017-07-14)
ENV REV=836efe42bb4aa16aaa17b9c155d8813d336ed720
RUN go get -d golang.org/x/text/secure/bidirule `#and 4 other pkgs` &&\
    (cd /go/src/golang.org/x/text && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo google.golang.org/api at 295e4bb (2017-07-18)
ENV REV=295e4bb0ade057ae2cfb9876ab0b54635dbfcea4
RUN go get -d google.golang.org/api/gensupport `#and 9 other pkgs` &&\
    (cd /go/src/google.golang.org/api && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo google.golang.org/genproto at b0a3dcf (2017-07-12)
ENV REV=b0a3dcfcd1a9bd48e63634bd8802960804cf8315
RUN go get -d google.golang.org/genproto/googleapis/api/annotations `#and 3 other pkgs` &&\
    (cd /go/src/google.golang.org/genproto && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo google.golang.org/grpc at 0c41876 (2017-07-21)
ENV REV=0c41876308d45bc82e587965971e28be659a1aca
RUN go get -d google.golang.org/grpc `#and 14 other pkgs` &&\
    (cd /go/src/google.golang.org/grpc && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

# Repo grpc.go4.org at 11d0a25 (2017-06-09)
ENV REV=11d0a25b491971beb5a4625ea7856a3c4afaafa5
RUN go get -d grpc.go4.org `#and 11 other pkgs` &&\
    (cd /go/src/grpc.go4.org && (git cat-file -t $REV 2>/dev/null || git fetch -q origin $REV) && git reset --hard $REV)

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
	google.golang.org/grpc/codes \
	google.golang.org/grpc/credentials \
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
# END deps

COPY . /go/src/golang.org/x/build/

RUN go install -ldflags "-linkmode=external -extldflags '-static -pthread'" golang.org/x/build/maintner/maintnerd
