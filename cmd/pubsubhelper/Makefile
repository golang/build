# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

VERSION=v1

docker-prod: Dockerfile
	docker build -f Dockerfile --tag=gcr.io/symbolic-datum-552/pubsubhelper:$(VERSION)  ../..
docker-dev: Dockerfile
	docker build -f Dockerfile --tag=gcr.io/go-dashboard-dev/pubsubhelper:latest  ../..

push-prod: docker-prod
	gcloud docker -- push gcr.io/symbolic-datum-552/pubsubhelper:$(VERSION)
push-dev: docker-dev
	gcloud docker -- push gcr.io/go-dashboard-dev/pubsubhelper:latest
