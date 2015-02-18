# Copyright 2015 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# gobuilders/linux-x86-nacl for 32- and 64-bit nacl.
#
# We need more modern libc than Debian stable as used in base, so we're
# using Debian sid instead.

FROM gobuilders/linux-x86-sid
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive

RUN (cd /usr/bin && curl -s -O https://storage.googleapis.com/gobuilder/sel_ldr_x86_32 && chmod +x sel_ldr_x86_32)
RUN (cd /usr/bin && curl -s -O https://storage.googleapis.com/gobuilder/sel_ldr_x86_64 && chmod +x sel_ldr_x86_64)

ADD go_nacl_386_exec /usr/bin/
ADD go_nacl_amd64p32_exec /usr/bin/
