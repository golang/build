# Copyright 2022 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

Set-StrictMode -Version Latest

# Helpers
function Test-RegistryKeyExists($path, $name)
{
  $key = Get-Item -LiteralPath $path -ErrorAction SilentlyContinue
  ($key -and $null -ne $key.GetValue($name, $null)) -ne $false
}

function Get-FileFromUrl(
  [string] $URL,
  [string] $Output)
{
  Add-Type -AssemblyName "System.Net.Http"

  $client = New-Object System.Net.Http.HttpClient
  $request = New-Object System.Net.Http.HttpRequestMessage -ArgumentList @([System.Net.Http.HttpMethod]::Get, $URL)
  $responseMsg = $client.SendAsync($request)
  $responseMsg.Wait()

  if (!$responseMsg.IsCanceled)
  {
    $response = $responseMsg.Result
    if ($response.IsSuccessStatusCode)
    {
      $downloadedFileStream = [System.IO.File]::Create($Output)
      $copyStreamOp = $response.Content.CopyToAsync($downloadedFileStream)
      $copyStreamOp.Wait()
      $downloadedFileStream.Close()
      if ($copyStreamOp.Exception -ne $null)
      {
    throw $copyStreamOp.Exception
      }
    }
  }
}

# https://social.technet.microsoft.com/Forums/ie/en-US/29508e4e-a2b5-42eb-9729-6eca473716ae/disabling-password-complexity-via-command?forum=ITCG
function Disable-PasswordComplexity
{
  param()

  $secEditPath = [System.Environment]::ExpandEnvironmentVariables("%SystemRoot%\system32\secedit.exe")
  $tempFile = [System.IO.Path]::GetTempFileName()

  $exportArguments = '/export /cfg "{0}" /quiet' -f $tempFile
  $importArguments = '/configure /db secedit.sdb /cfg "{0}" /quiet' -f $tempFile

  Start-Process -FilePath $secEditPath -ArgumentList $exportArguments -Wait

  $currentConfig = Get-Content -Path $tempFile

  $currentConfig = $currentConfig -replace 'PasswordComplexity = .', 'PasswordComplexity = 0'
  $currentConfig = $currentConfig -replace 'MinimumPasswordLength = .', 'MinimumPasswordLength = 0'
  $currentConfig | Out-File -FilePath $tempFile

  Start-Process -FilePath $secEditPath -ArgumentList $importArguments -Wait

  Remove-Item -Path .\secedit.sdb
  Remove-Item -Path $tempFile
}

# Wait till network comes up
while(-Not (Test-NetConnection 8.8.8.8 -Port 53 | ? { $_.TcpTestSucceeded })) {
  Write-Host "waiting for network (external network) to come up"
  sleep 3
}

# Disable password complexity, automatic updates, windows firewall, error reporting, and UAC
#
# - Update can interrupt the builds
# - We don't care about security since this isn't going to be Internet-facing
# - No ports will ever be accessible externally
# - We can be trusted to run as a real Administrator
Write-Host "disabling security features"
New-Item -Path "HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate"
New-Item -Path "HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU"
New-ItemProperty -Path "HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU" -Name NoAutoUpdate -Value 1 -Force | Out-Null
New-ItemProperty -Path "HKLM:\SOFTWARE\Microsoft\Windows\Windows Error Reporting" -Name Disabled -Value 1 -Force | Out-Null
New-ItemProperty -Path "HKLM:\SOFTWARE\Microsoft\Windows\Windows Error Reporting" -Name DontShowUI -Value 1 -Force | Out-Null
New-ItemProperty -Path "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\policies\system" -Name EnableLUA -PropertyType DWord -Value 0 -Force | Out-Null
netsh advfirewall set allprofiles state off
netsh firewall set opmode mode=disable profile=ALL
Set-MpPreference -DisableRealtimeMonitoring $true

