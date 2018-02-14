# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
FROM alpine
LABEL maintainer "golang-dev@googlegroups.com"
RUN apk add --no-cache git curl gnupg

# See https://github.com/golang/go/issues/23705
ENV TINI_VERSION v0.16.1
ADD https://github.com/krallin/tini/releases/download/${TINI_VERSION}/tini /tini
ADD https://github.com/krallin/tini/releases/download/${TINI_VERSION}/tini.asc /tini.asc
RUN gpg --keyserver hkp://p80.pool.sks-keyservers.net:80 --recv-keys 595E85A6B1B4779EA4DAAEC70B588DFF0527A9B7 \
  && gpg --verify /tini.asc
RUN chmod +x /tini

RUN git config --global user.email "letsusegerrit@gmail.com"
RUN git config --global user.name "GerritBot"
RUN git config --global http.cookiefile "/gitcookies"
COPY gerritbot /
ENTRYPOINT ["/tini", "--", "/gerritbot"]
