<!---
# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
-->

# Azure windows arm64 VM setup notes

This doc contains notes on setup info for deploying windows arm64 Go builders on Azure.

## Prerequisites

You'll need to install the Azure CLI toolset ("az *" commands) to take the various actions (VM creation, setup) below.  You'll also need a Microsoft account into order to do anything with "az" and/or to log into the azure website (e.g. portal.asure.com); recommendation is to use your golang.org account.

## CLI install

Although you can try to install the Azure CLI using "sudo apt-get install azure-cli", this version winds up being broken/non-functional.  Make sure this version is uninstalled via "sudo apt-get remove azure-cli", then install the CLI via

  pip install azure-cli

## Authentication

Authenticate with "az login".

## VM strategy for Azure

At the moment, windows-arm64 Azure VMs are configured as reverse builders, and they are set up with no public IP address and no exposed ports. To interact with the VMs directly (e.g. to log in and poke around) it is recommended to use the Azure "bastion" feature, which provides RDP-like access to VMs from within the portal.  If you need to log in, use the account "gopheradmin" (password in Valentine). You can also run PowerShell scripts on the deployed VMs via "az vm run-command invoke --command-id=RunPowerShellScript ... --scripts @scriptpath" to perform upkeep operations.

## Deployment VM creation

Deployment VMs are set up with invocations of the following az CLI command:

```
az vm create \
  --name=MyNewVmName \
  --resource-group=dev_buildlets \
  --admin-username=gopheradmin \
  --admin-password=<password from valentine> \
  --image=microsoftwindowsdesktop:windows11preview-arm64:win11-22h2-ent:latest \
  --nsg-rule=NONE \
  --size=Standard_D8ps_v5 \
  --subscription=<set subscription ID here> \
  --public-ip-address ""
```

and then configure as described below in VM setup. This VM will have no public IP address or open ports, thus will be usable only by the coordinator. 

Notes:
* the "image" argument above is arm-specific, and in addition "size" argument also encodes the arm64-ness of the VM (strangely)


## VM setup

Once a VM has been created, you can apply Go-specific configuration to it by running the setup script in this directory (startup.ps1), using this command:

```
az vm run-command invoke \
    --command-id=RunPowerShellScript \
    --name="MyNewVM" \
    --resource-group=dev_buildlets \
    --scripts @startup.ps1
```

Where "startup.ps1" is the path (on your local machine) to the script to be run on the Azure VM, and the value passed to "--name" is the one you used when creating the VM.

Notes:

* output from the command is in JSON
* exit status of the "az" command does NOT accurately reflect exit status of the powershell script.

## Follow-ons to disable antivirus

In later versions of windows, it can be very difficult to completely disable the system's antivirus software, due to "features" such as [tamper protection](https://learn.microsoft.com/en-us/microsoft-365/security/defender-endpoint/prevent-changes-to-security-settings-with-tamper-protection?view=o365-worldwide), which make it almost impossible to programmatically turn off windows defender (and which ensure that any changes made are undone when the system reboots).

Running this command should help somewhat:

```
az vm run-command invoke \
    --command-id=RunPowerShellScript \
    --name="MyNewVM" \
    --resource-group=dev_buildlets \
    --scripts @antivirusadditions.ps1
```

## First login

Log into the new builder as "gopher" at least once so as to go through the "initial login" Windows workflow.

## Builder key

Generate a builder key for the VMs according to the directions in [x/build/cmd/genbuilderkey](https://go.googlesource.com/build/+/fdfb99e1de1f68b555502056567be459d98a0e71/cmd/genbuilderkey/README.md).

Once the key is available, write it to the builder (via "az vm run-command invoke" as above) using a PowerShell script of the form

```
Write-Host "writing builder key"

$key = "<insert key here>"
$key | Out-File -Encoding ascii -FilePath C:\Users\gopher\.gobuildkey-host-windows11-arm64-azure
```

## Debugging/testing VM creation

To create a new windows-arm64 VM named "MyNewVM" that is net accessible (e.g. with a public IP and ssh port exposed), use this command:

```
az vm create \
  --name=MyNewVM \
  --resource-group=dev_buildlets \
  --admin-username=<pick your admin account name> \
  --admin-password=<pick password> \
  --image=microsoftwindowsdesktop:windows11preview-arm64:win11-22h2-ent:latest \
  --nsg-rule=SSH \
  --size=Standard_D8ps_v5 \
  --subscription=<set subscription ID here> \
  --public-ip-sku Standard
```

Notes:

* be sure to pick a very strong password
* configure the VM once created as in `VM Setup` above, but with the section that starts the stage0 buildlet commented out (since we don't want the VM to connect to the coordinator)
* delete VM when you are finished with it

