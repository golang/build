#!/bin/bash -eux

go install golang.org/dl/gotip@latest
gotip download
MAJOR=$(gotip env GOVERSION | grep -Eo 'go1\.[0-9]+')
gotip tool dist list > allports/${MAJOR}.txt
