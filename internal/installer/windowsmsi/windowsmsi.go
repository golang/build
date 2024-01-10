// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package windowsmsi encodes the process of building a Windows MSI
// installer from the given Go toolchain .tar.gz binary archive.
package windowsmsi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/build/internal/untar"
)

// InstallerOptions holds options for constructing the installer.
type InstallerOptions struct {
	GOARCH string // The target GOARCH.
}

// ConstructInstaller constructs an installer for the provided Go toolchain .tar.gz
// binary archive using workDir as a working directory, and returns the output path.
//
// It's intended to run on a Windows system where the WiX tools can run.
func ConstructInstaller(_ context.Context, workDir, tgzPath string, opt InstallerOptions) (msiPath string, _ error) {
	var errs []error
	if opt.GOARCH == "" {
		errs = append(errs, fmt.Errorf("GOARCH is empty"))
	}
	if err := errors.Join(errs...); err != nil {
		return "", err
	}

	oldDir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	if err := os.Chdir(workDir); err != nil {
		panic(err)
	}
	defer func() {
		if err := os.Chdir(oldDir); err != nil {
			panic(err)
		}
	}()

	fmt.Println("Extracting the Go toolchain .tar.gz binary archive.")
	putTar(tgzPath, ".")
	version := readVERSION("go")

	fmt.Println("\nInstalling WiX tools.")
	const wixDir = "wix"
	switch opt.GOARCH {
	default:
		if err := installWix(wixRelease311, wixDir); err != nil {
			return "", err
		}
	case "arm", "arm64":
		if err := installWix(wixRelease314, wixDir); err != nil {
			return "", err
		}
	}

	fmt.Println("\nWriting out installer data used by the packaging process.")
	const winDir = "windows"
	if err := writeDataFiles(windowsData, winDir); err != nil {
		return "", err
	}

	fmt.Println("\nGathering files (running wix heat).")
	const goDir = "go"
	appFiles := filepath.Join(winDir, "AppFiles.wxs")
	if err := run(filepath.Join(wixDir, "heat"),
		"dir", goDir,
		"-nologo",
		"-gg", "-g1", "-srd", "-sfrag", "-sreg",
		"-cg", "AppFiles",
		"-template", "fragment",
		"-dr", "INSTALLDIR",
		"-var", "var.SourceDir",
		"-out", appFiles,
	); err != nil {
		return "", err
	}

	fmt.Println("\nBuilding package (running wix candle).")
	verMajor, verMinor, err := splitVersion(version)
	if err != nil {
		return "", fmt.Errorf("failed to split version %q: %v", version, err)
	}
	var msArch string
	switch opt.GOARCH {
	case "386":
		msArch = "x86"
	case "amd64":
		msArch = "x64"
	case "arm":
		// Historically the installer for the windows/arm port
		// used the same value as for the windows/arm64 port.
		fallthrough
	case "arm64":
		msArch = "arm64"
	default:
		panic("unknown arch for windows " + opt.GOARCH)
	}
	if err := run(filepath.Join(wixDir, "candle"),
		"-nologo",
		"-arch", msArch,
		"-dGoVersion="+version,
		fmt.Sprintf("-dGoMajorVersion=%v", verMajor),
		fmt.Sprintf("-dWixGoVersion=1.%v.%v", verMajor, verMinor),
		"-dArch="+opt.GOARCH,
		"-dSourceDir="+goDir,
		filepath.Join(winDir, "installer.wxs"),
		appFiles,
	); err != nil {
		return "", err
	}

	fmt.Println("\nLinking the .msi installer (running wix light).")
	if err := os.Mkdir("msi-out", 0755); err != nil {
		return "", err
	}
	if err := run(filepath.Join(wixDir, "light"),
		"-b", winDir,
		"-nologo",
		"-dcl:high",
		"-ext", "WixUIExtension",
		"-ext", "WixUtilExtension",
		"AppFiles.wixobj",
		"installer.wixobj",
		"-o", filepath.Join("msi-out", version+"-unsigned.msi"),
	); err != nil {
		return "", err
	}

	return filepath.Join(workDir, "msi-out", version+"-unsigned.msi"), nil
}

type wixRelease struct {
	BinaryURL string
	SHA256    string
}

