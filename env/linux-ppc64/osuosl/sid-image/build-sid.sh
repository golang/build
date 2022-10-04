#!/bin/sh -ex
# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

export rootfs=$(pwd)/debian_sid
if [ ! -e "$rootfs" ]; then
        (
                sudo -E sh -c 'debootstrap --variant=minbase --keyring=/usr/share/keyrings/debian-ports-archive-keyring.gpg --include debian-ports-archive-keyring sid $rootfs https://deb.debian.org/debian-ports'
                cd "$rootfs"
                sudo rm -rf var/log/{dpkg,bootstrap,alternatives}.log var/cache/ldconfig/aux-cache var/cache/apt/* var/lib/apt/lists/* dev/* proc/* sys/*
        )
else
        echo "skipping debootstrap"
fi

cat > Dockerfile.sid <<'EOF'
FROM scratch
COPY ./debian_sid/ /
CMD ["/bin/bash"]
EOF

# always build with buildkit :)
export DOCKER_BUILDKIT=1
sudo -E docker build -f Dockerfile.sid -t murp/debian:sid-ppc64 .
sudo docker run -it --rm murp/debian:sid-ppc64 cat /etc/os-release
