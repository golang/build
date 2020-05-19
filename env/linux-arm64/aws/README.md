# AWS Linux ARM64 Builders

## Machines

The AWS builders use the m6 instance types which are arm64 based machines of varying specifications.
The base type used will be m6g.xlarge 4 vCPUs, 16384 MiB.

## Machine Image

Machine images are stored on AWS EBS service as a snapshot. New VMs can use the snapshot as an image
by providing the AMI ID as the base image when a new VM is created. The machine image will be configured
to install and initialize rundockerbuildlet.

### Creating a New Image

Requirements:

Two environmental variables are required to be set before initiating the command:
`AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` should be set with the appropriate values.

The [packer](https://www.packer.io) binary should be in `PATH`.

Command:

`make create-aws-image`

or

`AWS_ACCESS_KEY_ID=<id> AWS_SECRET_ACCESS_KEY=<secret> make create-aws-image`

## Buildlet Container Image

Buildlet container images must be build on an arm64 instance with the proper credentials. The instructions
are as follows:

*  In your normal gcloud dev environment, retrieve a short-lived access token:

  `you@dev:~$ gcloud auth print-access-token`

*  On an arm64 instance, clone the build repository.

*  cd into the `env/linux-arm64/aws` directory.

*  Execute: `make prod-push`

*  When prompted for your password, paste in the access token from the first step.

*  Ensure `/root/.docker/config.json` has been deleted.
