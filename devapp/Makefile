# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

MUTABLE_VERSION ?= latest
VERSION ?= $(shell git rev-parse --short HEAD)

IMAGE_STAGING := gcr.io/go-dashboard-dev/devapp
IMAGE_PROD := gcr.io/symbolic-datum-552/devapp

DOCKER_IMAGE_build0=build0/devapp:latest
DOCKER_CTR_build0=devapp-build0

build0: *.go Dockerfile.0
	docker build --force-rm -f Dockerfile.0 --tag=$(DOCKER_IMAGE_build0) ../

devapp: build0
	docker create --name $(DOCKER_CTR_build0) $(DOCKER_IMAGE_build0)
	docker cp $(DOCKER_CTR_build0):/go/bin/$@ $@
	docker rm $(DOCKER_CTR_build0)

ca-certificates.crt:
	docker create --name $(DOCKER_CTR_build0) $(DOCKER_IMAGE_build0)
	docker cp $(DOCKER_CTR_build0):/etc/ssl/certs/$@ $@
	docker rm $(DOCKER_CTR_build0)

update-deps:
	go install golang.org/x/build/cmd/gitlock
	gitlock --update=Dockerfile.0 golang.org/x/build/devapp

docker-prod: Dockerfile devapp ca-certificates.crt
	docker build --force-rm --tag=$(IMAGE_PROD):$(VERSION) .
	docker tag $(IMAGE_PROD):$(VERSION) $(IMAGE_PROD):$(MUTABLE_VERSION)
docker-staging: Dockerfile devapp ca-certificates.crt
	docker build --force-rm --tag=$(IMAGE_STAGING):$(VERSION) .
	docker tag $(IMAGE_STAGING):$(VERSION) $(IMAGE_STAGING):$(MUTABLE_VERSION)

push-prod: docker-prod
	gcloud docker -- push $(IMAGE_PROD):$(VERSION)
push-staging: docker-staging
	gcloud docker -- push $(IMAGE_STAGING):$(VERSION)

deploy-prod: push-prod
	kubectl set image deployment/devapp-deployment devapp=$(IMAGE_PROD):$(VERSION)
deploy-staging: push-staging
	kubectl set image deployment/devapp-deployment devapp=$(IMAGE_STAGING):$(VERSION)

.PHONY: clean
clean:
	$(RM) devapp
	$(RM) ca-certificates.crt
