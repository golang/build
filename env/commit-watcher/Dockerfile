# Copyright 2014 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Commit watcher for Go repos.

FROM debian:wheezy
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive

ADD /scripts/install-apt-deps.sh /scripts/
RUN /scripts/install-apt-deps.sh

ADD /scripts/build-commit-watcher.sh /scripts/
# Note that WATCHER_REV must be full for "git fetch origin $REV" later:
RUN GO_REV=go1.4 TOOLS_REV=a54d0066172 WATCHER_REV=1b1e086fd18435e4ea46287fed6c85f268f47bf7 /scripts/build-commit-watcher.sh && test -f /usr/local/bin/watcher
