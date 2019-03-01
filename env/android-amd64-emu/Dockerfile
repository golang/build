# Copyright 2019 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang/buildlet-stage0 AS stage0

FROM debian:buster
MAINTAINER golang-dev <golang-dev@googlegroups.com>

ENV DEBIAN_FRONTEND noninteractive

ENV GO_BUILDER_ENV android-amd64-emu

# For gomobile tests
ENV ANDROID_HOME=/android/sdk
ENV CGO_CFLAGS=-I/android/openal-headers

ENV PATH="${PATH}:${ANDROID_HOME}/platform-tools:${ANDROID_HOME}/build-tools/27.0.3:/android/gradle/bin"
ENV CC_FOR_android_386=/android/sdk/ndk-bundle/toolchains/llvm/prebuilt/linux-x86_64/bin/i686-linux-android26-clang
ENV CC_FOR_android_amd64=/android/sdk/ndk-bundle/toolchains/llvm/prebuilt/linux-x86_64/bin/x86_64-linux-android26-clang

# gdb: optionally used by runtime tests for gdb
# strace: optionally used by some net/http tests
# gcc libc6-dev: for building Go's bootstrap 'dist' prog
# libc6-dev-i386 gcc-multilib: for 32-bit builds
# procps lsof psmisc: misc basic tools
# libgles2-mesa-dev libopenal-dev fonts-noto: required by x/mobile repo
# unzip openjdk-8-jdk python lib32z1: required by the Android SDK
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	ca-certificates \
	curl \
	gdb \
	strace \
	gcc \
	libc6-dev \
	libc6-dev-i386 \
	gcc-multilib \
	procps \
	lsof \
	psmisc \
	libgles2-mesa-dev \
	libopenal-dev \
	fonts-noto \
	fonts-noto-mono \
	openssh-server \
	unzip \
	openjdk-8-jdk \
	python \
	lib32z1 \
	&& rm -rf /var/lib/apt/lists/*

RUN mkdir -p /go1.4-amd64 \
	&& ( \
		curl --silent https://storage.googleapis.com/golang/go1.4.linux-amd64.tar.gz | tar -C /go1.4-amd64 -zxv \
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
RUN mkdir -p /android/sdk \
	&& curl -o /android/sdk/sdk-tools-linux.zip https://dl.google.com/android/repository/sdk-tools-linux-3859397.zip \
	&& unzip -d /android/sdk /android/sdk/sdk-tools-linux.zip \
	&& rm -rf /android/sdk/sdk-tools-linux.zip

RUN yes | /android/sdk/tools/bin/sdkmanager --licenses \
	&& /android/sdk/tools/bin/sdkmanager ndk-bundle "system-images;android-26;default;x86_64" \
	&& /android/sdk/tools/bin/sdkmanager "build-tools;21.1.2" "platforms;android-26" \
	&& /android/sdk/tools/bin/sdkmanager --update

# Gradle for gomobile
RUN curl -L -o /android/gradle-5.2.1-bin.zip https://services.gradle.org/distributions/gradle-5.2.1-bin.zip \
	&& unzip -d /android /android/gradle-5.2.1-bin.zip \
	&& rm /android/gradle-5.2.1-bin.zip \
	&& mv /android/gradle-5.2.1 /android/gradle

# Create emulator
RUN echo no | /android/sdk/tools/bin/avdmanager create avd --force --name android-avd --package "system-images;android-26;default;x86_64"

RUN mkdir /android/openal-headers
RUN cp -a /usr/include/AL /android/openal-headers/

COPY --from=stage0 /go/bin/stage0 /usr/local/bin/stage0

CMD ["/usr/local/bin/stage0"]
