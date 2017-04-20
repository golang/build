#!/bin/bash

# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -eu

ZONE="us-central1-f"
BUILDER_PREFIX="${1-golang}"
IMAGE_NAME="${1-${BASE_IMAGE}}"
INSTANCE_NAME="${BUILDER_PREFIX}-buildlet"
TEST_INSTANCE_NAME="${BUILDER_PREFIX}-buildlet-test"
MACHINE_TYPE="n1-standard-4"
BUILDLET_IMAGE="windows-amd64-${IMAGE_NAME}"
IMAGE_PROJECT=$IMAGE_PROJECT
BASE_IMAGE=$BASE_IMAGE

function wait_for_buildlet() {
  external_ip=$1
  seconds=5

  echo "Waiting for buildlet at ${external_ip} to become responsive"
  until curl "http://${external_ip}" 2>/dev/null; do
    echo "retrying ${external_ip} in ${seconds} seconds"
    sleep "${seconds}"
  done
}

#
# 0. Cleanup images/instances from prior runs
#
echo "Destroying existing instances (if exists)"
yes "Y" | gcloud compute instances delete "$INSTANCE_NAME" --project="$PROJECT_ID" --zone="$ZONE" || true
yes "Y" | gcloud compute instances delete "$TEST_INSTANCE_NAME" --project="$PROJECT_ID" --zone="$ZONE" || true
echo "Destroying existing image (if exists)"
yes "Y" | gcloud compute images delete "$BUILDLET_IMAGE" --project="$PROJECT_ID" || true


#
# 1. Create base instance
# 
echo "Creating target instance"
gcloud compute instances create --machine-type="$MACHINE_TYPE" "$INSTANCE_NAME" \
        --image "$BASE_IMAGE" --image-project "$IMAGE_PROJECT" \
        --project="$PROJECT_ID" --zone="$ZONE" \
        --metadata="buildlet-binary-url=https://storage.googleapis.com/go-builder-data/buildlet.windows-amd64" \
        --metadata-from-file=sysprep-specialize-script-ps1=sysprep.ps1,windows-startup-script-ps1=startup.ps1 --tags=allow-dev-access 

echo ""
echo "Fetch logs with:"
echo ""
echo gcloud compute instances get-serial-port-output "$INSTANCE_NAME" --zone="$ZONE" --project="$PROJECT_ID" 
echo ""
external_ip=$(gcloud compute instances describe "$INSTANCE_NAME" --project="$PROJECT_ID" --zone="$ZONE" --format="value(networkInterfaces[0].accessConfigs[0].natIP)")

wait_for_buildlet "$external_ip"

#
# 2. Image base instance
#

echo "Shutting down instance"
gcloud compute instances stop "$INSTANCE_NAME" \
        --project="$PROJECT_ID" --zone="$ZONE"

echo "Capturing image"
gcloud compute images create "$BUILDLET_IMAGE" --source-disk "$INSTANCE_NAME" --source-disk-zone "$ZONE" --project="$PROJECT_ID"

echo "Removing base machine"
yes "Y" | gcloud compute instances delete "$INSTANCE_NAME" --project="$PROJECT_ID" --zone="$ZONE" || true

#
# 3. Verify image is valid
#

echo "Creating new machine with image"
gcloud compute instances create --machine-type="$MACHINE_TYPE" --image "$BUILDLET_IMAGE" "$TEST_INSTANCE_NAME" \
       --project="$PROJECT_ID" --metadata="buildlet-binary-url=https://storage.googleapis.com/go-builder-data/buildlet.windows-amd64" \
       --tags=allow-dev-access --zone="$ZONE"

test_image_ip=$(gcloud compute instances describe "$TEST_INSTANCE_NAME" --project="$PROJECT_ID" --zone="$ZONE" --format="value(networkInterfaces[0].accessConfigs[0].natIP)")
wait_for_buildlet "$test_image_ip"

echo "Performing test build"
./test_buildlet.bash "$test_image_ip"

echo "Removing test instance"
yes "Y" | gcloud compute instances delete "$TEST_INSTANCE_NAME" --project="$PROJECT_ID" --zone="$ZONE" || true

echo "Success! A new buildlet can be created with the following command"
echo "gcloud compute instances create --machine-type='$MACHINE_TYPE' '$INSTANCE_NAME' \
--metadata='buildlet-binary-url=https://storage.googleapis.com/go-builder-data/buildlet.windows-amd64' \
--image '$BUILDLET_IMAGE' --image-project '$PROJECT_ID'"
