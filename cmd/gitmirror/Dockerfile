# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.17 AS build
LABEL maintainer="golang-dev@googlegroups.com"

RUN mkdir /gocache
ENV GOCACHE /gocache

COPY go.mod /go/src/golang.org/x/build/go.mod
COPY go.sum /go/src/golang.org/x/build/go.sum

WORKDIR /go/src/golang.org/x/build

# Download module dependencies to improve speed of re-building the
# Docker image during minor code changes.
RUN go mod download

COPY . /go/src/golang.org/x/build/

RUN go install golang.org/x/build/cmd/gitmirror

FROM debian:buster
LABEL maintainer="golang-dev@googlegroups.com"

# For interacting with the Go source & subrepos
RUN apt-get update && apt-get install -y \
	--no-install-recommends \
	ca-certificates \
	git-core \
	openssh-client \
	gnupg dirmngr \
	curl tini \
	&& rm -rf /var/lib/apt/lists/*

# Install gcloud for auth to CSR, see https://cloud.google.com/sdk/docs/install#deb
RUN echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] http://packages.cloud.google.com/apt cloud-sdk main" | tee -a /etc/apt/sources.list.d/google-cloud-sdk.list && curl https://packages.cloud.google.com/apt/doc/apt-key.gpg | apt-key --keyring /usr/share/keyrings/cloud.google.gpg  add - && apt-get update -y && apt-get install google-cloud-sdk -y && rm -rf /var/lib/apt/lists/*

# Add github.com's known_hosts entries, so git push calls later don't
# prompt, and don't need to have their strict host key checking
# disabled.
RUN mkdir -p ~/.ssh/ \
	&& chmod 0700 ~/.ssh/ \
	&& echo "|1|SFEvEAqYsJ18JCr+0iV4GtlwS4w=|P6oCZUUd/5t9pH4Om7ShlfltRyE= ssh-rsa AAAAB3NzaC1yc2EAAAABIwAAAQEAq2A7hRGmdnm9tUDbO9IDSwBK6TbQa+PXYPCPy6rbTrTtw7PHkccKrpp0yVhp5HdEIcKr6pLlVDBfOLX9QUsyCOV0wzfjIJNlGEYsdlLJizHhbn2mUjvSAHQqZETYP81eFzLQNnPHt4EVVUh7VfDESU84KezmD5QlWpXLmvU31/yMf+Se8xhHTvKSCZIFImWwoG6mbUoWf9nzpIoaSjB+weqqUUmpaaasXVal72J+UX2B+2RPW3RcT0eOzQgqlJL3RKrTJvdsjE3JEAvGq3lGHSZXy28G3skua2SmVi/w4yCE6gbODqnTWlg7+wC604ydGXA8VJiS5ap43JXiUFFAaQ==" > ~/.ssh/known_hosts \
	&& echo "|1|HygGkfOGLovavKfixjXWFJ7Yk1I=|lb/724row8KDTMC1dZiJlHyjxWM= ssh-rsa AAAAB3NzaC1yc2EAAAABIwAAAQEAq2A7hRGmdnm9tUDbO9IDSwBK6TbQa+PXYPCPy6rbTrTtw7PHkccKrpp0yVhp5HdEIcKr6pLlVDBfOLX9QUsyCOV0wzfjIJNlGEYsdlLJizHhbn2mUjvSAHQqZETYP81eFzLQNnPHt4EVVUh7VfDESU84KezmD5QlWpXLmvU31/yMf+Se8xhHTvKSCZIFImWwoG6mbUoWf9nzpIoaSjB+weqqUUmpaaasXVal72J+UX2B+2RPW3RcT0eOzQgqlJL3RKrTJvdsjE3JEAvGq3lGHSZXy28G3skua2SmVi/w4yCE6gbODqnTWlg7+wC604ydGXA8VJiS5ap43JXiUFFAaQ==" >> ~/.ssh/known_hosts \
	&& chmod 0600 ~/.ssh/known_hosts

COPY --from=build /go/bin/gitmirror /
ENTRYPOINT ["/usr/bin/tini", "--", "/gitmirror"]
