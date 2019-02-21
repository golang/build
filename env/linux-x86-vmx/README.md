# linux-x86-vmx

These scripts create a GCE VM image that acts like Container-Optimized
Linux but uses a Debian 9 (Stretch) kernel + userspace instead. We do
this because Debian 9 includes CONFIG_KVM for nested virtualization,
whereas that's not compiled in for Container-Optimized Linux.
