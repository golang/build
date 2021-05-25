# Windows buildlet images

Windows images are built by creating and configuring VMs hosted on AWS
a1.metal instances then saving the image manually.

## Build and test the Windows builder image

- Prepare the linux QEMU host image by following the instructions in
  `env/windows-arm64/aws`.
- Create an a1.metal instance (or other instance that supports KVM)
  in AWS.
- Download a Windows 10 ARM64 image.
    - Convert vhdx images to qcow2 via the following command:

      ```shell
      qemu-image convert -O qcow2 win.vhdx win.qcow2
      ```

- SSH to your instance tunneling port 5901, and run `win10-arm64.sh`
  script to boot the Windows VM.
    - You may need to stop the current VM: `sudo systemctl stop qemu`
- VNC to the tunneled port 5901.
- Open the device control panel, and use the "Search for Drivers"
  button to search the virtio drive `D:` for drivers.
    - Matching drivers will be automatically installed.
    - This is necessary for networking to work on Windows in qemu.
- Download the `startup.ps1` script to the Windows instance, and run
  in PowerShell. Check thoroughly for errors.
    - Alternatively, you can modify `win10-arm64.sh` to forward ssh
      access to the VM, and run PowerShell in the CLI, which is a bit
      easier than through VNC.
- Once the image is complete, download the image to your workstation
  and upload to `s3://go-builder-data`.
    - You can find the appropriate the S3 path referenced in
      `env/windows-arm64/aws/prepare_image.sh`.
- Re-run packer to build an AMI with your updated Windows image.

### Notes

- `QEMU_EFI.fd` is from the `qemu-efi-aarch64` Debian package, found
  at `/usr/share/qemu-efi-aarch64/QEMU_EFI.fd`. It can be regenerated
  with the following command:

  ```shell
  dd if=/dev/zero of=QEMU_EFI.fd bs=1M count=64
  dd if=/usr/share/qemu-efi-aarch64/QEMU_EFI.fd of=QEMU_EFI.fd bs=1M count=64 conv=notrunc
  ```

- `QEMU_VARS.fd` stores saved EFI state when booting a VM. It's
  generated via the following command:

  ```shell
  dd if=/dev/zero of=QEMU_VARS.fd bs=1M count=64
  ```

- The latest virtio driver image can be fetched from:
  https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/latest-virtio/virtio-win.iso

- `win10-arm64.sh` is hard-coded to run with 4 processors instead of
  the 16 available on an a1.metal instance. Higher numbers of
  processors are causing a fatal CLOCK_WATCHDOG_TIMEOUT error from
  interrupt requests not arriving in time. qemu-system-x86_64 has a
  workaround for this. We're still investigating how to increase this
  on aarch64.
