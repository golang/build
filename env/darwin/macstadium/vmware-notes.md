* Create a new virtual machine stored in GGLGTM*, with the most recent 
  supported version of macOS as the guest OS. Configure it with 2 CPUs,
  4 GB RAM, 60+ GiB of disk, and mount the installer ISO from ISO/OSX.
* Setup OS X following image-setup-notes.txt.
* Shut it down.
* Clone to Virtual Machine (convention: "osx_amd64_11_0_frozen" for macOS
  11.0")
* Snapshot that new frozen VM once to make its vmdk in COW format.
* Clone it again to _frozen_nfs; nobody quite knows why we do this
  but it's the required format for makemac.

Then change makemac to know about the new OS, and add the new reverse builders
and build to the coordinator.

Other misc notes:

```bash
$ source govc_env_file
$ govc vm.info -json mac_11_0_amd64_host07a | jq . | grep MacAdd
              "MacAddress": "00:50:56:b4:05:57",
```

if sleep failing,
```bash
sudo pmset -a hibernatemode 25
sudo pmset sleepnow
```

```bash
pmset -g assertions     # RemovableMedia mounted
system_profiler  # to see which
```

https://kb.vmware.com/selfservice/microsites/search.do?language=en_US&cmd=displayKC&externalId=1012225
  ... doesn't seem to work
  ... but changing from SATA to IDE does make the RemovableMedia assertion go away (but `pmset sleepnow` stil doesn't work)