# Disable unwanted services
Write-Host "disabling unused services"
Set-Service -Name 'NlaSvc' -StartupType 'Disabled'
Set-Service -Name 'LanmanServer' -StartupType 'Disabled'
Set-Service -Name 'BITS' -StartupType 'Disabled'
Set-Service -Name 'DPS' -StartupType 'Disabled'
Set-Service -Name 'MSDTC' -StartupType 'Disabled'
Set-Service -Name 'IKEEXT' -StartupType 'Disabled'
Set-Service -Name 'RemoteRegistry' -StartupType 'Disabled'
Set-Service -Name 'lmhosts' -StartupType 'Disabled'

# Download buildlet
Write-Host "downloading stage0"
$builder_dir = "C:\golang"
$bootstrap_exe_path = "$builder_dir\bootstrap.exe"
mkdir $builder_dir
Get-FileFromUrl -URL 'https://storage.googleapis.com/go-builder-data/buildlet-stage0.windows-arm64' -Output $bootstrap_exe_path

# Install the OpenSSH Client
Add-WindowsCapability -Online -Name OpenSSH.Client
# Install the OpenSSH Server
Add-WindowsCapability -Online -Name OpenSSH.Server

Start-Service sshd
# OPTIONAL but recommended:
Set-Service -Name sshd -StartupType 'Automatic'

# Download and unpack LLVM
Write-Host "downloading LLVM"
$dep_dir = "C:\godep"
$llvm64_tar = "$dep_dir\llvm64.tar.gz"
mkdir $dep_dir
Get-FileFromUrl -URL "https://storage.googleapis.com/go-builder-data/llvm-mingw-20220323-ucrt-aarch64.tar.gz" -Output "$llvm64_tar"

Write-Host "extracting LLVM"
$extract64_args=@("--untar-file=$llvm64_tar", "--untar-dest-dir=$dep_dir")
& $bootstrap_exe_path $extract64_args

$builder_dir = "C:\golang"
$bootstrap_exe_path = "$builder_dir\bootstrap.exe"

# Download and install Visual Studio Build Tools (MSVC)
# https://docs.microsoft.com/en-us/visualstudio/install/build-tools-container
Write-Host "downloading Visual Studio Build Tools"
$vs_buildtools = "$builder_dir\vs_buildtools.exe"
Get-FileFromUrl -URL "https://aka.ms/vs/16/release/vs_buildtools.exe" -Output "$vs_buildtools"

Write-Host "installing Visual Studio Build Tools"
& $vs_buildtools --quiet --wait --norestart --nocache --installPath "$dep_dir\vs" --all --add Microsoft.VisualStudio.Component.VC.Tools.ARM64 --add Microsoft.VisualStudio.Component.VC.Tools.ARM

# Create a buildlet user
Write-Host "creating buildlet user"
$buildlet_user = "gopher"
$buildlet_password = "gopher"
net user $buildlet_user $buildlet_password /ADD
net localgroup administrators $buildlet_user /ADD
Set-LocalUser -Name $buildlet_user -PasswordNeverExpires $true

# Set GO_BUILDER_NAME environment variable (needed by the stage0 buildlet);
# this setting needs to persist across reboots.
[Environment]::SetEnvironmentVariable('GO_BUILDER_ENV', 'host-windows11-arm64-azure', [System.EnvironmentVariableTarget]::Machine)

# Run the bootstrap program on login
Write-Host "setting stage0 to run on start"
$bootstrap_cmd = "cmd /k ""cd $builder_dir && $bootstrap_exe_path"""
New-ItemProperty -Path "HKLM:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "Buildlet" -PropertyType ExpandString -Value $bootstrap_cmd -Force

# Setup autologon and reboot
$RegPath = "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon"
if ((Test-RegistryKeyExists $RegPath "DefaultUsername") -eq $false) {
  Write-Host "configuring auto login"
  Remove-ItemProperty -Path 'HKLM:SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon' -Name 'AutoLogonCount' -Force | Out-Null
  Set-ItemProperty $RegPath "AutoAdminLogon" -Value "1" -type String
  Set-ItemProperty $RegPath "DefaultUsername" -Value "$buildlet_user" -type String
  Set-ItemProperty $RegPath "DefaultPassword" -Value "$buildlet_password" -type String
  Set-ItemProperty $RegPath "LogonCount" -Value "99999999" -type String
  Write-Host "rebooting"
  shutdown /r /t 0
}

