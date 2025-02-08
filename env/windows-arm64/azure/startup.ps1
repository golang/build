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

function Add-Path($Path) {
    $Path = [Environment]::GetEnvironmentVariable("PATH") + [IO.Path]::PathSeparator + $Path
    [Environment]::SetEnvironmentVariable( "PATH", $Path )
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
Set-Service -Name 'WSearch' -StartupType 'Disabled'
Stop-Service -Name 'WSearch'

$builder_dir = "C:\golang"
mkdir $builder_dir

# Download bootstrapswarm.exe
$bootstrapswarm_exe_path = "$builder_dir\bootstrapswarm.exe"
Write-Host "downloading bootstrapswarm to $bootstrapswarm_exe_path"
Get-FileFromUrl -URL 'https://storage.googleapis.com/go-builder-data/windows-arm64-bootstrapswarm.exe' -Output $bootstrapswarm_exe_path
dir $bootstrapswarm_exe_path

# Download luci_machine_tokend.exe
$tokend_exe_path = "$builder_dir\luci_machine_tokend.exe"
Write-Host "downloading luci_machine_tokend to $tokend_exe_path"
Get-FileFromUrl -URL 'https://storage.googleapis.com/go-builder-data/windows-arm64-luci_machine_tokend.exe' -Output $tokend_exe_path
dir $tokend_exe_path

# Install the OpenSSH Client
Add-WindowsCapability -Online -Name OpenSSH.Client
# Install the OpenSSH Server
Add-WindowsCapability -Online -Name OpenSSH.Server

Start-Service sshd
# OPTIONAL but recommended:
Set-Service -Name sshd -StartupType 'Automatic'

# Note: in previous versions of this script, we would download and install
# C/C++ compilers (https://storage.googleapis.com/go-builder-data/llvm-mingw-20220323-ucrt-aarch64.tar.gz), however in the LUCI case, compilers will be
# installed on the fly via CIPD. 

# Download and install Visual Studio Build Tools (MSVC)
# https://docs.microsoft.com/en-us/visualstudio/install/build-tools-container
Write-Host "downloading Visual Studio Build Tools"
$vs_buildtools = "$builder_dir\vs_buildtools.exe"
Get-FileFromUrl -URL "https://aka.ms/vs/16/release/vs_buildtools.exe" -Output "$vs_buildtools"

Write-Host "installing Visual Studio Build Tools"
$dep_dir = "C:\godep"
mkdir $dep_dir
& $vs_buildtools --quiet --wait --norestart --nocache --installPath "$dep_dir\vs" --all --add Microsoft.VisualStudio.Component.VC.Tools.ARM64 --add Microsoft.VisualStudio.Component.VC.Tools.ARM

# Download and install the root certificate used for crypto/x509 testing
Write-Host "downloading crypto/x509 test root"
$test_root = "$builder_dir\test_root.pem"
Get-FileFromUrl -URL "https://storage.googleapis.com/go-builder-data/platform_root_cert.pem" -Output "$test_root"

Write-Host "installing crypto/x509 test root"
Import-Certificate -FilePath "$test_root" -CertStoreLocation "Cert:\LocalMachine\Root"

# Install chocolatey, so as to install python via choco (preferred since
# it makes python available for all users).
Set-ExecutionPolicy Bypass -Scope Process -Force; [System.Net.ServicePointManager]::SecurityProtocol = [System.Net.ServicePointManager]::SecurityProtocol -bor 3072; iex ((New-Object System.Net.WebClient).DownloadString('https://community.chocolatey.org/install.ps1'))

# Install python
choco install python -y --version=3.11.3

# We need this module as well. Python dirs are not yet in our path, so
# insert them before this call.
Add-Path C:\Python311
Add-Path C:\Python311\Scripts
pip install pywin32

# "bootstrapswarm" requires that "python3" be on the path, not just "python",
# and annoyingly, choco only installed python.exe. Create a symbolic link
# by hand.
$here = Get-Location
Set-Location C:\Python311
New-Item -ItemType SymbolicLink -Path "python3.exe" -Target "python.exe"
Set-Location $here

# Create a tokend user (needs to be distinct from the swarming user).
Write-Host "creating tokend user"
$tokend_user = "tokend"
$tokend_password = "tokend"
net user $tokend_user $tokend_password /ADD
net localgroup administrators $tokend_user /ADD
Set-LocalUser -Name $tokend_user -PasswordNeverExpires $true

# Create swarming user
Write-Host "creating swarming user"
$swarming_user = "swarming"
$swarming_password = "swarming"
net user $swarming_user $swarming_password /ADD
net localgroup administrators $swarming_user /ADD
Set-LocalUser -Name $swarming_user -PasswordNeverExpires $true

# We need a swarming work directory in C:\b -- create that dir and
# make it owned by swarming.
$swarming_workdir = "C:\b"
mkdir $swarming_workdir
icacls $swarming_workdir /setowner swarming

# Download LUCI bot cert and place in proper location
$cert_dir = "C:\tokend"
mkdir $cert_dir
$cert_file = "$cert_dir\windows-arm64-azure-cert.txt"
Write-Host "downloading LUCI bot cert to $cert_file"
Get-FileFromUrl -URL "https://storage.googleapis.com/go-builder-data/windows-arm64-azure-1702406379-cert.txt" -Output "$cert_file"
dir $cert_file

# Note: the private key will live in C:\tokend\windows-arm64-azure-key.txt,
# however we'll distribute that to the builder in a separate step.

# Update ACLs on the cert/key to make them owned and readable only
# by tokend. [NB: many unsuccessful attempts at doing this in powershell,
# down down into cmd instead].
icacls $cert_file /setowner tokend
icacls $cert_file /deny swarming:r

# Set GO_BUILDER_NAME environment variable.
[Environment]::SetEnvironmentVariable('GO_BUILDER_NAME', 'windows-arm64-azure', [System.EnvironmentVariableTarget]::Machine)

# Tell the swarming bot not to reboot this machine. This appears to be
# needed to avoid having to re-disable anti-tamper security measures.
[Environment]::SetEnvironmentVariable('SWARMING_NEVER_REBOOT', 'true', [System.EnvironmentVariableTarget]::Machine)

# Path to the token.json file. Written by tokend, read by swarming.
$token_file = "$builder_dir\token.json"

# Set LUCI_MACHINE_TOKEN environment variable.
[Environment]::SetEnvironmentVariable('LUCI_MACHINE_TOKEN', "$token_file", [System.EnvironmentVariableTarget]::Machine)

# Create a tiny bat file that invokes tokend. This is mainly to
# avoid quoting problems when creating our scheduled task below, but
# also to log the date/time of the run for debugging purposes.
# *Important*: request ASCII content; without this we get UTF-16,
# which (ironically) can't be digested by windows 'cmd'.
$run_tokend_batfile = "$builder_dir\runtokend.bat"
$key_file = "$cert_dir\windows-arm64-azure-key.txt"
$cmd = "$tokend_exe_path -backend luci-token-server.appspot.com -cert-pem $cert_file -pkey-pem $key_file -token-file=$token_file"
$cmd | Out-File -Encoding ascii $run_tokend_batfile
Add-Content -Encoding ascii -Path $run_tokend_batfile -Value "echo %date% %time% >> $cert_dir\lastrun.txt"

# Create a scheduled task to run 'luci_machine_tokend.exe' every 10
# minutes to regenerate token.json.  Note that this scheduled task
# has to be run even when user "tokend" is not logged in, which requires
# a bit of extra work (via -LogonType option to New-ScheduledTaskPrincipal).
$task_action = New-ScheduledTaskAction -Execute $run_tokend_batfile
$task_trigger = New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 10)
$task_settings = New-ScheduledTaskSettingsSet -MultipleInstances Parallel
$principal = New-ScheduledTaskPrincipal -LogonType ServiceAccount -UserID "NT AUTHORITY\SYSTEM" -RunLevel Highest
$task = New-ScheduledTask -Action $task_action -Trigger $task_trigger -Settings $task_settings -Principal $principal
Register-ScheduledTask -TaskName 'Token Generator' -InputObject $task

# Run the swarming loop script on login
Write-Host "setting bootstrapswarm to run on start"
$bootstrap_cmd = "cmd /k $builder_dir\windows-arm64-bootstrapswarm-loop.bat"
New-ItemProperty -Path "HKLM:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "Swarming" -PropertyType ExpandString -Value "$bootstrap_cmd" -Force

# Setup autologon and reboot
$RegPath = "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon"
if ((Test-RegistryKeyExists $RegPath "DefaultUsername") -eq $false) {
  Write-Host "configuring auto login"
  Remove-ItemProperty -Path 'HKLM:SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon' -Name 'AutoLogonCount' -Force | Out-Null
  Set-ItemProperty $RegPath "AutoAdminLogon" -Value "1" -type String
  Set-ItemProperty $RegPath "DefaultUsername" -Value "$swarming_user" -type String
  Set-ItemProperty $RegPath "DefaultPassword" -Value "$swarming_password" -type String
  Set-ItemProperty $RegPath "LogonCount" -Value "99999999" -type String
  Write-Host "rebooting"
  shutdown /r /t 0
}
