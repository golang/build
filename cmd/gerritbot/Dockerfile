# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
FROM alpine
LABEL maintainer "golang-dev@googlegroups.com"
RUN apk add --no-cache git curl
RUN git config --global user.email "letsusegerrit@gmail.com"
RUN git config --global user.name "GerritBot"
RUN git config --global http.cookiefile "/gitcookies"
COPY gerritbot /
ENTRYPOINT ["/gerritbot"]
