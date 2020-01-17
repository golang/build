#!/bin/sh -ex

GO_VERSION=1.13.3
CRUN_VERSION=6254263af0a41d3b78ed280f944acda10199d42a # latest released version doesn't work
CONTAINERD_VERSION=v1.2.10
DOCKER_VERSION=v19.03.4
GOOS=linux
GOARCH=ppc64


# Setting up Go toolchain
bootstrap=go-${GOOS}-${GOARCH}-bootstrap
export GOROOT=$(pwd)/${bootstrap}
export GOPATH=$(pwd)/go
export PATH=$GOROOT/bin:$PATH

install_go() {
	if ! which go >/dev/null; then
		sha256sum -c $bootstrap.tgz.sha256
		tar xf $bootstrap.tgz
	else
		echo "skipping go installation"
	fi
	[ "$(go version)" = "go version go${GO_VERSION} ${GOOS}/${GOARCH}" ]
}

install_crun() {
	if [ -e /usr/local/bin/runc ]; then
		echo "skipping runc installation"
		return
	fi
	# crun is a C-only implementation of the OCI runtime spec, compatible with runc
	git clone https://github.com/containers/crun && cd crun && git checkout "$CRUN_VERSION"
	sudo apt-get update && sudo apt-get install --no-install-recommends -y make gcc libc6-dev pkgconf libtool go-md2man libtool autoconf python3 automake libcap-dev libseccomp-dev libyajl-dev
	./autogen.sh && ./configure --prefix=/usr/local && make && sudo make install

	sudo ln -s crun /usr/local/bin/runc    # hacky but will work
}

install_containerd() {
	if [ -e /usr/local/bin/containerd ]; then
		echo "skipping containerd installation"
		return
	fi
	# installs a static build of containerd
	mkdir -p $GOPATH/src/github.com/containerd
	git clone --depth 1 -b "$CONTAINERD_VERSION" https://github.com/containerd/containerd $GOPATH/src/github.com/containerd/containerd
	cd $GOPATH/src/github.com/containerd/containerd
	patch -p1 < containerd.patch
	BUILDTAGS='netgo osusergo static_build no_btrfs' make && sudo make install
}

install_docker() {
	# installs a static build of docker
	# unfortunately, need to build manually
	if [ -e /usr/local/bin/dockerd ]; then
		echo "skipping dockerd installation"
	else
		mkdir -p $GOPATH/src/github.com/docker
		git clone --depth=1 -b "$DOCKER_VERSION" https://github.com/docker/engine $GOPATH/src/github.com/docker/docker
		cd $GOPATH/src/github.com/docker/docker
		VERSION="$DOCKER_VERSION" GITCOMMIT=$(git rev-parse --short HEAD) bash ./hack/make/.go-autogen
		CGO_ENABLED=0 go build -o dockerd \
			-tags 'autogen netgo osusergo static_build exclude_graphdriver_devicemapper exclude_disk_quota' \
			-installsuffix netgo  \
			github.com/docker/docker/cmd/dockerd
		sudo mv dockerd /usr/local/bin/dockerd
	fi
	if [ -e /usr/local/bin/docker ]; then
		echo "skipping dockerd installation"
		return
	fi
	git clone --depth=1 -b "$DOCKER_VERSION" https://github.com/docker/cli $GOPATH/src/github.com/docker/cli
	cd $GOPATH/src/github.com/docker/cli
	VERSION="$DOCKER_VERSION" GITCOMMIT=$(git rev-parse --short HEAD) ./scripts/build/binary
	sudo cp build/docker-${GOOS}-${GOARCH} /usr/local/bin/docker
}

install_go
install_crun
install_containerd
install_docker

sudo install docker.service /etc/systemd/user/docker.service
sudo systemctl enable /etc/systemd/user/docker.service || true
sudo systemctl restart docker

sudo docker version
