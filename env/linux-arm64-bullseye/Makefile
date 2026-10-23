# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

IMAGE_NAME=$(shell basename $(CURDIR))
DOCKER ?= $(shell which docker >/dev/null 2>&1 && echo docker || echo podman)

usage:
	echo "see Makefile for usage for building $(IMAGE_NAME)"

docker: Dockerfile
# If this command is failing, run:
#  docker run --rm --privileged multiarch/qemu-user-static --reset -p yes
#  docker buildx create --name mybuilder
	$(DOCKER) buildx build -t golang/$(IMAGE_NAME) --platform linux/arm64 --load -f Dockerfile ../..

testssh: docker
	go install golang.org/x/build/cmd/buildlet/testssh
	testssh --start-image=golang/$(IMAGE_NAME)

push-staging: docker
	go install golang.org/x/build/cmd/xb
	xb --staging $(DOCKER) tag golang/$(IMAGE_NAME) REPO/$(IMAGE_NAME):latest
	xb --staging $(DOCKER) push REPO/$(IMAGE_NAME):latest

push-prod: docker
	xb --prod $(DOCKER) tag golang/$(IMAGE_NAME) REPO/$(IMAGE_NAME):latest
	xb --prod $(DOCKER) push REPO/$(IMAGE_NAME):latest
