#!/bin/sh -ex

GO_VERSION=1.13.3

echo "Meant to be run on a system on which Go has official releases and is installed"
GOHOSTARCH=$(go env GOHOSTARCH)
GOHOSTOS=$(go env GOHOSTOS)
export GOOS=linux
export GOARCH=ppc64
targz=go${GO_VERSION}.${GOHOSTOS}-${GOHOSTARCH}.tar.gz
wget https://dl.google.com/go/${targz}
tar xf ${targz}
( cd go/src; ./bootstrap.bash )
bootstrap=go-$GOOS-$GOARCH-bootstrap
rm -f ${bootstrap}.tbz # does not contain all that's needed
tar czf ${bootstrap}.tgz ${bootstrap}
sha256sum $bootstrap.tgz | tee $bootstrap.tgz.sha256
