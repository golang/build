#!/bin/bash

set -e
set -x

HOST_TYPE=$1
if [ "$HOST_TYPE" = "" ]; then
        echo "Missing host type arg; this file is not supposed to be run directly; see Makefile for usage" >&2
        exit 2
fi

sudo mv .gobuildkey /etc/gobuild.key
sudo install rundockerbuildlet.ppc64le /usr/local/bin/rundockerbuildlet

curl -o stage0 https://storage.googleapis.com/go-builder-data/buildlet-stage0.linux-ppc64le
chmod +x stage0
docker build -t golang/builder .

sed "s/env=XXX/env=$HOST_TYPE/" rundockerbuildlet.service > rundockerbuildlet.service.expanded
sudo cp rundockerbuildlet.service.expanded /etc/systemd/user/rundockerbuildlet.service
sudo systemctl enable /etc/systemd/user/rundockerbuildlet.service || true
sudo systemctl daemon-reload || true
sudo systemctl restart docker.service
sudo systemctl restart rundockerbuildlet.service
