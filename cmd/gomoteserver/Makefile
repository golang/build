# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

usage: FORCE
	exit 1

FORCE:

MUTABLE_VERSION := latest
# TODO(go.dev/issue/62118) change how version is generated.
VERSION := $(shell ./version.sh)

GCP_PROJECT_PROD := symbolic-datum-552
IMAGE_PROD := gcr.io/$(GCP_PROJECT_PROD)/gomoteserver

DOCKER_IMAGE=golang/gomoteserver

docker: *.go Dockerfile
	docker build --force-rm -f Dockerfile --build-arg "version=$(VERSION)" --tag=$(DOCKER_IMAGE):$(VERSION) ../..

# push-prod builds and pushes the gomoteserver image using the local source code.
# The image is tagged with a version generated from the current git state.
push-prod:
	gcloud builds submit --project $(GCP_PROJECT_PROD) --config ./cloudbuild.yaml --substitutions=TAG_NAME="$(VERSION)" ../../

# deploy-prod deploys the version of the gomoteserver that was just pushed by the push-prod target.
deploy-prod: push-prod
	go install golang.org/x/build/cmd/xb
	xb --prod kubectl --namespace prod set image deployment/gomoteserver-deployment gomoteserver=$(IMAGE_PROD):$(VERSION)

# push-prod-version builds and pushes the gomoteserver image from a specific git commit SHA.
# This target uses a separate cloudbuild configuration to check out the specified commit before building.
#
# Usage:
#   make push-prod-version SHA=<commit-sha>
push-prod-version:
ifndef SHA
	$(error SHA is not set. Please provide a commit SHA. Usage: make push-prod-version SHA=<commit-sha>)
endif
	gcloud builds submit --project $(GCP_PROJECT_PROD) --config ./cloudbuild-version.yaml --substitutions=_SHA=$(SHA) --no-source

# deploy-prod-version deploys a specific version of the gomoteserver based on a git commit SHA.
# It first builds and pushes the image using the push-prod-version target, then deploys it.
#
# Usage:
#   make deploy-prod-version SHA=<commit-sha>
deploy-prod-version: push-prod-version
ifndef SHA
	$(error SHA is not set. Please provide a commit SHA. Usage: make deploy-prod-version SHA=<commit-sha>)
endif
	go install golang.org/x/build/cmd/xb
	xb --prod kubectl --namespace prod set image deployment/gomoteserver-deployment gomoteserver=$(IMAGE_PROD):$(SHA)
