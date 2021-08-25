# Copyright 2021 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

VERSION := $(shell ../coordinator/version.sh)

.PHONY: dev
dev: postgres-dev docker
	docker run --rm --name=relui-dev -v postgres-run-dev:/var/run/postgresql -p 8080:8080 golang/relui:$(VERSION)

RUNNING := $(shell docker ps -q -f Name=postgres-dev)
.PHONY: postgres-dev
postgres-dev: network-dev
	docker stop postgres-dev || true
	docker run -d --rm --name=postgres-dev --network=relui-dev -v postgres-data-dev:/var/lib/postgresql/data -v postgres-run-dev:/var/run/postgresql -e POSTGRES_HOST_AUTH_METHOD=trust postgres:13

.PHONY: network-dev
network-dev:
	docker network inspect relui-dev -f "{{.Name}}" || docker network create -d bridge relui-dev

.PHONY: test
test: postgres-dev docker-test
	docker run --rm --name=relui-test -v postgres-run-dev:/var/run/postgresql -e RELUI_TEST_DATABASE="host=/var/run/postgresql user=postgres database=relui-test" golang/relui-test:$(VERSION)

.PHONY: docker
docker:
	docker build -f Dockerfile -t golang/relui:$(VERSION) ../..

.PHONY: docker-test
docker-test:
	docker build -f Dockerfile.test -t golang/relui-test:$(VERSION) ../..
