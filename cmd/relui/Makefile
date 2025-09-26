# Copyright 2021 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

VERSION := $(shell ../coordinator/version.sh)
DOCKER_TAG := golang/relui:$(VERSION)

INTERNAL_PATH := ../../internal/relui

POSTGRES_DATA_DEV := "postgres-data-dev:/var/lib/postgresql/data"
POSTGRES_RUN_DEV := "postgres-run-dev:/var/run/postgresql"
POSTGRES_USER := "postgres"
POSTGRES_TEST := psql --username=$(POSTGRES_USER) -c "SELECT 1;"

DEV_CFG := ${HOME}/.local/share/relui

MODULE_ROOT := "$(shell readlink -f ../../)"
NODE_IMAGE_VERSION := "node:16.15.1-bullseye"

.PHONY: dev
dev: postgres-dev docker
	docker run --rm --name=relui-dev -v $(POSTGRES_RUN_DEV) -e PGUSER=$(POSTGRES_USER) -e PGDATABASE=relui-dev -p 8080:8080 $(DOCKER_TAG)

.PHONY: postgres-dev
postgres-dev: $(DEV_CFG)/pgpass
	docker exec postgres-dev $(POSTGRES_TEST) || \
	docker run --rm -d --name=postgres-dev \
		-p 127.0.0.1:5432:5432 \
		-v $(POSTGRES_DATA_DEV) \
		-v $(POSTGRES_RUN_DEV) \
		-v $(DEV_CFG)/pgpass:/run/secrets/pgpass \
		-e POSTGRES_PASSWORD_FILE=/run/secrets/pgpass \
		postgres:13

migrate: docker
	docker run --rm --name=relui-dev-migrate -v $(POSTGRES_RUN_DEV) -e PGUSER=$(POSTGRES_USER) -e PGDATABASE=relui-dev $(DOCKER_TAG) --migrate-only

migrate-down-up: docker
	docker run --rm --name=relui-dev-migrate -v $(POSTGRES_RUN_DEV) -e PGUSER=$(POSTGRES_USER) -e PGDATABASE=relui-dev $(DOCKER_TAG) --migrate-down-up

.PHONY: test
test: postgres-dev docker-test
	docker run --rm --name=relui-test -v $(POSTGRES_RUN_DEV) -e PGUSER=$(POSTGRES_USER) -e PGDATABASE=relui-test golang/relui-test:$(VERSION)

GCP_PROJECT_PROD := symbolic-datum-552
DOCKER_IMAGE := golang/relui
IMAGE_PROD := gcr.io/$(GCP_PROJECT_PROD)/relui
MUTABLE_VERSION := latest

.PHONY: docker
docker:
	docker build -f Dockerfile -t $(DOCKER_IMAGE):$(VERSION) ../..

.PHONY: docker-test
docker-test:
	docker build -f Dockerfile.test -t golang/relui-test:$(VERSION) ../..

.PHONY: docker-prod
docker-prod: docker
	docker tag $(DOCKER_IMAGE):$(VERSION) $(IMAGE_PROD):$(VERSION)
	docker tag $(DOCKER_IMAGE):$(VERSION) $(IMAGE_PROD):$(MUTABLE_VERSION)

.PHONY: push-prod
push-prod:
	gcloud builds submit --project=$(GCP_PROJECT_PROD) --config=../../deploy/cloudbuild-from-local.yaml \
	  --substitutions="_SERVICE_NAME=relui,TAG_NAME=$(VERSION)" ../..

.PHONY: deploy-prod
deploy-prod: push-prod
	go install golang.org/x/build/cmd/xb
	xb --prod kubectl --namespace prod set image deployment/relui-deployment relui=$(IMAGE_PROD):$(VERSION)

node_modules/: package.json package-lock.json
	docker run --rm \
		--volume $(MODULE_ROOT):/workspace \
		--workdir /workspace/cmd/relui \
		--env NODE_OPTIONS="--experimental-vm-modules --no-warnings" \
		$(NODE_IMAGE_VERSION) \
		npm install --no-update-notifier

.PHONY: lint
lint: node_modules/
	docker run --rm \
		--volume $(MODULE_ROOT):/workspace \
		--workdir /workspace/cmd/relui \
		--env NODE_OPTIONS="--experimental-vm-modules --no-warnings" \
		$(NODE_IMAGE_VERSION) \
		npm --no-update-notifier run lint:js
	docker run --rm \
		--volume $(MODULE_ROOT):/workspace \
		--workdir /workspace/cmd/relui \
		--env NODE_OPTIONS="--experimental-vm-modules --no-warnings" \
		$(NODE_IMAGE_VERSION) \
		npm --no-update-notifier run lint:css
