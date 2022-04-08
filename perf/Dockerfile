# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.18 AS builder

COPY go.mod /app/go.mod
COPY go.sum /app/go.sum

WORKDIR /app

RUN go mod download

COPY . /app
RUN go build -o perf golang.org/x/build/perf

FROM debian:bullseye

COPY --from=builder /app/perf /
ENTRYPOINT ["/perf"]
