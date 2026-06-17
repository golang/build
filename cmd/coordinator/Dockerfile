# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.25-trixie AS build
LABEL maintainer="golang-dev@googlegroups.com"

RUN mkdir /gocache
ENV GOCACHE /gocache

COPY go.mod /go/src/golang.org/x/build/go.mod
COPY go.sum /go/src/golang.org/x/build/go.sum

WORKDIR /go/src/golang.org/x/build

# Download module dependencies to improve speed of re-building the
# Docker image during minor code changes.
RUN go mod download

# Makefile passes a string with --build-arg version
# This becomes part of the cache key for all subsequent instructions,
# so it must not be placed above the "go mod download" command above.
ARG version=unknown

# TODO: ideally we'd first copy all of x/build here EXCEPT
# cmd/coordinator, then build x/build/..., and *then* COPY in the
# cmd/coordinator files and then build the final binary. Currently we
# do too much building of x/build/foo stuff when just modifying
# cmd/coordinator/*.go files.

COPY . /go/src/golang.org/x/build/

RUN go install -ldflags "-X 'main.Version=$version'" golang.org/x/build/cmd/coordinator


FROM debian:trixie

RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	ca-certificates \
	&& rm -rf /var/lib/apt/lists/*


COPY --from=build /go/src/golang.org/x/build/cmd/coordinator/internal/dashboard/dashboard.html /dashboard.html
COPY --from=build /go/src/golang.org/x/build/cmd/coordinator/style.css /style.css
COPY --from=build /go/bin/coordinator /

ENTRYPOINT ["/coordinator"]
