# Copyright 2021 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.25-trixie AS build

COPY go.mod /app/go.mod
COPY go.sum /app/go.sum

WORKDIR /app

RUN go mod download

COPY . /app

RUN go build golang.org/x/build/cmd/relui

FROM debian:trixie

RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	tini ca-certificates git

ARG PORT=8080
ENV PORT=${PORT}
EXPOSE ${PORT}

COPY --from=build /app/relui /app/relui
WORKDIR /app
ENTRYPOINT ["/usr/bin/tini", "--", "./relui"]
