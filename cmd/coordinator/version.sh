#!/bin/sh

# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

VERSION=$(git rev-parse HEAD)
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
if ! git diff-index HEAD --quiet || ! git diff-files --quiet; then
  VERSION=$VERSION-dirty
  dirty=1
fi
if [ -n "$dirty" ] || [ -z "$(git config --get-all "branch.${CURRENT_BRANCH}.remote")" ] || [ -n "$(git rev-list '@{upstream}..HEAD')" ]; then
  # Append -user-20190509T023926
  VERSION=$VERSION-$USER-$(git show --quiet --pretty=%cI HEAD | perl -npe 's/[^\dT]//g;$_=substr($_,0,15)')
fi
echo "$VERSION"
