#!/bin/bash

# Meant to be run under Docker.

if [ "$BUILDKEY_ARM" == "" ]; then
env
    echo "ERROR: BUILDKEY_ARM not set. (using docker run?)" >&2
    exit 1
fi
if [ "$BUILDKEY_ARM5" == "" ]; then
env
    echo "ERROR: BUILDKEY_ARM5 not set. (using docker run?)" >&2
    exit 1
fi


set -e
echo $BUILDKEY_ARM > /root/.gobuildkey-linux-arm
echo $BUILDKEY_ARM5 > /root/.gobuildkey-linux-arm-arm5
exec /gopath/bin/buildlet -reverse=linux-arm,linux-arm-arm5 -coordinator farmer.golang.org:443
