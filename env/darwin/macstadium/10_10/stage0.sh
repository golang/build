#!/bin/bash

# This is the file baked into the VM image.
#
# It fetches https://storage.googleapis.com/go-builder-data/run-builder-darwin-10_10.gz
# (which might be a shell script or an executable) and runs it to do the rest.

set -e
url="https://storage.googleapis.com/go-builder-data/run-builder-darwin-10_10.gz"
while ! curl -f -o run-builder.gz "$url"; do
    echo
    echo "curl failed to fetch $url"
    echo "Sleeping before retrying..."
    sleep 2
done

set -x
gunzip -f run-builder.gz
chmod +x run-builder
exec ./run-builder
