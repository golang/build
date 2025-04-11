# Copyright 2014 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang/buildlet-stage0 AS stage0

FROM debian:sid
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive

# gdb: optionally used by runtime tests for gdb
# strace: optionally used by some net/http tests
# gcc libc6-dev: for building Go's bootstrap 'dist' prog
# gfortran: for compiling cgo with fortran support (multilib for 386)
# libc6-dev-i386 gcc-multilib: for 32-bit builds
# procps lsof psmisc: misc basic tools
# libgles2-mesa-dev libopenal-dev fonts-droid-fallback: required by x/mobile repo
# upx: executable compressor, needed to test for go binaries compatibility
# qemu-user: QEMU (machine emulator and virtualizer) user-space emulation
# python3.12: for bootstrapswarm
# nano: for interactive debugging convenience
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	libcrypt1 \
	ca-certificates \
	curl \
	gdb \
	strace \
	gcc \
	libc6-dev \
	gfortran \
	gfortran-multilib \
	libc6-dev-i386 \
	gcc-multilib \
	procps \
	lsof \
	psmisc \
	libgles2-mesa-dev \
	libopenal-dev \
	fonts-droid-fallback \
	upx \
	qemu-user \
	netbase \
	openssh-server \
	make \
	sudo \
	iproute2 \
	python3.12 \
	nano \
	&& rm -rf /var/lib/apt/lists/*

# bootstrapswarming requires a 'python3' binary in PATH, so add one that points to the python3.12 installed above.
#
# Note: python3.12 is installed because python3 gets python 3.13, and as of 2025-04-16 swarming bot wasn't compatible
# with it due to reliance on a deprecated 'pipes' module:
#
#	2025-04-16 00:10:05.187 E: Failed to run start_bot
#	Traceback (most recent call last):
#	  File "/home/swarming/.swarming/swarming_bot.1.zip/__main__.py", line 324, in main
#	    return fn(args)
#	  File "/home/swarming/.swarming/swarming_bot.1.zip/__main__.py", line 194, in CMDstart_bot
#	    from bot_code import bot_main
#	  File "/home/swarming/.swarming/swarming_bot.1.zip/bot_code/bot_main.py", line 47, in <module>
#	    from api import bot
#	  File "/home/swarming/.swarming/swarming_bot.1.zip/api/bot.py", line 20, in <module>
#	    from api import os_utilities
#	  File "/home/swarming/.swarming/swarming_bot.1.zip/api/os_utilities.py", line 24, in <module>
#	    import pipes
#	ModuleNotFoundError: No module named 'pipes'
#
# Once swarming bot is updated to be compatible with the latest python3 version, this can be removed
# and the python3 package can be installed.
RUN ln -s /usr/bin/python3.12 /usr/local/bin/python3

RUN mkdir -p /go1.4-amd64 \
	&& ( \
		curl --silent https://storage.googleapis.com/golang/go1.4.3.linux-amd64.tar.gz | tar -C /go1.4-amd64 -zxv \
	) \
	&& mv /go1.4-amd64/go /go1.4 \
	&& rm -rf /go1.4-amd64 \
	&& rm -rf /go1.4/pkg/linux_amd64_race \
		/go1.4/api \
		/go1.4/blog \
		/go1.4/doc \
		/go1.4/misc \
		/go1.4/test \
	&& find /go1.4 -type d -name testdata | xargs rm -rf

COPY --from=stage0 /go/bin/* /usr/local/bin/

CMD ["/usr/local/bin/run-worker.sh"]
