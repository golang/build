# Windows buildlet images

Windows images are built by creating and configuring VMs in GCP then capturing the image to the GCP Project.

The provisioning happens in two stages:
  - [sysprep.ps1](./sysprep.ps1): Downloads and unpacks dependencies, disabled unneeded Windows features (eg UAC)
  - [startup.ps1](./startup.ps1): Creates and configures user for unattended login to launch the buildlet

## Prerequisite: Setup a firewall rule
Allow traffic to instances tagged `allow-dev-access` on tcp:80, tcp:3389

```bash
# restrict this down to your local network
source_range=0.0.0.0/0

gcloud compute firewall-rules create --allow=tcp:80,tcp:3389 --target-tags allow-dev-access --source-ranges $source_range allow-dev-access
```

## Examples/Tools

### Build and test a single base image
Builds a buildlet from the BASE_IMAGE and sets it up with  and  An image is captured and then a new VM is created from that image and validated with [test_buildlet.bash](./test_buildlet.bash).

```bash
export PROJECT_ID=YOUR_GCP_PROJECT
export BASE_IMAGE=windows-server-2016-dc-core-v20170214
export IMAGE_PROJECT=windows-cloud

./build.bash
```

### Build all targets
```bash
./make.bash
```

### Build/test golang
```bash
instance_name=golang-buildlet-test
external_ip=$(gcloud compute instances describe golang-buildlet-test --project=${PROJECT_ID} --zone=${ZONE} --format="value(networkInterfaces[0].accessConfigs[0].natIP)")
./test_buildlet.bash $external_ip
```

### Troubleshoot via RDP
```bash
./connect.bash <instance_name>
```
