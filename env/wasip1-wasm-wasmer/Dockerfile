# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

ARG REPO

FROM debian:latest as builder
LABEL maintainer="golang-dev@googlegroups.com"

RUN apt-get update && apt-get -y install curl

# A copy of https://raw.githubusercontent.com/wasmerio/wasmer-install/master/install.sh.
COPY install.sh install.sh

RUN bash install.sh v3.3.0

FROM ${REPO}/linux-x86-sid:20221109

COPY --from=builder /root/.wasmer/bin/wasmer /usr/local/bin/wasmer

CMD ["/usr/local/bin/stage0"]
