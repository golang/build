# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

usage: FORCE
	# See Makefile targets.
	exit 1

FORCE:

# After "make upload", either reboot the machine, or ssh to it and:
#   sudo systemctl restart gobuild.service
# And watch its logs with:
#   sudo journalctl -f -u gobuild.service
upload: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=linux-amd64 --file=go:golang.org/x/build/cmd/coordinator --public go-builder-data/coordinator

dev-upload: FORCE
	go install golang.org/x/build/cmd/upload
	upload --verbose --osarch=linux-amd64 --file=go:golang.org/x/build/cmd/coordinator --public dev-go-builder-data/coordinator

MUTABLE_VERSION := latest
VERSION := $(shell ./version.sh)

IMAGE_STAGING := gcr.io/go-dashboard-dev/coordinator
IMAGE_PROD := gcr.io/symbolic-datum-552/coordinator

DOCKER_IMAGE=golang/coordinator

docker: *.go Dockerfile
	docker build --force-rm -f Dockerfile --build-arg "version=$(VERSION)" --tag=$(DOCKER_IMAGE):$(VERSION) ../..

docker-prod: docker
	docker tag $(DOCKER_IMAGE):$(VERSION) $(IMAGE_PROD):$(VERSION)
	docker tag $(DOCKER_IMAGE):$(VERSION) $(IMAGE_PROD):$(MUTABLE_VERSION)
docker-staging: docker
	docker tag $(DOCKER_IMAGE):$(VERSION) $(IMAGE_STAGING):$(VERSION)
	docker tag $(DOCKER_IMAGE):$(VERSION) $(IMAGE_STAGING):$(MUTABLE_VERSION)

push-prod: docker-prod
	docker push $(IMAGE_PROD):$(VERSION)
	docker push $(IMAGE_PROD):$(MUTABLE_VERSION)
push-staging: docker-staging
	docker push $(IMAGE_STAGING):$(VERSION)
	docker push $(IMAGE_STAGING):$(MUTABLE_VERSION)

deploy-prod: push-prod
	go install golang.org/x/build/cmd/xb
	xb --prod kubectl set image deployment/coordinator-deployment coordinator=$(IMAGE_PROD):$(VERSION)
deploy-staging: push-staging
	go install golang.org/x/build/cmd/xb
	xb --staging kubectl set image deployment/coordinator-deployment coordinator=$(IMAGE_STAGING):$(VERSION)
