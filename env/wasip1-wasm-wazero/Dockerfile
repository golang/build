# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

ARG REPO

FROM golang:latest as builder
LABEL maintainer="golang-dev@googlegroups.com"

RUN go install github.com/tetratelabs/wazero/cmd/wazero@v1.3.1

FROM ${REPO}/linux-x86-sid:20221109

COPY --from=builder /go/bin/wazero /usr/local/bin/wazero

CMD ["/usr/local/bin/stage0"]
