# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.27-trixie AS build

COPY go.mod /app/go.mod
COPY go.sum /app/go.sum

WORKDIR /app

RUN go mod download

COPY . /app

# Disable cgo because the influxdb2 container image may have an older version
# of glibc.
ENV CGO_ENABLED=0

RUN go build -o run-influx golang.org/x/build/cmd/influx

FROM marketplace.gcr.io/google/influxdb2:latest

# Upgrade OS packages because marketplace.gcr.io/google/influxdb2:latest
# has not been updated recently and includes vulnerable Debian 12 packages.
RUN apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/*

COPY --from=build /app/run-influx /run-influx

ENTRYPOINT ["/bin/sh"]
CMD ["-c", "/run-influx -listen-http=:80"]
