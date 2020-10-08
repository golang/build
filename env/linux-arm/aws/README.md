# AWS Linux ARM Builder

## Machines

The AWS builders use the m6 instance types which are arm64 based machines of varying specifications.
The base type used will be m6g.xlarge 4 vCPUs, 16384 MiB.

## Machine Image

Machine images are stored on AWS EBS service as a snapshot. New VMs can use the snapshot as an image
by providing the AMI ID as the base image when a new VM is created. The machine image will be configured
to install and initialize rundockerbuildlet.

## Buildlet Container Image

Buildlet container images must be build on an arm/arm64 instance with the proper credentials. The instructions
are as follows:

*  In your normal gcloud dev environment, retrieve a short-lived access token:

  `you@dev:~$ gcloud auth print-access-token`

*  On an arm64 instance, clone the build repository.

*  cd into the `env/linux-arm/aws` directory.

*  Execute: `make prod-push`

*  When prompted for your password, paste in the access token from the first step.

*  Ensure `/root/.docker/config.json` has been deleted.
