# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.25-trixie AS build

COPY go.mod /app/go.mod
COPY go.sum /app/go.sum

WORKDIR /app

RUN go mod download

COPY . /app

# Disable cgo because the influxdb2 container image may have an older version
# of glibc.
ENV CGO_ENABLED=0

RUN go build -o run-influx golang.org/x/build/influx

FROM marketplace.gcr.io/google/influxdb2:latest

COPY --from=build /app/run-influx /run-influx

ENTRYPOINT ["/bin/sh"]
CMD ["-c", "/run-influx -listen-http=:80"]
