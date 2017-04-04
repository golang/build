# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -ex

apt-get update
apt-get install -y --no-install-recommends ca-certificates

apt-get clean
rm -fr /var/lib/apt/lists
