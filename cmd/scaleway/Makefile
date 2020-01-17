# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

VERSION=latest

docker: Dockerfile *.go
	xb docker build -f Dockerfile --force-rm --tag=golang/scaleway:$(VERSION) ../..

push-prod: docker
	docker tag golang/scaleway:$(VERSION) gcr.io/symbolic-datum-552/scaleway:$(VERSION)
	docker push gcr.io/symbolic-datum-552/scaleway:$(VERSION)

stop-prod:
	xb --prod kubectl delete -f deployment-prod.yaml

start-prod:
	xb --prod kubectl create -f deployment-prod.yaml

restart-prod:
	xb --prod kubectl delete -f deployment-prod.yaml || true
	xb --prod kubectl create -f deployment-prod.yaml
