# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

IMAGE_NAME=$(shell basename $(CURDIR))
PROD_REPO=gcr.io/symbolic-datum-552

usage:
	echo "Use prod or dev targets. For dev, specify your Docker repository with the REPO=foo argument." ; exit 1

prod: Dockerfile
	docker build -t $(PROD_REPO)/$(IMAGE_NAME):latest --build-arg REPO=$(PROD_REPO) -f Dockerfile .

pushprod: prod
	docker push $(PROD_REPO)/$(IMAGE_NAME):latest

# You must provide a REPO=your-repo-name arg when you make
# this target. REPO is the name of the Docker repository
# that will be prefixed to the name of the image being built.
dev: Dockerfile
	docker build -t $(REPO)/$(IMAGE_NAME):latest --build-arg REPO=$(REPO) -f Dockerfile .
	docker push $(REPO)/$(IMAGE_NAME):latest
