# Copyright 2022 Go Authors All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM marketplace.gcr.io/google/debian11:latest

RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	wget

RUN set -eux; \
	url='https://dl.google.com/go/go1.19.2.linux-amd64.tar.gz'; \
	sha256='5e8c5a74fe6470dd7e055a461acda8bb4050ead8c2df70f227e3ff7d8eb7eeb6'; \
	\
	wget -O go.tgz "$url" --progress=dot:giga; \
	echo "$sha256 *go.tgz" | sha256sum -c -; \
	\
	tar -C /usr/local -xzf go.tgz; \
	rm go.tgz

ENV GOPATH /go
RUN mkdir -p "$GOPATH/src" "$GOPATH/bin" && chmod -R 777 "$GOPATH"
ENV PATH /usr/local/go/bin:$GOPATH/bin:$PATH

COPY go.mod /app/go.mod
COPY go.sum /app/go.sum

WORKDIR /app

RUN go mod download

COPY . /app

RUN go build golang.org/x/build/cmd/securitybot

RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	tini

ARG PORT=8080
ENV PORT=${PORT}
EXPOSE ${PORT}

WORKDIR /app
ENTRYPOINT ["/usr/bin/tini", "--", "./securitybot", "-gcs=stb-logs"]