#!/bin/bash

set -e
set -x

ip=$(ifconfig | grep "inet 10.50.0." | perl -npe 's/.*\.(\d+) netmask.*/$1/')

echo "Running with hostname ms_$ip"

# For now use the buildlet baked into the image. We'll probably want to stop
# doing that later, though:

while true; do
    $HOME/bin/buildlet \
        -coordinator=farmer.golang.org \
        -halt=false \
        -hostname=ms_$ip \
        -reverse=darwin-amd64-10_8 || sleep 5
done
