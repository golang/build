#!/bin/bash

# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -eu

ZONE=us-central1-f
INSTANCE_NAME="${1:-golang-buildlet}"

# Set, fetch credentials
yes "Y" | gcloud compute reset-windows-password "${INSTANCE_NAME}" --user wingopher --project="${PROJECT_ID}" --zone="${ZONE}" > instance.txt

echo ""
echo "Instance credentials: "
echo ""
cat instance.txt

echo ""
echo "Connecting to instance: "
echo ""

username="$(grep username instance.txt | cut -d ':' -f 2 | xargs echo -n)"
password="$(grep password instance.txt | sed 's/password:\s*//' | xargs echo -n)"
hostname="$(grep ip_address instance.txt | cut -d ':' -f 2 | xargs echo -n)"

echo xfreerdp -u "${username}" -p "'${password}'" -n "${hostname}" --ignore-certificate "${hostname}"
xfreerdp -u "${username}" -p "${password}" -n "${hostname}" --ignore-certificate "${hostname}"
