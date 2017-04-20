# Copyright 2017 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

Set-StrictMode -Version Latest

# Helpers
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

# Disable automatic updates, windows firewall, error reporting, and UAC
# 
# - They'll just interrupt the builds later. 
# - We don't care about security since this isn't going to be Internet-facing. 
# - No ports will be accessible once the image is built.
# - We can be trusted to run as a real Administrator
New-ItemProperty "HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU" -Name NoAutoUpdate -Value 1 -Force | Out-Null
new-ItemProperty "HKLM:\SOFTWARE\Microsoft\Windows\Windows Error Reporting" -Name Disabled -Value 1 -Force | Out-Null
new-ItemProperty "HKLM:\SOFTWARE\Microsoft\Windows\Windows Error Reporting" -Name DontShowUI -Value 1 -Force | Out-Null
netsh advfirewall set allprofiles state off
netsh firewall set opmode mode=disable profile=ALL
New-ItemProperty -Path HKLM:Software\Microsoft\Windows\CurrentVersion\policies\system -Name EnableLUA -PropertyType DWord -Value 0 -Force | Out-Null

# Download buildlet
$url = "https://storage.googleapis.com/go-builder-data/buildlet-stage0.windows-amd64.untar"
$builder_dir = "C:\golang"
$bootstrap_exe_path = "$builder_dir\bootstrap.exe"
mkdir $builder_dir
Get-FileFromUrl -URL $url -Output $bootstrap_exe_path

# Download and unpack dependencies
$dep_dir = "C:\godep"
$gcc32_tar = "$dep_dir\gcc32.tar.gz"
$gcc64_tar = "$dep_dir\gcc64.tar.gz"
mkdir $dep_dir
Get-FileFromUrl -URL "https://storage.googleapis.com/godev/gcc5-1-tdm32.tar.gz" -Output "$gcc32_tar"
Get-FileFromUrl -URL "https://storage.googleapis.com/godev/gcc5-1-tdm64.tar.gz" -Output "$gcc64_tar"

# Extract GCC
$extract32_args=@("--untar-file=$gcc32_tar", "--untar-dest-dir=$dep_dir")
& $bootstrap_exe_path $extract32_args 
$extract64_args=@("--untar-file=$gcc64_tar", "--untar-dest-dir=$dep_dir")
& $bootstrap_exe_path $extract64_args 
