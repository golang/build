# Copyright 2014 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

DOCKER ?= $(shell which docker >/dev/null 2>&1 && echo docker || echo podman)

docker: Dockerfile
	$(DOCKER) build -t gobuilders/linux-arm-qemu .
