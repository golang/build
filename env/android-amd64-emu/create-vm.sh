#!/bin/sh
# Copyright 2019 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -e
set -x

ZONE=us-central1-f
DEBIAN=bullseye
TARGET_IMAGE=android-amd64-emu-$DEBIAN

TMP_DISK=dev-android-amd64-emu-tmpdisk
TMP_IMG=dev-android-amd64-emu-image
TMP_VM=dev-android-amd64-emu

# Create disk, forking our vmx-enabled image
gcloud compute disks delete $TMP_DISK --zone=$ZONE --quiet || true
gcloud compute disks create $TMP_DISK \
       --zone=$ZONE \
       --size=40GB \
       --image=debian-$DEBIAN-vmx

gcloud compute images delete $TMP_IMG --quiet || true
gcloud compute images create \
       $TMP_IMG \
       --source-disk=$TMP_DISK \
       --source-disk-zone=$ZONE

# No longer need that temp disk:
gcloud compute disks delete $TMP_DISK --zone=$ZONE --quiet

# Create the VM
gcloud compute instances delete --zone=$ZONE $TMP_VM --quiet || true
gcloud compute instances create \
       $TMP_VM \
       --zone=$ZONE \
       --image=$TMP_IMG \
       --min-cpu-platform "Intel Haswell" \
       --network default-vpc \
       --no-service-account --no-scopes

echo "Waiting for SSH port to be available..."
while ! gcloud compute ssh $TMP_VM --zone=$ZONE --tunnel-through-iap -- echo hi; do
    sleep 1
done

echo "SSH is up. Pulling docker container $CONTAINER on VM..."

gcloud compute ssh $TMP_VM --zone=$ZONE --tunnel-through-iap -- sudo docker pull gcr.io/symbolic-datum-552/android-amd64-emu:latest

echo "Done pulling; shutting down"

# Shut it down so it's a stable source to snapshot from.
gcloud compute instances stop $TMP_VM --zone=$ZONE

# Now make the new image from our instance's disk.
gcloud compute images delete $TARGET_IMAGE --quiet || true
gcloud compute images create $TARGET_IMAGE --source-disk=$TMP_VM --source-disk-zone=$ZONE

gcloud compute images delete $TMP_IMG --quiet


echo "Done."
