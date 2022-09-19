# Darwin builders on AWS

Darwin builders on AWS run on [EC2 Mac
Instances](https://aws.amazon.com/ec2/instance-types/mac/), which are dedicated
Mac Mini hosts. These dedicated hosts must be allocated for at least 24 hours at
a time. They can be reimaged at any time while allocated, but the reimaging
process takes around an hour. Thus, for faster refresh time on hermetic
builders, we run buildlets as MacOS guests inside of QEMU on the dedicated
hosts.

## Creating a dedicated host

Note that if you simply need more instances, an AMI with the final state is
saved on the AWS account.

To bring up a new host:

1. In the EC2 console, go to "Dedicated Hosts" -> "Allocate Dedicated Host".
2. Configure host type, zone. Instance family `mac1` is amd64; `mac2` is arm64.
3. Once allocated and available, select the host and click "Actions" -> "Launch
   Instance(s) onto host".
4. Select a macOS AMI. If starting fresh, select the latest macOS version from
   "Quick Start". If simply adding more instances, a fully set-up AMI is saved
   in "My AMIs".
5. Select a "Key pair" for SSH access. `ec2-go-builders` for official builders,
   a custom key for testing. You will need the private key to login.
6. Configure a 200GB disk.
7. If creating from a fully set-up AMI, uncheck "Allow SSH Traffic".
7. Other settings can remain at default. Launch instance.

If creating from a fully set-up AMI, you are done!

SSH with the key pair using the "Public IPv4 DNS" address from the "Instances"
page. This won't appear until the instance is booted.

```sh
$ export KEY_PATH=~/.ssh/ec2-go-builders.pem
$ ssh -i $KEY_PATH ec2-user@$INSTANCE
```

[See the AWS
docs](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-mac-instances.html#mac-instance-vnc)
for setting up remote desktop access. Note that not all VNC client work with
Apple's server. [Remmina](https://remmina.org/) works.

The OS will only use 100GB of the disk by default. You must [increase the volume
size](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-mac-instances.html#mac-instance-increase-volume)
to utilize the full disk. This can be done while the disk is in use.

Continue below to create a new guest image, or [skip ahead](#guest-creation) to
use a pre-created image.

## Creating a new guest image

Steps to create a new QEMU macOS guest image:

1. Build (`make dist`) or
   [download](https://github.com/thenickdude/KVM-Opencore/releases) a copy of
   the OpenCore bootloader from
   https://github.com/thenickdude/KVM-Opencore.
     1. Grab the `.iso.gz` file, `gunzip` it, and rename to `opencore.img` (it
        is a raw disk image, not actually an `iso`).
2. Create a macOS recovery disk image:
   1. Clone https://github.com/kholia/OSX-KVM.
   2. `cd scripts/monterey && make Monterey-recovery.dmg`
3. Download the UTM QEMU fork and extract to `~/sysroot-macos-x86_64`.
   1. Available as `Sysroot-macos-x86_64` in
      https://github.com/utmapp/UTM/actions?query=event%3Arelease builds.
4. Create a disk image to install macOS to.
   1. `DYLD_LIBRARY_PATH="$HOME/sysroot-macos-x86_64/lib"
      "$HOME/sysroot-macos-x86_64/bin/qemu-img" create -f qcow2
      macos-monterey.qcow2 128G`
5. Determine the magic Apple OSK value.
   1. Either [read it directly from the machine](https://www.nicksherlock.com/2021/10/installing-macos-12-monterey-on-proxmox-7/#:~:text=Fetch%20the%20OSK%20authentication%20key), or [find it in some code](https://github.com/kholia/OSX-KVM/blob/master/OpenCore-Boot-macOS.sh#L45).
6. Copy the shell scripts from this directory to `$HOME`.
6. Use `$HOME/start-installer.sh macos-monterey.qcow2 opencore.img
   Monetery-recovery.dmg $OSK_VALUE` to launch the macOS installer in QEMU.

This starts QEMU with the display on a VNC server at `localhost:5901`. Use SSH
port forwarding to forward this to your local machine:

```
$ ssh -i $KEY_PATH -L 5901:localhost:5901 -N ec2-user@$INSTANCE
```

Then use a VNC client to connect to `localhost:5901`.

1. Once connected, select "macOS Base Image" from the bootloader to launch the
   installer.
2. In the installer, open the Disk Utility, find the ~128GB QEMU hard disk,
   click "Erase", name it "macOS", and leave other options at the default
   settings.
3. When formatting is complete, close Disk Utililty, and select "Reinstall
   macOs Monterey".
4. Click through the installer. The VM will reboot a few times. When it does,
   select "macOS Installer" from the bootloader to continue installation.
   Installation is complete when "macOS Installer" is replaced with "MacOS".
5. Select "macOS" and go through the macOS setup as described in the [generic
   setup notes](../setup-notes.md).

Once macOS is fully installed, we will install OpenCore on the primary disk and
configure it to autoboot to macOS.

1. In the guest, find the OpenCore and primary disks with `diskutil list`.
    * The OpenCore disk contains only one parition, of type "EFI". It is likely
      /dev/disk0, EFI partition /dev/disk0s1.
    * The primary disk contains two paritions, one of type "EFI", one of type
      "Apple_APFS". It is likely /dev/disk2, EFI partition /dev/disk2s1.
2. Copy the OpenCore EFI partition over the primary disk EFI partition.
    * `sudo dd if=/dev/disk0s1 of=/dev/disk2s1`
3. Mount the primary disk EFI partition to edit the configuration.
    * `sudo mkdir /Volumes/EFI`
    * `sudo mount -t msdos /dev/disk2s1 /Volumes/EFI`
4. Open `/Volumes/EFI/EFI/OC/config.plist`.
    * Change the Misc -> Boot -> Timeout option from `0` to `1` to set the
      bootloader to automatically boot macOS with a 1s delay.
    * In the `7C436110-AB2A-4BBB-A880-FE41995C9F82`, section, add `-v` to the
      `boot-args` string value. This applies the `nvram` option mentioned in
      [the setup notes](../setup-notes.md).
5. Shutdown the VM and boot it again with `start-mutable.sh`.

```sh
$ $HOME/start-mutable.sh macos-monterey.qcow2 $OSK_VALUE
```

Now complete the remainder of the [machine setup](../setup-notes.md).

Copy complete images to `s3://go-builder-data/darwin/` for use on other
builders.

## Set up automated guest creation {#guest-creation}

1. Download the latest image from `s3://go-builder-data/darwin/` and save it to
   `$HOME/macos.qcow2`.
2. Download the UTM QEMU fork and extract to `~/sysroot-macos-x86_64`.
   1. Available as `Sysroot-macos-x86_64` in
      https://github.com/utmapp/UTM/actions?query=event%3Arelease builds.
3. Copy `qemu.sh` and `start-snapshot.sh` to `$HOME`.
4. Create `$HOME/loop1.sh`:

```sh
#!/bin/bash

while true; do
  echo "Running QEMU..."
  $HOME/start-snapshot.sh $HOME/macos.qcow2 ${OSK_VALUE?} 1
done
```

4. Create `$HOME/loop2.sh`:

```sh
#!/bin/bash

while true; do
  echo "Running QEMU..."
  $HOME/start-snapshot.sh $HOME/macos.qcow2 ${OSK_VALUE?} 2
done
```

Replace `${OSK_VALUE?} with the OSK value described in the previous section.

5. Setup Automator:
  1. Open Automator
  2. File > New > Application
  3. Add "Run shell script"
  4. `open -a Terminal.app $HOME/loop1.sh`
  5. Save to desktop twice as `run-builder1`
  2. File > New > Application
  3. Add "Run shell script"
  4. `open -a Terminal.app $HOME/loop2.sh`
  5. Save to desktop twice as `run-builder2`

Note that loop1.sh and loop2.sh guests will have a display at VNC port 5901 and
5902, respectively.

6. Setup login:
  1. Users & Groups > ec2-user > Login Items > add run-builder1
  2. Users & Groups > ec2-user > Login Items > add run-builder2
  3. Users & Groups > Login Options > auto-login ec2-user
  4. Desktop & Screensaver > uncheck show screensaver

Once image is set up and working, stop the instance and create an AMI copy of
the instance. On the EC2 "Instances" page, select the instance, and click
"Actions" -> "Image and templates" -> "Create Image".

Either create a new instance from this image with no SSH access, or edit the
instance networking inbound rules to remove SSH access.


## References

* [EC2 Mac Instances USer
  Guide](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-mac-instances.html):
  Contains most of the useful how-tos on working with Mac instances.
* [Remmina](https://remmina.org/): VNC client that works with Apple's [VNC
  server](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-mac-instances.html#mac-instance-vnc)
  (not all clients work).
* [QEMU MacOS install
  guide](https://www.nicksherlock.com/2021/10/installing-macos-12-monterey-on-proxmox-7/):
  Guide with similar instructions for MacOS in QEMU.
