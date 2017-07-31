# Copyright 2014 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

VERSION=latest
DOCKER_IMAGE_build0=build0/gitmirror:latest
DOCKER_CTR_build0=gitmirror-build0

build0: *.go Dockerfile.0
	docker build --force-rm -f Dockerfile.0 --tag=$(DOCKER_IMAGE_build0) ../..

gitmirror: build0
	docker create --name $(DOCKER_CTR_build0) $(DOCKER_IMAGE_build0)
	docker cp $(DOCKER_CTR_build0):/go/bin/$@ $@
	docker rm $(DOCKER_CTR_build0)

update-deps:
	go install golang.org/x/build/cmd/gitlock
	gitlock --update=Dockerfile.0 golang.org/x/build/cmd/gitmirror

docker-prod: Dockerfile gitmirror
	docker build --force-rm --tag=gcr.io/symbolic-datum-552/gitmirror:$(VERSION) .
docker-staging: Dockerfile gitmirror
	docker build --force-rm --tag=gcr.io/go-dashboard-dev/gitmirror:latest .

push-prod: docker-prod
	gcloud docker -- push gcr.io/symbolic-datum-552/gitmirror:$(VERSION)
push-staging: docker-staging
	gcloud docker -- push gcr.io/go-dashboard-dev/gitmirror:latest

.PHONY: clean
clean:
	$(RM) gitmirror
