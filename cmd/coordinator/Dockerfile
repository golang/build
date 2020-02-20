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
RUN go install \
    cloud.google.com/go/compute/metadata \
    cloud.google.com/go/datastore \
    cloud.google.com/go/errorreporting \
    cloud.google.com/go/monitoring/apiv3 \
    cloud.google.com/go/storage \
    github.com/gliderlabs/ssh \
    github.com/golang/protobuf/ptypes \
    github.com/kr/pty \
    go4.org/syncutil \
    golang.org/x/crypto/acme/autocert \
    golang.org/x/crypto/ssh \
    golang.org/x/oauth2 \
    golang.org/x/oauth2/google \
    golang.org/x/perf/storage \
    golang.org/x/time/rate \
    google.golang.org/api/compute/v1 \
    google.golang.org/api/container/v1 \
    google.golang.org/api/googleapi \
    google.golang.org/genproto/googleapis/api/metric \
    google.golang.org/genproto/googleapis/monitoring/v3 \
    gopkg.in/inf.v0 \
    grpc.go4.org \
    && true

# Makefile passes a string with --build-arg version
# This becomes part of the cache key for all subsequent instructions,
# so it must not be placed above the "go install" commands above.
ARG version=unknown

# TODO: ideally we'd first copy all of x/build here EXCEPT
# cmd/coordinator, then build x/build/..., and *then* COPY in the
# cmd/coordinator files and then build the final binary. Currently we
# do too much building of x/build/foo stuff when just modifying
# cmd/coordinator/*.go files.

COPY . /go/src/golang.org/x/build/

RUN go install -ldflags "-X 'main.Version=$version'" golang.org/x/build/cmd/coordinator


FROM debian:stretch AS build_drawterm

RUN apt-get update && apt-get install -y --no-install-recommends \
    git-core ca-certificates make gcc libc6-dev libx11-dev

# drawterm connects to plan9 instances like:
#    echo glenda123 | ./drawterm -a <addr> -c <addr> -u glenda -k user=glenda
# Where <addr> is the IP address of the Plan 9 instance on GCE,
# "glenda" is the username and "glenda123" is the password.
RUN git clone https://github.com/0intro/conterm /tmp/conterm && \
    cd /tmp/conterm && \
    CONF=unix make && mv /tmp/conterm/drawterm /usr/local/bin && \
    rm -rf /tmp/conterm


FROM debian:stretch

# openssh client is for the gomote ssh proxy client.
# telnet is for the gomote ssh proxy to windows. (no ssh server there)
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	ca-certificates \
	openssh-client \
	telnet \
	&& rm -rf /var/lib/apt/lists/*


COPY --from=build /go/src/golang.org/x/build/cmd/coordinator/internal/dashboard/dashboard.html /dashboard.html
COPY --from=build /go/src/golang.org/x/build/cmd/coordinator/style.css /style.css
COPY --from=build /go/bin/coordinator /
COPY --from=build_drawterm /usr/local/bin/drawterm /usr/local/bin/

ENTRYPOINT ["/coordinator"]
