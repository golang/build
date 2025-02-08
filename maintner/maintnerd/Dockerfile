# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.23-bookworm AS build
LABEL maintainer="golang-dev@googlegroups.com"

RUN mkdir /gocache
ENV GOCACHE /gocache

COPY go.mod /go/src/golang.org/x/build/go.mod
COPY go.sum /go/src/golang.org/x/build/go.sum

WORKDIR /go/src/golang.org/x/build

# Optimization for iterative docker build speed, not necessary for correctness:
# TODO: write a tool to make writing Go module-friendly Dockerfiles easier.
RUN go install cloud.google.com/go/compute/metadata
RUN go install cloud.google.com/go/storage
RUN go install golang.org/x/crypto/acme/autocert
RUN go install golang.org/x/net/http2
RUN go install golang.org/x/time/rate
RUN go install google.golang.org/grpc

COPY . /go/src/golang.org/x/build/

RUN go install golang.org/x/build/maintner/maintnerd


FROM debian:bookworm
LABEL maintainer="golang-dev@googlegroups.com"

# For interacting with the Go source & subrepos
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	ca-certificates \
	dirmngr \
	git-core \
	gnupg \
	netbase \
	openssh-client \
	tini \
	&& rm -rf /var/lib/apt/lists/*

# Add Github.com's known_hosts entries, so git push calls later don't
# prompt, and don't need to have their strict host key checking
# disabled.
RUN mkdir -p ~/.ssh/ \
	&& chmod 0700 ~/.ssh/ \
	&& echo "|1|SFEvEAqYsJ18JCr+0iV4GtlwS4w=|P6oCZUUd/5t9pH4Om7ShlfltRyE= ssh-rsa AAAAB3NzaC1yc2EAAAABIwAAAQEAq2A7hRGmdnm9tUDbO9IDSwBK6TbQa+PXYPCPy6rbTrTtw7PHkccKrpp0yVhp5HdEIcKr6pLlVDBfOLX9QUsyCOV0wzfjIJNlGEYsdlLJizHhbn2mUjvSAHQqZETYP81eFzLQNnPHt4EVVUh7VfDESU84KezmD5QlWpXLmvU31/yMf+Se8xhHTvKSCZIFImWwoG6mbUoWf9nzpIoaSjB+weqqUUmpaaasXVal72J+UX2B+2RPW3RcT0eOzQgqlJL3RKrTJvdsjE3JEAvGq3lGHSZXy28G3skua2SmVi/w4yCE6gbODqnTWlg7+wC604ydGXA8VJiS5ap43JXiUFFAaQ==" > ~/.ssh/known_hosts \
	&& echo "|1|HygGkfOGLovavKfixjXWFJ7Yk1I=|lb/724row8KDTMC1dZiJlHyjxWM= ssh-rsa AAAAB3NzaC1yc2EAAAABIwAAAQEAq2A7hRGmdnm9tUDbO9IDSwBK6TbQa+PXYPCPy6rbTrTtw7PHkccKrpp0yVhp5HdEIcKr6pLlVDBfOLX9QUsyCOV0wzfjIJNlGEYsdlLJizHhbn2mUjvSAHQqZETYP81eFzLQNnPHt4EVVUh7VfDESU84KezmD5QlWpXLmvU31/yMf+Se8xhHTvKSCZIFImWwoG6mbUoWf9nzpIoaSjB+weqqUUmpaaasXVal72J+UX2B+2RPW3RcT0eOzQgqlJL3RKrTJvdsjE3JEAvGq3lGHSZXy28G3skua2SmVi/w4yCE6gbODqnTWlg7+wC604ydGXA8VJiS5ap43JXiUFFAaQ==" >> ~/.ssh/known_hosts \
	&& chmod 0600 ~/.ssh/known_hosts

COPY --from=build /go/bin/maintnerd /
ENTRYPOINT ["/usr/bin/tini", "--", "/maintnerd"]
