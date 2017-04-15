# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

FROM golang:1.8-wheezy
LABEL maintainer "golang-dev@googlegroups.com"

# You can verify this list with prdeps:
# go get github.com/davecheney/prdeps
# prdeps -d 1 golang.org/x/build/cmd/coordinator

RUN go get -d cloud.google.com/go/compute/metadata cloud.google.com/go/datastore cloud.google.com/go/storage
RUN cd /go/src/cloud.google.com/go && git reset --hard cd0da878c66091060d2e7403abd62192b3e387e0

RUN go get -d golang.org/x/time/rate
RUN cd /go/src/golang.org/x/time/rate && git reset --hard f51c12702a4d776e4c1fa9b0fabab841babae631

RUN go get -d golang.org/x/oauth2 golang.org/x/oauth2/google
RUN cd /go/src/golang.org/x/oauth2 && git reset --hard 314dd2c0bf3ebd592ec0d20847d27e79d0dbe8dd

RUN go get -d golang.org/x/crypto/acme/autocert
RUN cd /go/src/golang.org/x/crypto && git reset --hard 573951cbe80bb6352881271bb276f48749eab6f4

RUN go get -d go4.org/syncutil
RUN cd /go/src/go4.org && git reset --hard 7ce08ca145dbe0e66a127c447b80ee7914f3e4f9

RUN go get -d golang.org/x/net/context
RUN cd /go/src/golang.org/x/net && git reset --hard f2499483f923065a842d38eb4c7f1927e6fc6e6d

RUN go get -d google.golang.org/api/container/v1 google.golang.org/api/compute/v1 google.golang.org/api/googleapi
RUN cd /go/src/google.golang.org/api && git reset --hard dfa61ae24628a06502b9c2805d983b57e89399b5

RUN go get -d google.golang.org/genproto/googleapis/type/latlng
RUN cd /go/src/google.golang.org/genproto  && git reset --hard b3e7c2fb04031add52c4817f53f43757ccbf9c18
RUN go get -d google.golang.org/genproto/googleapis/datastore/v1

RUN go get -d gopkg.in/inf.v0
RUN cd /go/src/gopkg.in/inf.v0 && git reset --hard 3887ee99ecf07df5b447e9b00d9c0b2adaa9f3e4

RUN cd /go/src/google.golang.org/grpc && git reset --hard 50955793b0183f9de69bd78e2ec251cf20aab121

COPY . /go/src/golang.org/x/build/

RUN go install golang.org/x/build/cmd/coordinator

ENTRYPOINT ["/go/bin/coordinator"]
