# AWS Windows ARM64 Builders

## Machines

The AWS builders use the a1 instance types which are arm64 machines.
The base type used will be a1.metal, which are the cheapest which
expose KVM support.

## Machine image

Machine images are stored on AWS EBS service as a snapshot. New VMs
can use the snapshot as an image by providing the AMI ID as the base
image when a new VM is created.

### Creating a new Ubuntu host image

Requirements:

Two environment variables are required to be set before initiating
the command: `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` should be
set with the appropriate values.

The [packer](https://www.packer.io) binary should be in `PATH`.

Command:

`make create-aws-image`

or

`AWS_ACCESS_KEY_ID=<id> AWS_SECRET_ACCESS_KEY=<secret> make create-aws-image`
