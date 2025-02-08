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

push-prod:
	gcloud builds submit --project $(GCP_PROJECT_PROD) --config ./cloudbuild.yaml --substitutions=TAG_NAME="$(VERSION)" ../../

deploy-prod: push-prod
	go install golang.org/x/build/cmd/xb
	xb --prod kubectl --namespace prod set image deployment/gomoteserver-deployment gomoteserver=$(IMAGE_PROD):$(VERSION)
