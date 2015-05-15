# Copyright 2014 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

docker: Dockerfile
	docker build -t go-commit-watcher .

upload: docker
	docker save go-commit-watcher | gzip | (cd ../../cmd/upload && go run upload.go --public go-builder-data/docker-commit-watcher.tar.gz)
dev-upload:
	docker save go-commit-watcher | gzip | (cd ../../cmd/upload && go run upload.go --public dev-go-builder-data/docker-commit-watcher.tar.gz)
