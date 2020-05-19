# Copyright 2020 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# run on arm64 instance
prod-build: Dockerfile
	sudo docker build -t gcr.io/symbolic-datum-552/gobuilder-arm64-aws:latest .

# run on arm64 instance
prod-push: prod-build
	sudo docker login -u oauth2accesstoken https://gcr.io
	sudo docker push gcr.io/symbolic-datum-552/gobuilder-arm64-aws:latest
	sudo rm /root/.docker/config.json

env-var-check:
ifndef AWS_ACCESS_KEY_ID
	$(error AWS_ACCESS_KEY_ID env var is not set)
endif

ifndef AWS_SECRET_ACCESS_KEY
	$(error AWS_SECRET_ACCESS_KEY env var is not set)
endif

create-aws-image: env-var-check
	packer build packer_image_aws_arm64.json
