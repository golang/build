# Copyright 2018 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

IMAGE_NAME=$(shell basename $(CURDIR))
DOCKER ?= $(shell which docker >/dev/null 2>&1 && echo docker || echo podman)

usage:
	echo "see Makefile for usage for building $(IMAGE_NAME)"

docker: Dockerfile
	go install golang.org/x/build/cmd/xb
	xb $(DOCKER) build -t golang/$(IMAGE_NAME) .

push-staging: docker
	go install golang.org/x/build/cmd/xb
	$(eval REV := $(shell $(DOCKER) images --format={{.ID}} golang/android-amd64-emu:latest))
	xb --staging $(DOCKER) tag golang/$(IMAGE_NAME) REPO/$(IMAGE_NAME):$(REV)
	xb --staging $(DOCKER) push REPO/$(IMAGE_NAME):$(REV)

push-prod: docker
	go install golang.org/x/build/cmd/xb
	$(eval REV := $(shell $(DOCKER) images --format={{.ID}} golang/android-amd64-emu:latest))
	xb --prod $(DOCKER) tag golang/$(IMAGE_NAME) REPO/$(IMAGE_NAME):$(REV)
	xb --prod $(DOCKER) push REPO/$(IMAGE_NAME):$(REV)

vm-prod: push-prod
	./create-vm.sh
