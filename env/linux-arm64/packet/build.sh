#!/bin/bash
# Copyright 2022 Go Authors All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

#
# This is run on the arm64 host with the Dockerfile in the same directory.
# The parent Dockerfile and build.sh (linux-arm64/*) must be in parent directory.

(cd ../ && ./build.sh) && docker build -t gobuilder-arm64-packet:1 .
