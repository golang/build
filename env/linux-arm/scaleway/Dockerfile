# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.13 AS build
RUN go get golang.org/x/build/cmd/buildlet/stage0

FROM debian:buster

RUN apt-get update

RUN apt-get install --yes \
          gcc strace procps psmisc libc6-dev curl netbase \
          openssh-server

RUN curl -L https://golang.org/dl/go1.13.4.linux-armv6l.tar.gz | \
    tar xzv  -C /usr/local && /usr/local/go/bin/go version

ENV GOROOT_BOOTSTRAP=/usr/local/go

# compiled stage0 binary must be in working dir
COPY --from=build /go/bin/stage0 /usr/local/bin/stage0

CMD ["/usr/local/bin/stage0"]
