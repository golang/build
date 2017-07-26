# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Set dirty suffix depending on output of "git status".
DIRTY = ""
ifneq ($(shell git status --porcelain -- .),)
  DIRTY = "-tainted"
endif
VERSION ?= `git show --format='%h' --no-patch`$(DIRTY)

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
	docker build --force-rm --tag=gcr.io/symbolic-datum-552/devapp:$(VERSION) .
docker-staging: Dockerfile devapp ca-certificates.crt
	docker build --force-rm --tag=gcr.io/go-dashboard-dev/devapp:$(VERSION) .

push-prod: docker-prod
	gcloud docker -- push gcr.io/symbolic-datum-552/devapp:$(VERSION)
push-staging: docker-staging
	gcloud docker -- push gcr.io/go-dashboard-dev/devapp:$(VERSION)

deploy-prod: push-prod
	kubectl set image deployment/devapp-deployment devapp=gcr.io/symbolic-datum-552/devapp:$(VERSION)
deploy-staging: push-staging
	kubectl set image deployment/devapp-deployment devapp=gcr.io/go-dashboard-dev/devapp:$(VERSION)

.PHONY: clean
clean:
	$(RM) devapp
	$(RM) ca-certificates.crt
