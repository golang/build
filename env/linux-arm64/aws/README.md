# AWS Linux ARM64 Builders

## Machines

The AWS builders use the a1 instance types which are arm64 based machines of varying specifications.
The base type used will be a1.xlarge 4 vCPUs, 8192 MiB.

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

## Buildlet Image

Buildlet images with stage0 installed can be created via:

Prod:

`make prod`

Staging:

`make staging`
