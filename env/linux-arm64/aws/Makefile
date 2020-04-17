# Copyright 2020 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Compiled stage0 binary must be in working dir.

STAGING_BUCKET=dev-go-builder-data
PROD_BUCKET=go-builder-data

staging: Dockerfile
	docker build --build-arg -t gobuilder-arm64-aws:latest .

prod: Dockerfile
	docker build --build-arg -t gobuilder-arm64-aws:latest .

env-var-check:
ifndef AWS_ACCESS_KEY_ID
	$(error AWS_ACCESS_KEY_ID env var is not set)
endif

ifndef AWS_SECRET_ACCESS_KEY
	$(error AWS_SECRET_ACCESS_KEY env var is not set)
endif

create-aws-image: env-var-check
	packer build packer_image_aws_arm64.json
