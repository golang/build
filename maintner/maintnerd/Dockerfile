# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
FROM golang:1.8
LABEL maintainer "golang-dev@googlegroups.com"

# TODO (jessfraz): change this, gitmirror, and coordinator to have minimal
# builds with a nested build Dockerfile/Makefile

RUN go get -d golang.org/x/oauth2
RUN cd /go/src/golang.org/x/oauth2 && git reset --hard 314dd2c0bf3ebd592ec0d20847d27e79d0dbe8dd

RUN go get -d golang.org/x/net/context
RUN cd /go/src/golang.org/x/net && git reset --hard f2499483f923065a842d38eb4c7f1927e6fc6e6d

RUN go get -d github.com/google/go-github/github
RUN cd /go/src/github.com/google/go-github && git reset --hard 6896997c7c9fe603fb9d2e8e92303bb18481e60a

RUN go get -d github.com/gregjones/httpcache
RUN cd /go/src/github.com/gregjones/httpcache && git reset --hard d02018f006d98f58512bf3adfc171d88d17626df

RUN go get -d go4.org/types
RUN cd /go/src/go4.org && git reset --hard 7ce08ca145dbe0e66a127c447b80ee7914f3e4f9

RUN go get -d golang.org/x/sync/errgroup
RUN cd /go/src/golang.org/x/sync && git reset --hard 5a06fca2c336a4b2b2fcb45702e8c47621b2aa2c

RUN go get -d github.com/golang/protobuf/proto
RUN cd /go/src/github.com/golang/protobuf && git reset --hard 2bba0603135d7d7f5cb73b2125beeda19c09f4ef

COPY . /go/src/golang.org/x/build/

RUN go install golang.org/x/build/maintner/maintnerd

ENTRYPOINT ["/go/bin/maintnerd"]
