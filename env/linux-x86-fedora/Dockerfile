# Copyright 2019 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang/buildlet-stage0 AS stage0

FROM fedora:30
MAINTAINER golang-dev <golang-dev@googlegroups.com>

RUN yum -y update && yum -y install \
    ca-certificates \
    findutils \
    gcc \
    glibc-static \
    patch \
    strace \
    which \
    openssh-server \
    && true

RUN ssh-keygen -A

RUN mkdir -p /goboot-amd64 \
	&& ( \
		curl --silent https://storage.googleapis.com/golang/go1.13.4.linux-amd64.tar.gz | tar -C /goboot-amd64 -zxv \
	) \
	&& mv /goboot-amd64/go /goboot \
	&& rm -rf /goboot-amd64 \
	&& rm -rf /goboot/pkg/linux_amd64_race \
		/goboot/api \
		/goboot/blog \
		/goboot/doc \
		/goboot/misc \
		/goboot/test \
	&& find /goboot -type d -name testdata | xargs rm -rf

COPY --from=stage0 /go/bin/stage0 /usr/local/bin/stage0

CMD ["/usr/local/bin/stage0"]
