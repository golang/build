set -ex

export GOPATH=/gopath
export GOROOT=/goroot
PREFIX=/usr/local
: ${GO_REV:?"need to be set to the golang repo revision used to build the commit watcher."}
: ${TOOLS_REV:?"need to be set to the tools repo revision used to build the commit watcher."}
: ${WATCHER_REV:?"need to be set to the build repo revision for the commit watcher."}

mkdir -p $GOROOT
git clone https://go.googlesource.com/go $GOROOT
(cd $GOROOT/src && git reset --hard $GO_REV && find && ./make.bash)

GO_TOOLS=$GOPATH/src/golang.org/x/tools
mkdir -p $GO_TOOLS
git clone https://go.googlesource.com/tools $GO_TOOLS
(cd $GO_TOOLS && git reset --hard $TOOLS_REV)

GO_BUILD=$GOPATH/src/golang.org/x/build
mkdir -p $GO_BUILD
git clone https://go.googlesource.com/build $GO_BUILD

# Um, this didn't seem to work? Old git version in wheezy?
#git fetch https://go.googlesource.com/build $WATCHER_REV:origin/dummy-commit # in case it's a pending CL
# Hack, instead:
cd $GO_BUILD && git fetch https://go.googlesource.com/build refs/changes/50/10750/5

mkdir -p $PREFIX/bin
(cd $GO_BUILD && git reset --hard $WATCHER_REV && GOBIN=$PREFIX/bin /goroot/bin/go install golang.org/x/build/cmd/watcher)

rm -fR $GOROOT/bin $GOROOT/pkg $GOPATH
