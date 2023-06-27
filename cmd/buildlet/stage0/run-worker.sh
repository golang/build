#!/bin/bash -eux
# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -o pipefail
project=$(curl -s -H "Metadata-Flavor: Google" http://metadata.google.internal/computeMetadata/v1/project/project-id)

worker=stage0
if [[ $project =~ "luci" ]]; then
    worker=bootstrapswarm
fi
exec $(dirname $0)/$worker
