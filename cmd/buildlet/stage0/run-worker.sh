#!/bin/bash -eux
# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -o pipefail

trap "exit 10" SIGUSR1

project=$(curl -s -H "Metadata-Flavor: Google" http://metadata.google.internal/computeMetadata/v1/project/project-id)

if [[ ! $project =~ "luci" ]]; then
    exec $(dirname $0)/stage0
fi

if ! id swarming >& /dev/null; then
    useradd -m swarming
fi

echo "%swarming ALL = NOPASSWD: /sbin/shutdown" > /etc/sudoers.d/swarming
echo -e '#!/bin/bash\nkill -SIGUSR1 1' > /sbin/shutdown
chmod 0755 /sbin/shutdown

SWARM_DIR=/b/swarming
mkdir -p $SWARM_DIR
chown swarming:swarming $SWARM_DIR

su -c "$(dirname $0)/bootstrapswarm --swarming ${SWARMING}.appspot.com" swarming &
wait %1
exit $?