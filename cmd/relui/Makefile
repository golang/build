# Copyright 2021 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

VERSION := $(shell ../coordinator/version.sh)
DOCKER_TAG := golang/relui:$(VERSION)

INTERNAL_PATH := ../../internal/relui
MIGRATION_FILES := $(wildcard $(INTERNAL_PATH)/migrations/*.sql)
GO_FILES := $(shell find $(INTERNAL_PATH) -name "*.go")

POSTGRES_DATA_DEV := "postgres-data-dev:/var/lib/postgresql/data"
POSTGRES_RUN_DEV := "postgres-run-dev:/var/run/postgresql"
POSTGRES_USER := "postgres"
POSTGRES_TEST := psql --username=$(POSTGRES_USER) -c "SELECT 1;"

DEV_CFG := ${HOME}/.local/share/relui

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

schema.sql: $(MIGRATION_FILES) $(GO_FILES)
	docker build -f Dockerfile -t golang/relui:$(VERSION) ../..
	docker run --rm --name=relui-dev-migrate -v $(POSTGRES_RUN_DEV) -e PGUSER=$(POSTGRES_USER) -e PGDATABASE=relui-dev $(DOCKER_TAG) --only-migrate
	docker exec postgres-dev pg_dump --username=$(POSTGRES_USER) --schema-only relui-dev > schema.sql

.PHONY: test
test: postgres-dev docker-test
	docker run --rm --name=relui-test -v $(POSTGRES_RUN_DEV) -e PGUSER=$(POSTGRES_USER) -e PGDATABASE=relui-test golang/relui-test:$(VERSION)

.PHONY: docker
docker:
	docker build -f Dockerfile -t golang/relui:$(VERSION) ../..

.PHONY: docker-test
docker-test:
	docker build -f Dockerfile.test -t golang/relui-test:$(VERSION) ../..

$(DEV_CFG):
	mkdir -p $(DEV_CFG)

$(DEV_CFG)/pgpass: $(DEV_CFG)
	touch $(DEV_CFG)/pgpass
	chmod 0600 $(DEV_CFG)/pgpass
	openssl rand -hex -out $(DEV_CFG)/pgpass 32
