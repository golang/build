# Copyright 2019 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM ubuntu:20.04

ENV DEBIAN_FRONTEND noninteractive

RUN apt-get update && \
    apt-get install --yes \
          gcc gdb curl strace \
          ca-certificates netbase \
          procps lsof psmisc \
          openssh-server \
          patch

# For GOPPC64=power10 support. This hack can be removed once the bootstrap
# minimum reachs go1.20.
# P8 and P9 will use the default bootstrap toolchain downloaded during CI.
ADD p10.patch /
RUN mkdir -p /usr/local && \
    curl --silent -L https://go.dev/dl/go1.17.13.linux-ppc64le.tar.gz --output /go.tar.gz && \
    tar -C /usr/local/ -zxf /go.tar.gz && \
    mv /usr/local/go /usr/local/go-bootstrap && \
    tar -C /usr/local/ -zxf /go.tar.gz && \
    mv /usr/local/go /usr/local/go-tmp && \
    cd /usr/local/go-bootstrap && patch -p1 < /p10.patch && \
    cd /usr/local/go-bootstrap/src && GOROOT_BOOTSTRAP=/usr/local/go-tmp GOOS=linux GOARCH=ppc64le ./bootstrap.bash && \
    /usr/local/go-tmp/bin/go install golang.org/x/build/cmd/genbootstrap@v0.0.0-20221130230848-98eb5685978b && \
    cd /usr/local && /root/go/bin/genbootstrap -v -skip_build go-linux-ppc64le-bootstrap linux-ppc64le && \
    rm -rf /usr/local/go-tmp /usr/local/go-bootstrap /usr/local/go-linux-ppc64le-bootstrap /usr/local/go-linux-ppc64le-bootstrap.tbz && \
    mkdir /usr/local/go-bootstrap && tar -C /usr/local/go-bootstrap -xf /tmp/genbootstrap-*/gobootstrap-linux-ppc64le-go*.tar.gz && \
    rm -rf /usr/local/go-tmp /root/go /root/.cache /p10.patch /go.tar.gz /tmp/* && apt-get remove --yes patch

ENV GOROOT_BOOTSTRAP /usr/local/go-bootstrap
ADD stage0 /usr/local/bin/stage0

ENV GO_BUILD_KEY_DELETE_AFTER_READ true
ENV GO_BUILD_KEY_PATH /buildkey/gobuildkey

CMD ["/usr/local/bin/stage0"]
