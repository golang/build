# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

ARG REPO

FROM debian:latest as builder
LABEL maintainer="golang-dev@googlegroups.com"

RUN apt-get update && apt-get -y install python3 git

# A copy of https://raw.githubusercontent.com/WasmEdge/WasmEdge/master/utils/install.py.
COPY install.py install.py

RUN python3 install.py --version 0.12.0

FROM ${REPO}/linux-x86-sid:20221109

COPY --from=builder /root/.wasmedge/bin/wasmedge /usr/local/bin/wasmedge
COPY --from=builder /root/.wasmedge/lib/* /usr/local/lib/

ENV LD_LIBRARY_PATH=/usr/local/lib

CMD ["/usr/local/bin/stage0"]
