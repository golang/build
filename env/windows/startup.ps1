# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

Set-StrictMode -Version Latest

function Test-RegistryKeyExists($path, $name)
{
    $key = Get-Item -LiteralPath $path -ErrorAction SilentlyContinue
    ($key -and $null -ne $key.GetValue($name, $null)) -ne $false
}

$builder_dir = "C:\golang"
$bootstrap_exe_path = "$builder_dir\bootstrap.exe"

# Create a buildlet user
$buildlet_user = "buildlet"
$buildlet_password = "bUi-dL3ttt"
net user $buildlet_user $buildlet_password /ADD
net localgroup administrators $buildlet_user /ADD

# Run the bootstrap program on login
$bootstrap_cmd = "cmd /k ""cd $builder_dir && $bootstrap_exe_path"""
New-ItemProperty -Path "HKLM:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "Buildlet" -PropertyType ExpandString -Value $bootstrap_cmd -Force

# Setup autologon and reboot
$RegPath = "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon"
if ((Test-RegistryKeyExists $RegPath "DefaultUsername") -eq $false) {
  Remove-ItemProperty -Path 'HKLM:SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon' -Name 'AutoLogonCount' -Force
  Set-ItemProperty $RegPath "AutoAdminLogon" -Value "1" -type String 
  Set-ItemProperty $RegPath "DefaultUsername" -Value "$buildlet_user" -type String 
  Set-ItemProperty $RegPath "DefaultPassword" -Value "$buildlet_password" -type String
  Set-ItemProperty $RegPath "LogonCount" -Value "99999999" -type String
  shutdown /r /t 0
}
