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
  --resource-group=<dev/prod>_buildlets \
  --admin-username=gopheradmin \
  --admin-password=<password from valentine> \
  --image=microsoftwindowsdesktop:windows11preview-arm64:win11-22h2-ent:latest \
  --nsg=<dev/prod>_buildlets-security-group \
  --size=Standard_D4ps_v5 \
  --subscription=<Development/Production> \
  --public-ip-address ""
```

and then configure as described below in VM setup. This VM will have no public IP address or open ports, thus will be usable only by the coordinator. 

Notes:
* the "image" argument above is arm-specific, and in addition "size" argument also encodes the arm64-ness of the VM (strangely)

## VM setup (part 1 of 3)

Once a VM has been created, you can apply Go-specific configuration to it by running the setup script in this directory (startup.ps1), using this command:

```
az vm run-command invoke \
    --command-id=RunPowerShellScript \
    --name="MyNewVM" \
    --subscription=<Development/Production> \
    --resource-group=<dev/prod>_buildlets \
    --scripts @startup.ps1
```

Where "startup.ps1" is the path (on your local machine) to the script to be run on the Azure VM, and the value passed to "--name" is the one you used when creating the VM.

Notes:

* output from the command is in JSON
* exit status of the "az" command does NOT accurately reflect exit status of the powershell script.
* errors about things already existing are expected

## VM setup (part 2 of 3)

Each VM instance needs a unique hostname; this is handled by starting with a base hostname of "windows-arm64-azure" and then tacking on a numeric "--NN" suffix.  To enable the VM to use the correct hostname, the next step is to write a "bootstrapswarm boot loop" script of the following form for the VM (where `INSTANCE` is replaced with a unique numeric value, such as "00" or "01"):

```
if %username%==swarming goto loop
exit 0
:loop
@echo Invoking bootstrapswarm.exe at %date% %time% on %computername%
C:\golang\bootstrapswarm.exe -hostname windows-arm64-azure--<INSTANCE>
timeout 10
goto loop
```

The following PowerShell script will write out a script of the proper form to the file "C:\golang\windows-arm64-bootstrapswarm-loop.bat" on the VM (hostname will vary of course):

```
Write-Host "Writing windows-arm64-bootstrapswarm-loop.bat"

mkdir C:\golang

$path = "C:\golang\windows-arm64-bootstrapswarm-loop.bat"
$line = "rem boostrapswarm loop script"
$hostname | Out-File -Encoding ascii -FilePath $path

$line = "if %username%==swarming goto loop"
Add-Content -Encoding ascii -Path $path -Value $line
$line = "exit 0"
Add-Content -Encoding ascii -Path $path -Value $line
$line = ":loop"
Add-Content -Encoding ascii -Path $path -Value $line
$line = "@echo Invoking bootstrapswarm.exe at %date% %time% on %computername%"
Add-Content -Encoding ascii -Path $path -Value $line
$line = "C:\golang\bootstrapswarm.exe -hostname windows-arm64-azure--<INSTANCE>"
Add-Content -Encoding ascii -Path $path -Value $line
$line = "timeout 10"
Add-Content -Encoding ascii -Path $path -Value $line
$line = "goto loop"
Add-Content -Encoding ascii -Path $path -Value $line
```

Run the script with "az vm run-command invoke" as with the startup script above.

## VM setup (part 3 of 3)

As a final step, you will need to distribute a copy of the private builder key to the VM (for details on keys, see https://github.com/golang/go/wiki/DashboardBuilders#luci-builders).  Because the VM created in step 1 does not have a public IP, we can't use ssh/scp to copy in the file, so instead the recommendation is to do the transfer using "writefilegenpowerscript.go", steps below.

```
# Copy key from valentine to a local file
$ cp ... windows-arm64-azure-key.txt
# Encode into powershell script
$ go build writefilegenpowerscript.go
$ ./writefilegenpowerscript -input-file windows-arm64-azure-key.txt -output-file transferFile.ps1 -windows-target-path "C:\tokend\windows-arm64-azure-key.txt" -set-owner tokend -deny-user-read swarming
$ ls transferFile.ps1
transferFile.ps1
$ az vm run-command invoke \
    --command-id=RunPowerShellScript \
    --name="MyNewVM" \
    --subscription=<Development/Production> \
    --resource-group=<dev/prod>_buildlets \
    --scripts @transferFile.ps1
```

## First login

Log into the new builder as "swarming" at least once so as to go through the "initial login" Windows workflow. Find the VM in the Azure portal, and enter the login in the Bastion section. Choose "no" on all the setup prompts.

Check to make sure that the scheduled task to run "luci_machine_tokend.exe" every 10 minutes is working properly. You can do this by looking for the presence of the "C:\golang\token.json" file.

## Follow-ons to disable antivirus

In later versions of windows, it can be very difficult to completely disable the system's antivirus software, due to "features" such as [tamper protection](https://learn.microsoft.com/en-us/microsoft-365/security/defender-endpoint/prevent-changes-to-security-settings-with-tamper-protection?view=o365-worldwide), which make it almost impossible to programmatically turn off windows defender (and which ensure that any changes made are undone when the system reboots).

Open Windows Security, Virus & threat protection, Manage settings, and turn off Tamper Protection. Then run this command:

```
az vm run-command invoke \
    --command-id=RunPowerShellScript \
    --name="MyNewVM" \
    --subscription=<Development/Production> \
    --resource-group=<prod/dev>_buildlets \
    --scripts @antivirusadditions.ps1
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
* configure the VM once created as in `VM Setup` above, but with the section that starts boostrapswarm on login commented out (since we don't want the VM to connect to the LUCI)
* delete VM when you are finished with it