var (
	wixRelease311 = wixRelease{
		BinaryURL: "https://storage.googleapis.com/go-builder-data/wix311-binaries.zip",
		SHA256:    "da034c489bd1dd6d8e1623675bf5e899f32d74d6d8312f8dd125a084543193de",
	}
	wixRelease314 = wixRelease{
		BinaryURL: "https://storage.googleapis.com/go-builder-data/wix314-binaries.zip",
		SHA256:    "34dcbba9952902bfb710161bd45ee2e721ffa878db99f738285a21c9b09c6edb", // WiX v3.14.0.4118 release, SHA 256 of wix314-binaries.zip from https://wixtoolset.org/releases/v3-14-0-4118/.
	}
)

// installWix fetches and installs the wix toolkit to the specified path.
func installWix(wix wixRelease, path string) error {
	// Fetch wix binary zip file.
	body, err := httpGet(wix.BinaryURL)
	if err != nil {
		return err
	}

	// Verify sha256.
	sum := sha256.Sum256(body)
	if fmt.Sprintf("%x", sum) != wix.SHA256 {
		return errors.New("sha256 mismatch for wix toolkit")
	}

	// Unzip to path.
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		name := filepath.FromSlash(f.Name)
		err := os.MkdirAll(filepath.Join(path, filepath.Dir(name)), 0755)
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		err = os.WriteFile(filepath.Join(path, name), b, 0644)
		if err != nil {
			return err
		}
	}

	return nil
}

func httpGet(url string) ([]byte, error) {
	r, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return nil, err
	}
	if r.StatusCode != 200 {
		return nil, errors.New(r.Status)
	}
	return body, nil
}

func putTar(tgz, dir string) {
	f, err := os.Open(tgz)
	if err != nil {
		panic(err)
	}
	err = untar.Untar(f, dir)
	if err != nil {
		panic(err)
	}
	err = f.Close()
	if err != nil {
		panic(err)
	}
}

// run runs the specified command.
// It prints the command line.
func run(name string, args ...string) error {
	fmt.Printf("$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

var versionRE = regexp.MustCompile(`^go1\.(\d+(\.\d+)?)`)

// splitVersion splits a Go version string such as "go1.23.4" or "go1.24rc1"
// (as matched by versionRE) into its parts: major and minor.
func splitVersion(v string) (major, minor int, _ error) {
	m := versionRE.FindStringSubmatch(v)
	if len(m) < 2 {
		return 0, 0, fmt.Errorf("no regexp match")
	}
	parts := strings.Split(m[1], ".")
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parsing major part: %v", err)
	}
	if len(parts) >= 2 {
		var err error
		minor, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("parsing minor part: %v", err)
		}
	}
	return major, minor, nil
}

const storageBase = "https://storage.googleapis.com/go-builder-data/release/"

// writeDataFiles writes the files in the provided map to the provided base
// directory. If the map value is a URL it fetches the data at that URL and
// uses it as the file contents.
func writeDataFiles(data map[string]string, base string) error {
	for name, body := range data {
		dst := filepath.Join(base, name)
		err := os.MkdirAll(filepath.Dir(dst), 0755)
		if err != nil {
			return err
		}
		b := []byte(body)
		if strings.HasPrefix(body, storageBase) {
			b, err = httpGet(body)
			if err != nil {
				return err
			}
		}
		// (We really mean 0755 on the next line; some of these files
		// are executable, and there's no harm in making them all so.)
		if err := os.WriteFile(dst, b, 0755); err != nil {
			return err
		}
	}
	return nil
}

