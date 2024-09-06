# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.23-bookworm AS build

COPY go.mod /app/go.mod
COPY go.sum /app/go.sum

WORKDIR /app

RUN go mod download

COPY . /app
RUN go build -o perf golang.org/x/build/perf

FROM debian:bookworm

# netbase and ca-certificates are needed for dialing TLS.
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	netbase \
	ca-certificates \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /app/perf /
ENTRYPOINT ["/perf"]
