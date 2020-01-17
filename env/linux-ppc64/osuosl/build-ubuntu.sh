#!/bin/sh -ex

export rootfs=$(pwd)/ubuntu_xenial_1604
if [ ! -e "$rootfs" ]; then
	(
		sudo -E sh -c 'debootstrap --variant=minbase --keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg --include ubuntu-keyring xenial $rootfs'
		cd "$rootfs"
		sudo rm -rf var/log/{dpkg,bootstrap,alternatives}.log var/cache/ldconfig/aux-cache var/cache/apt/* var/lib/apt/lists/* dev/* proc/* sys/*
	)
else
	echo "skipping debootstrap"
fi

cat > Dockerfile.xenial <<'EOF'
FROM scratch
COPY ./ubuntu_xenial_1604/ /
CMD ["/bin/bash"]
EOF

# always build with buildkit :)
export DOCKER_BUILDKIT=1
sudo -E docker build -f Dockerfile.xenial -t tiborvass/ubuntu:xenial-ppc64 .
#sudo docker login
#sudo docker push tiborvass/ubuntu:xenial-ppc64
sudo docker run -it --rm tiborvass/ubuntu:xenial-ppc64 cat /etc/os-release
