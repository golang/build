# Copyright 2021 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

env-var-check:
ifndef AWS_ACCESS_KEY_ID
	$(error AWS_ACCESS_KEY_ID env var is not set)
endif

ifndef AWS_SECRET_ACCESS_KEY
	$(error AWS_SECRET_ACCESS_KEY env var is not set)
endif

create-aws-image: env-var-check
	export AWS_MAX_ATTEMPTS=600
	export AWS_POLL_DELAY_SECONDS=10
	export PACKER_LOG=1
	packer build -timestamp-ui packer_image_aws_arm64.json
