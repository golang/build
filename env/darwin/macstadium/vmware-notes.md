* Setup OS X.
* Shut it down.
* Clone to Virtual Machine (convention: "osx_11_frozen" for OS X
  10.11")
* Snapshot that new frozen VM once to make its vmdk in COW format.

Then, to create more:

* 10.14:

```bash
export VMHOST=4
export VMWHICH=b
export VMNAME="mac_10_14_host0${VMHOST}_${VMWHICH}"
govc vm.create -m 4096 -c 6 -on=false -net dvPortGroup-Private -g darwin18_64Guest -ds "BOOT_$VMHOST" $VMNAME
govc vm.change -e smc.present=TRUE -e ich7m.present=TRUE -e firmware=efi -e guestinfo.key-host-darwin-10_14=$(cat $HOME/keys/host-darwin-10_14) -e guestinfo.name=$VMNAME -vm $VMNAME
govc device.usb.add -vm $VMNAME
govc vm.disk.attach -vm $VMNAME -link=true -persist=false -ds GGLGLN-A-001-STV1 -disk osx_14_frozen_nfs/osx_14_frozen_nfs_17.vmdk
govc vm.power -on $VMNAME
```

* 10.x:

Change MINOR to target minor version (14, 12, 11, 10, and 8 are supported)

```bash
export MINOR=14
export VMHOST=4
export VMWHICH=b
export GUEST_TYPE=darwin$(expr $MINOR + 4)_64Guest # (14: darwin18, 12: darwin16...)
export VMNAME="mac_10_${MINOR}_host0${VMHOST}_${VMWHICH}"
export SNAPSHOT=$(govc vm.info -json osx_${MINOR}_frozen_nfs | jq -r '.VirtualMachines[0].Layout.Snapshot[0].SnapshotFile|.[]|match(" .+vmdk$").string')
govc vm.create -m 4096 -c 6 -on=false -net dvPortGroup-Private -g darwin16_64Guest -ds "BOOT_$VMHOST" $VMNAME
govc vm.change -e smc.present=TRUE -e ich7m.present=TRUE -e firmware=efi -e guestinfo.key-host-darwin-10_$MINOR=$(cat $HOME/keys/host-darwin-10_${MINOR}) -e guestinfo.name=$VMNAME -vm $VMNAME
govc device.usb.add -vm $VMNAME
govc vm.disk.attach -vm $VMNAME -link=true -persist=false -ds GGLGLN-A-001-STV1 -disk $SNAPSHOT
govc vm.power -on $VMNAME
```

Other misc notes:

```bash
$ govc vm.info -json osx11_host12_a | jq . | grep MacAdd
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