var windowsData = map[string]string{

	"installer.wxs": `<?xml version="1.0" encoding="UTF-8"?>
<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
<!--
# Copyright 2010 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
-->

<?if $(var.Arch) = 386 ?>
  <?define UpgradeCode = {1C3114EA-08C3-11E1-9095-7FCA4824019B} ?>
  <?define InstallerVersion="300" ?>
  <?define SysFolder=SystemFolder ?>
  <?define ArchProgramFilesFolder="ProgramFilesFolder" ?>
<?elseif $(var.Arch) = arm64 ?>
  <?define UpgradeCode = {21ade9a3-3fdd-4ba6-bea6-c85abadc9488} ?>
  <?define InstallerVersion="500" ?>
  <?define SysFolder=System64Folder ?>
  <?define ArchProgramFilesFolder="ProgramFiles64Folder" ?>
<?else?>
  <?define UpgradeCode = {22ea7650-4ac6-4001-bf29-f4b8775db1c0} ?>
  <?define InstallerVersion="300" ?>
  <?define SysFolder=System64Folder ?>
  <?define ArchProgramFilesFolder="ProgramFiles64Folder" ?>
<?endif?>

<Product
    Id="*"
    Name="Go Programming Language $(var.Arch) $(var.GoVersion)"
    Language="1033"
    Version="$(var.WixGoVersion)"
    Manufacturer="https://go.dev"
    UpgradeCode="$(var.UpgradeCode)" >

<Package
    Id='*'
    Keywords='Installer'
    Description="The Go Programming Language Installer"
    Comments="The Go programming language is an open source project to make programmers more productive."
    InstallerVersion="$(var.InstallerVersion)"
    Compressed="yes"
    InstallScope="perMachine"
    Languages="1033" />

<Property Id="ARPCOMMENTS" Value="The Go programming language is a fast, statically typed, compiled language that feels like a dynamically typed, interpreted language." />
<Property Id="ARPCONTACT" Value="golang-nuts@googlegroups.com" />
<Property Id="ARPHELPLINK" Value="https://go.dev/help" />
<Property Id="ARPREADME" Value="https://go.dev" />
<Property Id="ARPURLINFOABOUT" Value="https://go.dev" />
<Property Id="LicenseAccepted">1</Property>
<Icon Id="gopher.ico" SourceFile="images\gopher.ico"/>
<Property Id="ARPPRODUCTICON" Value="gopher.ico" />
<Property Id="EXISTING_GOLANG_INSTALLED">
  <RegistrySearch Id="installed" Type="raw" Root="HKCU" Key="Software\GoProgrammingLanguage" Name="installed" />
</Property>
<MediaTemplate EmbedCab="yes" CompressionLevel="high" MaximumUncompressedMediaSize="10" />
<?if $(var.GoMajorVersion) < 21 ?>
<Condition Message="Windows 7 (with Service Pack 1) or greater required.">
    ((VersionNT > 601) OR (VersionNT = 601 AND ServicePackLevel >= 1))
</Condition>
<?else?>
<Condition Message="Windows 10 or greater required.">
<!-- In true MS fashion, Windows 10 pretends to be windows 8.1.
	See https://learn.microsoft.com/en-us/troubleshoot/windows-client/application-management/versionnt-value-for-windows-10-server .
	Workarounds exist, but seem difficult/flaky.
	1) We could build a "bootstrapper" with wix burn, but then we'll be building .exes and there might be implications to that.
	2) We can try one of the things listed here: https://stackoverflow.com/q/31932646 but that takes us back to https://github.com/wixtoolset/issues/issues/5824 and needing a bootstrapper.
	So we're stuck with checking for 8.1.
-->
    (VersionNT >= 603)
</Condition>
<?endif?>
<MajorUpgrade AllowDowngrades="yes" />

<CustomAction
    Id="SetApplicationRootDirectory"
    Property="ARPINSTALLLOCATION"
    Value="[INSTALLDIR]" />

<!-- Define the directory structure and environment variables -->
<Directory Id="TARGETDIR" Name="SourceDir">
  <Directory Id="$(var.ArchProgramFilesFolder)">
    <Directory Id="INSTALLDIR" Name="Go"/>
  </Directory>
  <Directory Id="ProgramMenuFolder">
    <Directory Id="GoProgramShortcutsDir" Name="Go Programming Language"/>
  </Directory>
  <Directory Id="EnvironmentEntries">
    <Directory Id="GoEnvironmentEntries" Name="Go Programming Language"/>
  </Directory>
</Directory>

<!-- Programs Menu Shortcuts -->
<DirectoryRef Id="GoProgramShortcutsDir">
  <Component Id="Component_GoProgramShortCuts" Guid="{f5fbfb5e-6c5c-423b-9298-21b0e3c98f4b}">
    <Shortcut
        Id="UninstallShortcut"
        Name="Uninstall Go"
        Description="Uninstalls Go and all of its components"
        Target="[$(var.SysFolder)]msiexec.exe"
        Arguments="/x [ProductCode]" />
    <RemoveFolder
        Id="GoProgramShortcutsDir"
        On="uninstall" />
    <RegistryValue
        Root="HKCU"
        Key="Software\GoProgrammingLanguage"
        Name="ShortCuts"
        Type="integer"
        Value="1"
        KeyPath="yes" />
  </Component>
</DirectoryRef>

<!-- Registry & Environment Settings -->
<DirectoryRef Id="GoEnvironmentEntries">
  <Component Id="Component_GoEnvironment" Guid="{3ec7a4d5-eb08-4de7-9312-2df392c45993}">
    <RegistryKey
        Root="HKCU"
        Key="Software\GoProgrammingLanguage">
            <RegistryValue
                Name="installed"
                Type="integer"
                Value="1"
                KeyPath="yes" />
            <RegistryValue
                Name="installLocation"
                Type="string"
                Value="[INSTALLDIR]" />
    </RegistryKey>
    <Environment
        Id="GoPathEntry"
        Action="set"
        Part="last"
        Name="PATH"
        Permanent="no"
        System="yes"
        Value="[INSTALLDIR]bin" />
    <Environment
        Id="UserGoPath"
        Action="create"
        Name="GOPATH"
        Permanent="no"
        Value="%USERPROFILE%\go" />
    <Environment
        Id="UserGoPathEntry"
        Action="set"
        Part="last"
        Name="PATH"
        Permanent="no"
        Value="%USERPROFILE%\go\bin" />
    <RemoveFolder
        Id="GoEnvironmentEntries"
        On="uninstall" />
  </Component>
</DirectoryRef>

<!-- Install the files -->
<Feature
    Id="GoTools"
    Title="Go"
    Level="1">
      <ComponentRef Id="Component_GoEnvironment" />
      <ComponentGroupRef Id="AppFiles" />
      <ComponentRef Id="Component_GoProgramShortCuts" />
</Feature>

<!-- Update the environment -->
<InstallExecuteSequence>
    <Custom Action="SetApplicationRootDirectory" Before="InstallFinalize" />
</InstallExecuteSequence>

<!-- Notify top level applications of the new PATH variable (go.dev/issue/18680)  -->
<CustomActionRef Id="WixBroadcastEnvironmentChange" />

<!-- Include the user interface -->
<WixVariable Id="WixUILicenseRtf" Value="LICENSE.rtf" />
<WixVariable Id="WixUIBannerBmp" Value="images\Banner.jpg" />
<WixVariable Id="WixUIDialogBmp" Value="images\Dialog.jpg" />
<Property Id="WIXUI_INSTALLDIR" Value="INSTALLDIR" />
<UIRef Id="Golang_InstallDir" />
<UIRef Id="WixUI_ErrorProgressText" />

</Product>
<Fragment>
  <!--
    The installer steps are modified so we can get user confirmation to uninstall an existing golang installation.

    WelcomeDlg  [not installed]  =>                  LicenseAgreementDlg => InstallDirDlg  ..
                [installed]      => OldVersionDlg => LicenseAgreementDlg => InstallDirDlg  ..
  -->
  <UI Id="Golang_InstallDir">
    <!-- style -->
    <TextStyle Id="WixUI_Font_Normal" FaceName="Tahoma" Size="8" />
    <TextStyle Id="WixUI_Font_Bigger" FaceName="Tahoma" Size="12" />
    <TextStyle Id="WixUI_Font_Title" FaceName="Tahoma" Size="9" Bold="yes" />

    <Property Id="DefaultUIFont" Value="WixUI_Font_Normal" />
    <Property Id="WixUI_Mode" Value="InstallDir" />

    <!-- dialogs -->
    <DialogRef Id="BrowseDlg" />
    <DialogRef Id="DiskCostDlg" />
    <DialogRef Id="ErrorDlg" />
    <DialogRef Id="FatalError" />
    <DialogRef Id="FilesInUse" />
    <DialogRef Id="MsiRMFilesInUse" />
    <DialogRef Id="PrepareDlg" />
    <DialogRef Id="ProgressDlg" />
    <DialogRef Id="ResumeDlg" />
    <DialogRef Id="UserExit" />
    <Dialog Id="OldVersionDlg" Width="240" Height="95" Title="[ProductName] Setup" NoMinimize="yes">
      <Control Id="Text" Type="Text" X="28" Y="15" Width="194" Height="50">
        <Text>A previous version of Go Programming Language is currently installed. By continuing the installation this version will be uninstalled. Do you want to continue?</Text>
      </Control>
      <Control Id="Exit" Type="PushButton" X="123" Y="67" Width="62" Height="17"
        Default="yes" Cancel="yes" Text="No, Exit">
        <Publish Event="EndDialog" Value="Exit">1</Publish>
      </Control>
      <Control Id="Next" Type="PushButton" X="55" Y="67" Width="62" Height="17" Text="Yes, Uninstall">
        <Publish Event="EndDialog" Value="Return">1</Publish>
      </Control>
    </Dialog>

    <!-- wizard steps -->
    <Publish Dialog="BrowseDlg" Control="OK" Event="DoAction" Value="WixUIValidatePath" Order="3">1</Publish>
    <Publish Dialog="BrowseDlg" Control="OK" Event="SpawnDialog" Value="InvalidDirDlg" Order="4"><![CDATA[NOT WIXUI_DONTVALIDATEPATH AND WIXUI_INSTALLDIR_VALID<>"1"]]></Publish>

    <Publish Dialog="ExitDialog" Control="Finish" Event="EndDialog" Value="Return" Order="999">1</Publish>

    <Publish Dialog="WelcomeDlg" Control="Next" Event="NewDialog" Value="OldVersionDlg"><![CDATA[EXISTING_GOLANG_INSTALLED << "#1"]]> </Publish>
    <Publish Dialog="WelcomeDlg" Control="Next" Event="NewDialog" Value="LicenseAgreementDlg"><![CDATA[NOT (EXISTING_GOLANG_INSTALLED << "#1")]]></Publish>

    <Publish Dialog="OldVersionDlg" Control="Next" Event="NewDialog" Value="LicenseAgreementDlg">1</Publish>

    <Publish Dialog="LicenseAgreementDlg" Control="Back" Event="NewDialog" Value="WelcomeDlg">1</Publish>
    <Publish Dialog="LicenseAgreementDlg" Control="Next" Event="NewDialog" Value="InstallDirDlg">LicenseAccepted = "1"</Publish>

    <Publish Dialog="InstallDirDlg" Control="Back" Event="NewDialog" Value="LicenseAgreementDlg">1</Publish>
    <Publish Dialog="InstallDirDlg" Control="Next" Event="SetTargetPath" Value="[WIXUI_INSTALLDIR]" Order="1">1</Publish>
    <Publish Dialog="InstallDirDlg" Control="Next" Event="DoAction" Value="WixUIValidatePath" Order="2">NOT WIXUI_DONTVALIDATEPATH</Publish>
    <Publish Dialog="InstallDirDlg" Control="Next" Event="SpawnDialog" Value="InvalidDirDlg" Order="3"><![CDATA[NOT WIXUI_DONTVALIDATEPATH AND WIXUI_INSTALLDIR_VALID<>"1"]]></Publish>
    <Publish Dialog="InstallDirDlg" Control="Next" Event="NewDialog" Value="VerifyReadyDlg" Order="4">WIXUI_DONTVALIDATEPATH OR WIXUI_INSTALLDIR_VALID="1"</Publish>
    <Publish Dialog="InstallDirDlg" Control="ChangeFolder" Property="_BrowseProperty" Value="[WIXUI_INSTALLDIR]" Order="1">1</Publish>
    <Publish Dialog="InstallDirDlg" Control="ChangeFolder" Event="SpawnDialog" Value="BrowseDlg" Order="2">1</Publish>

    <Publish Dialog="VerifyReadyDlg" Control="Back" Event="NewDialog" Value="InstallDirDlg" Order="1">NOT Installed</Publish>
    <Publish Dialog="VerifyReadyDlg" Control="Back" Event="NewDialog" Value="MaintenanceTypeDlg" Order="2">Installed AND NOT PATCH</Publish>
    <Publish Dialog="VerifyReadyDlg" Control="Back" Event="NewDialog" Value="WelcomeDlg" Order="2">Installed AND PATCH</Publish>

    <Publish Dialog="MaintenanceWelcomeDlg" Control="Next" Event="NewDialog" Value="MaintenanceTypeDlg">1</Publish>

    <Publish Dialog="MaintenanceTypeDlg" Control="RepairButton" Event="NewDialog" Value="VerifyReadyDlg">1</Publish>
    <Publish Dialog="MaintenanceTypeDlg" Control="RemoveButton" Event="NewDialog" Value="VerifyReadyDlg">1</Publish>
    <Publish Dialog="MaintenanceTypeDlg" Control="Back" Event="NewDialog" Value="MaintenanceWelcomeDlg">1</Publish>

    <Property Id="ARPNOMODIFY" Value="1" />
  </UI>

  <UIRef Id="WixUI_Common" />
</Fragment>
</Wix>
`,

	"LICENSE.rtf":           storageBase + "windows/LICENSE.rtf",
	"images/Banner.jpg":     storageBase + "windows/Banner.jpg",
	"images/Dialog.jpg":     storageBase + "windows/Dialog.jpg",
	"images/DialogLeft.jpg": storageBase + "windows/DialogLeft.jpg",
	"images/gopher.ico":     storageBase + "windows/gopher.ico",
}

// readVERSION reads the VERSION file and
// returns the first line of the file, the Go version.
func readVERSION(goroot string) (version string) {
	b, err := os.ReadFile(filepath.Join(goroot, "VERSION"))
	if err != nil {
		panic(err)
	}
	version, _, _ = strings.Cut(string(b), "\n")
	return version
}
