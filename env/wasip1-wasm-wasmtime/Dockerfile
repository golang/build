# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

ARG REPO

FROM debian:latest as builder
LABEL maintainer="golang-dev@googlegroups.com"

RUN apt-get update && apt-get -y install curl xz-utils

# A copy of https://wasmtime.dev/install.sh.
COPY install.sh install.sh

RUN bash install.sh --version v9.0.1

FROM ${REPO}/linux-x86-sid:20221109

COPY --from=builder /root/.wasmtime/bin/wasmtime /usr/local/bin/wasmtime

CMD ["/usr/local/bin/stage0"]
