# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Note that OpenSSH 6.5+ is required for the Github SSH private key, which requires
# at least Debian Jessie (not Wheezy). This uses Jessie:
FROM golang:1.8
LABEL maintainer "golang-dev@googlegroups.com"

# TODO(bradfitz): remove duplication between this, gitmirror, and coordinator?
# Maybe make both derive FROM "golang_org:buildbase" or something. But
# then I want to make sure that FROM base is rebuilt always when things in
# x/build change.

COPY /cmd/gitmirror/install-apt-deps.sh /scripts/install-apt-deps.sh
RUN  /scripts/install-apt-deps.sh

RUN go get -d cloud.google.com/go/compute/metadata && \
    cd /go/src/cloud.google.com/go/compute/metadata && \
    git reset --hard cd0da878c66091060d2e7403abd62192b3e387e0

RUN go get -d golang.org/x/time/rate && \
    cd /go/src/golang.org/x/time/rate && \
    git reset --hard f51c12702a4d776e4c1fa9b0fabab841babae631

RUN go get -d golang.org/x/oauth2 && \
    cd /go/src/golang.org/x/oauth2 && \
    git reset --hard 314dd2c0bf3ebd592ec0d20847d27e79d0dbe8dd

RUN go get -d github.com/bradfitz/go-smtpd/smtpd && \
    cd /go/src/github.com/bradfitz/go-smtpd/smtpd && \
    git reset --hard deb6d623762522f8ad4a55b952001e4215a76cf4

RUN go get -d github.com/jellevandenhooff/dkim && \
    cd /go/src/github.com/jellevandenhooff/dkim && \
    git reset --hard f50fe3d243e1a9c9369eea29813517f3af190518

RUN go get -d golang.org/x/crypto/acme/autocert && \
    cd /go/src/golang.org/x/crypto/acme/autocert && \
    git reset --hard 573951cbe80bb6352881271bb276f48749eab6f4

RUN go get -d go4.org/types && \
    cd /go/src/go4.org && \
    git reset --hard d7e10a7752d55a1f81e384fe49a6a5b417a9a6dd

COPY . /go/src/golang.org/x/build/

RUN go install golang.org/x/build/cmd/pubsubhelper

ENTRYPOINT ["/go/bin/pubsubhelper"]
