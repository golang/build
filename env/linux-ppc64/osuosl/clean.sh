#!/bin/sh

[ $(id -u) -ne 0 ] && echo need root && exit 1

set -x

pwd
cat $0
echo
read -p "Press enter to continue" r

rm -f /usr/local/bin/crun
# being extra careful
[ -L /usr/local/bin/runc ] && [ "$(readlink /usr/local/bin/runc)" = "crun" ] && rm -f /usr/local/bin/runc
file /usr/local/bin/containerd | grep -q static && rm -f /usr/local/bin/containerd{,-shim,-shim-runc-v1,-shim-runc-v2,-stress} /usr/local/bin/ctr
file /usr/local/bin/dockerd | grep -q static && rm -f /usr/local/bin/dockerd /usr/local/bin/docker

[ "$1" = all ] && rm -rf Dockerfile.xenial crun go-linux-ppc64-bootstrap go ubuntu_xenial_1604
