# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

MUTABLE_VERSION ?= latest
VERSION ?= $(shell git rev-parse --short HEAD)

IMAGE_PROD := gcr.io/symbolic-datum-552/vcs-test
IMAGE_PROD_APACHE := gcr.io/symbolic-datum-552/vcs-test-apache

docker-prod: Dockerfile Dockerfile.apache
	docker build --force-rm -f Dockerfile --tag=$(IMAGE_PROD):$(VERSION) ../
	docker tag $(IMAGE_PROD):$(VERSION) $(IMAGE_PROD):$(MUTABLE_VERSION)
	docker build --force-rm -f Dockerfile.apache --tag=$(IMAGE_PROD_APACHE):$(VERSION) ../
	docker tag $(IMAGE_PROD_APACHE):$(VERSION) $(IMAGE_PROD_APACHE):$(MUTABLE_VERSION)

push-prod: docker-prod
	docker push $(IMAGE_PROD):$(VERSION)
	docker push $(IMAGE_PROD):$(MUTABLE_VERSION)
	docker push $(IMAGE_PROD_APACHE):$(VERSION)
	docker push $(IMAGE_PROD_APACHE):$(MUTABLE_VERSION)


deploy-prod: push-prod
	go install golang.org/x/build/cmd/xb
	xb --prod kubectl --namespace prod set image deployment/vcs-test-deployment vcs-test=$(IMAGE_PROD):$(VERSION) apache=$(IMAGE_PROD_APACHE):$(VERSION)
