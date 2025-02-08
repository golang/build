# Copyright 2014 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

MUTABLE_VERSION ?= latest
VERSION := $(shell ../coordinator/version.sh)

IMAGE_PROD := gcr.io/symbolic-datum-552/gitmirror

push-prod:
	gcloud builds submit --project=symbolic-datum-552 --config=cloudbuild.yaml --substitutions=TAG_NAME="$(VERSION)" ../..

deploy-prod: push-prod
	go install golang.org/x/build/cmd/xb
	xb --prod kubectl --namespace prod set image deployment/gitmirror-serving-deployment gitmirror=$(IMAGE_PROD):$(VERSION)
	xb --prod kubectl --namespace prod set image deployment/gitmirror-mirroring-deployment gitmirror=$(IMAGE_PROD):$(VERSION)
