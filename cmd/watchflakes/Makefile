# Copyright 2024 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

MUTABLE_VERSION ?= latest
VERSION := $(shell ../coordinator/version.sh)

GCP_PROJECT_PROD := symbolic-datum-552
IMAGE_PROD := gcr.io/$(GCP_PROJECT_PROD)/watchflakes

push-prod:
	gcloud builds submit --project=$(GCP_PROJECT_PROD) --config=../../deploy/cloudbuild-from-local.yaml \
	  --substitutions="_SERVICE_NAME=watchflakes,TAG_NAME=$(VERSION)" ../..

deploy-prod: push-prod
	go install golang.org/x/build/cmd/xb
	xb --prod kubectl --namespace prod set image deployment/watchflakes-deployment watchflakes=$(IMAGE_PROD):$(VERSION)
