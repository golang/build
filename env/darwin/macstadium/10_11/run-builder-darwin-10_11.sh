#!/bin/bash

set -e

# Add unix timestamp to the end to bust caches, otherwise GCS caches aggressively if you
# accidentally upload a buildlet with caching enabled once. Ideally this script
# would be replaced with a Go program that only did this with HEAD and then only fetched
# the full blob if the Last-Modified or ETag had changed from before. But network is cheap.
url="https://storage.googleapis.com/go-builder-data/buildlet.darwin-amd64.gz?$(date +%s)"
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
