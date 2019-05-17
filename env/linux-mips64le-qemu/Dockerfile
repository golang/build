# Copyright 2019 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM debian:stretch
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive

RUN apt-get update && apt-get install -y \
	--no-install-recommends \
        qemu-system-mips \
        seabios \
        ipxe-qemu \
	netbase \
	ca-certificates \
	curl \
	gdb \
	strace \
	gcc \
	libc6-dev \
	gcc-multilib \
	procps \
	lsof \
	psmisc \
	openssh-server \
	git \
	iptables \
	&& rm -rf /var/lib/apt/lists/*

RUN mkdir -p /var/lib/qemu
WORKDIR /var/lib/qemu

RUN curl -O https://storage.googleapis.com/go-builder-data/qemu/mips64le/vmlinux-4.9.0-9-5kc-malta
RUN curl -O https://storage.googleapis.com/go-builder-data/qemu/mips64le/initrd.img-4.9.0-9-5kc-malta
RUN curl -O https://storage.googleapis.com/go-builder-data/qemu/mips64le/hda-v1.img

CMD ["qemu-system-mips64el", "-M", "malta", "-cpu", "5KEc", "-m", "1024", \
    "-drive", "file=/var/lib/qemu/hda-v1.img,if=virtio,index=0", \
    "-device", "e1000-82545em,netdev=net0", \
    "-netdev", "user,id=net0,hostfwd=tcp:0.0.0.0:80-:80", \
    "-kernel", "/var/lib/qemu/vmlinux-4.9.0-9-5kc-malta", \
    "-initrd", "/var/lib/qemu/initrd.img-4.9.0-9-5kc-malta", \
    "-append", "console=ttyS0 append mem=256m@0x0 mem=768m@0x90000000 root=/dev/vda1 nokaslr", \
    "-serial", "mon:stdio", \
    "-nographic"]
