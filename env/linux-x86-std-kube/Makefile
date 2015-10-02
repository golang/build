# Copyright 2015 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
usage:
	echo "Use staging or prod targets" ; exit 1

staging: Dockerfile
	docker build -t gcr.io/go-dashboard-dev/linux-buildlet-std:latest .
	gcloud docker push gcr.io/go-dashboard-dev/linux-buildlet-std:latest

prod: Dockerfile
	docker build -t gcr.io/symbolic-datum-552/linux-buildlet-std:latest .
	gcloud docker push gcr.io/symbolic-datum-552/linux-buildlet-std:latest
