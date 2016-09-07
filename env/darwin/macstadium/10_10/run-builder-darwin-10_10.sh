#!/bin/bash

set -e

url="https://storage.googleapis.com/go-builder-data/buildlet.darwin-amd64.gz"
while ! curl -f -o buildlet.gz "$url"; do
    echo
    echo "curl failed to fetch $url"
    echo "Sleeping before retrying..."
    sleep 5
done

set -x
gunzip -f buildlet.gz
chmod +x buildlet

export GO_BUILDER_ENV=macstadium_vm
while true; do ./buildlet || sleep 5; done

