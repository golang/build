#!/bin/bash

# See Makefile for usage

set -e

USER_AT_HOST=$1
if [ "$USER_AT_HOST" = "" ]; then
        echo "Missing user@host arg; see Makefile for usage" >&2
        exit 2
fi
HOST_TYPE=$2
if [ "$HOST_TYPE" = "" ]; then
        echo "Missing host type arg; see Makefile for usage" >&2
        exit 2
fi

GOARCH=ppc64le GOOS=linux go build -o rundockerbuildlet.ppc64le golang.org/x/build/cmd/rundockerbuildlet

rsync -e "ssh -i ~/.ssh/id_ed25519_golang1" -avPW ./ $USER_AT_HOST:./
scp -i ~/.ssh/id_ed25519_golang1 $HOME/keys/${HOST_TYPE}.buildkey $USER_AT_HOST:.gobuildkey

# Install Docker, including adding our username to the "docker" group:
ssh -i ~/.ssh/id_ed25519_golang1 $USER_AT_HOST ./install-docker.bash

# Now that we have Docker, "log in" again (with access to the docker
# group) and do the rest:
ssh -i ~/.ssh/id_ed25519_golang1 $USER_AT_HOST ./install-buildlet.bash $HOST_TYPE


