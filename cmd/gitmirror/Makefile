# Copyright 2014 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

VERSION=v4

docker-prod: Dockerfile
	docker build -f Dockerfile --tag=gcr.io/symbolic-datum-552/gitmirror:$(VERSION)  ../..
docker-dev: Dockerfile
	docker build -f Dockerfile --tag=gcr.io/go-dashboard-dev/gitmirror:latest  ../..

push-prod: docker-prod
	gcloud docker push gcr.io/symbolic-datum-552/gitmirror:$(VERSION)
push-dev: docker-dev
	gcloud docker push gcr.io/go-dashboard-dev/gitmirror:latest
