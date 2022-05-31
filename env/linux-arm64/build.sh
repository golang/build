#!/bin/bash
# Copyright 2022 Go Authors All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

#
# This is run on the arm64 host, with the Dockerfile in the same directory,
# by the build scripts in linaro and packet subdirectories.

docker build -t golang.org/linux-arm64 .
