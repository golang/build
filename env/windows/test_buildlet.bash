#!/bin/bash

# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Prerequisites for using this script

set -ue

hostname="$1"
BUILDLET="windows-amd64-2012@${hostname}"

echo "Pushing go1.4 to buildlet"
gomote put14 "$BUILDLET"

echo "Pushing go tip to buildlet"
(
  TEMPDIR=`mktemp -d `
  cd $TEMPDIR
  git clone //go.googlesource.com/go
  tar zcf - ./go > go.tar.gz
  gomote puttar "$BUILDLET" go.tar.gz
  rm -rf go go.tar.gz
)

echo "Building go (32-bit)"
gomote run -e GOARCH=386 -e GOHOSTARCH=386 -path 'C:/godep/gcc32/bin,$WORKDIR/go/bin,$PATH' -e 'GOROOT=c:\workdir\go' "$BUILDLET" go/src/make.bat

echo "Building go (64-bit)"
gomote run -path '$PATH,C:/godep/gcc64/bin,$WORKDIR/go/bin,$PATH' -e 'GOROOT=c:\workdir\go' "$BUILDLET" go/src/make.bat

# Note: full tests commented out for now. Comment out this early exit to
# re-enable tests when qualifying new VMs.
exit 0

# Keep going on error.
set +e

echo "Rebuilding go (32-bit)"
gomote run -e GOARCH=386 -e GOHOSTARCH=386 -path 'C:/godep/gcc32/bin,$WORKDIR/go/bin,$PATH' -e 'GOROOT=c:\workdir\go' "$BUILDLET" go/src/make.bat

echo "Running tests for go (32-bit)"
gomote run -e GOARCH=386 -e GOHOSTARCH=386 -path 'C:/godep/gcc32/bin,$WORKDIR/go/bin,$PATH' -e 'GOROOT=C:\workdir\go' "$BUILDLET" go/bin/go.exe tool dist test -v --no-rebuild

echo "Rebuilding go (64-bit)"
gomote run -path '$PATH,C:/godep/gcc64/bin,$WORKDIR/go/bin,$PATH' -e 'GOROOT=c:\workdir\go' "$BUILDLET" go/src/make.bat

echo "Running tests for go (64-bit)"
gomote run -path 'C:/godep/gcc64/bin,$WORKDIR/go/bin,$PATH' -e 'GOROOT=C:\workdir\go' "$BUILDLET" go/bin/go.exe tool dist test -v --no-rebuild
