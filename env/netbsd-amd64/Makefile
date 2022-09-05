# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

RELEASE=9.3
ARCH=amd64

VM_IMAGE_FILE=vm-image-netbsd-${ARCH}-${RELEASE}.tar.gz
VM_IMAGE_NAME=netbsd-${ARCH}-$(subst .,-,$(RELEASE))


upload-staging:
	go install golang.org/x/build/cmd/upload
	upload --verbose --file=${VM_IMAGE_FILE} --public dev-go-builder-data/${VM_IMAGE_FILE}

upload-prod:
	go install golang.org/x/build/cmd/upload
	upload --verbose --file=${VM_IMAGE_FILE} --public go-builder-data/${VM_IMAGE_FILE}

create-image-staging:
	gcloud compute --project go-dashboard-dev images create ${VM_IMAGE_NAME} --source-uri gs://dev-go-builder-data/${VM_IMAGE_FILE}

create-image-prod:
	gcloud compute --project symbolic-datum-552 images create ${VM_IMAGE_NAME} --source-uri gs://go-builder-data/${VM_IMAGE_FILE}

clean:
	rm -rf netbsd-*.tar.gz vm-image-netbsd-*.tar.gz work-NetBSD-* anita-*.tar.gz anita-*/
