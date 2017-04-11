#!/bin/bash
#
# This is run on the arm64 host, with the Dockerfile in the same directory.

exec docker build -t gobuilder-arm64:1 .
