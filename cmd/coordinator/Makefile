# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

usage: FORCE
	# See Makefile targets.
	exit 1

FORCE:

MUTABLE_VERSION := latest
VERSION := $(shell ./version.sh)

GCP_PROJECT_PROD := symbolic-datum-552
IMAGE_STAGING := gcr.io/go-dashboard-dev/coordinator
IMAGE_PROD := gcr.io/$(GCP_PROJECT_PROD)/coordinator

DOCKER_IMAGE=golang/coordinator

docker: *.go Dockerfile
	docker build --force-rm -f Dockerfile --build-arg "version=$(VERSION)" --tag=$(DOCKER_IMAGE):$(VERSION) ../..

docker-staging: docker
	docker tag $(DOCKER_IMAGE):$(VERSION) $(IMAGE_STAGING):$(VERSION)
	docker tag $(DOCKER_IMAGE):$(VERSION) $(IMAGE_STAGING):$(MUTABLE_VERSION)

push-prod:
	gcloud builds submit --project $(GCP_PROJECT_PROD) --config ./cloudbuild.yaml --substitutions=TAG_NAME="$(VERSION)" ../../

push-staging: docker-staging
	docker push $(IMAGE_STAGING):$(VERSION)
	docker push $(IMAGE_STAGING):$(MUTABLE_VERSION)

deploy-prod: push-prod
	go install golang.org/x/build/cmd/xb
	xb --prod kubectl --namespace prod set image deployment/coordinator-deployment coordinator=$(IMAGE_PROD):$(VERSION)
deploy-staging: push-staging
	go install golang.org/x/build/cmd/xb
	xb --staging kubectl set image deployment/coordinator-deployment coordinator=$(IMAGE_STAGING):$(VERSION)
