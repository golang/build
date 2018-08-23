// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build ignore

// Command releaselet does buildlet-side release construction tasks.
// It is intended to be executed on the buildlet preparing a release.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

func main() {
	if v, _ := strconv.ParseBool(os.Getenv("RUN_RELEASELET_TESTS")); v {
		runSelfTests()
		return
	}

	if err := godoc(); err != nil {
		log.Fatal(err)
	}
	if err := tour(); err != nil {
		log.Fatal(err)
	}
	if dir := archDir(); dir != "" {
		if err := cp("go/bin/go", "go/bin/"+dir+"/go"); err != nil {
			log.Fatal(err)
		}
		if err := cp("go/bin/gofmt", "go/bin/"+dir+"/gofmt"); err != nil {
			log.Fatal(err)
		}
		os.RemoveAll("go/bin/" + dir)
		os.RemoveAll("go/pkg/linux_amd64")
		os.RemoveAll("go/pkg/tool/linux_amd64")
	}
	os.RemoveAll("go/pkg/obj")
	var err error
	switch runtime.GOOS {
	case "windows":
		// Clean up .exe~ files; golang.org/issue/23894
		filepath.Walk("go", func(path string, fi os.FileInfo, err error) error {
			if strings.HasSuffix(path, ".exe~") {
				os.Remove(path)
			}
			return nil
		})
		err = windowsMSI()
	case "darwin":
		err = darwinPKG()
	}
	if err != nil {
		log.Fatal(err)
	}
}

func archDir() string {
	if os.Getenv("GO_BUILDER_NAME") == "linux-s390x-crosscompile" {
		return "linux_s390x"
	}
	return ""
}

func godoc() error {
	// Pre Go 1.7, the godoc binary is placed here by cmd/go.
	// After Go 1.7, we need to copy the binary from GOPATH/bin to GOROOT/bin.
	// TODO(cbro): Remove after Go 1.6 is no longer supported.
	dst := filepath.FromSlash("go/bin/godoc" + ext())
	if _, err := os.Stat(dst); err == nil {
		return nil
	}

	// Copy godoc binary to $GOROOT/bin.
	return cp(
		dst,
		filepath.FromSlash("gopath/bin/"+archDir()+"/godoc"+ext()),
	)
}

const tourPath = "golang.org/x/tour"

var tourContent = []string{
	"content",
	"solutions",
	"static",
	"template",
}

var tourPackages = []string{
	"pic",
	"reader",
	"tree",
	"wc",
}

// TODO: Remove after Go 1.13 is released, and Go 1.11 is no longer supported.
func tour() error {
	_, version, _ := environ()
	verMajor, verMinor, _ := splitVersion(version)
	if verMajor > 1 || verMinor >= 12 {
		return nil // Only include the tour in go1.11.x and earlier releases.
	}

	tourSrc := filepath.Join("gopath/src", tourPath)
	contentDir := filepath.FromSlash("go/misc/tour")

	// Copy all the tour content to $GOROOT/misc/tour.
	if err := cpAllDir(contentDir, tourSrc, tourContent...); err != nil {
		return err
	}

	// Copy the tour source code so it's accessible with $GOPATH pointing to $GOROOT/misc/tour.
	tourPKGDir := filepath.Join(contentDir, "src", tourPath)
	if err := cpAllDir(tourPKGDir, tourSrc, tourPackages...); err != nil {
		return err
	}

	// Copy gotour binary to tool directory as "tour"; invoked as "go tool tour".
	return cp(
		filepath.FromSlash("go/pkg/tool/"+runtime.GOOS+"_"+runtime.GOARCH+"/tour"+ext()),
		filepath.FromSlash("gopath/bin/"+archDir()+"/gotour"+ext()),
	)
}

func environ() (cwd, version string, err error) {
	cwd, err = os.Getwd()
	if err != nil {
		return
	}
	var versionBytes []byte
	versionBytes, err = ioutil.ReadFile("go/VERSION")
	if err != nil {
		return
	}
	version = string(bytes.TrimSpace(versionBytes))
	return
}

func darwinPKG() error {
	cwd, version, err := environ()
	if err != nil {
		return err
	}

	// Write out darwin data that is used by the packaging process.
	defer os.RemoveAll("darwin")
	if err := writeDataFiles(darwinData, "darwin"); err != nil {
		return err
	}

	// Create a work directory and place inside the files as they should
	// be on the destination file system.
	work := filepath.Join(cwd, "darwinpkg")
	if err := os.MkdirAll(work, 0755); err != nil {
		return err
	}
	defer os.RemoveAll(work)

	// Write out /etc/paths.d/go.
	const pathsBody = "/usr/local/go/bin"
	pathsDir := filepath.Join(work, "etc/paths.d")
	pathsFile := filepath.Join(pathsDir, "go")
	if err := os.MkdirAll(pathsDir, 0755); err != nil {
		return err
	}
	if err = ioutil.WriteFile(pathsFile, []byte(pathsBody), 0644); err != nil {
		return err
	}

	// Copy Go installation to /usr/local/go.
	goDir := filepath.Join(work, "usr/local/go")
	if err := os.MkdirAll(goDir, 0755); err != nil {
		return err
	}
	if err := cpDir(goDir, "go"); err != nil {
		return err
	}

	// Build the package file.
	dest := "package"
	if err := os.Mkdir(dest, 0755); err != nil {
		return err
	}
	defer os.RemoveAll(dest)

	if err := run("pkgbuild",
		"--identifier", "com.googlecode.go",
		"--version", version,
		"--scripts", "darwin/scripts",
		"--root", work,
		filepath.Join(dest, "com.googlecode.go.pkg"),
	); err != nil {
		return err
	}

	const pkg = "pkg" // known to cmd/release
	if err := os.Mkdir(pkg, 0755); err != nil {
		return err
	}
	return run("productbuild",
		"--distribution", "darwin/Distribution",
		"--resources", "darwin/Resources",
		"--package-path", dest,
		filepath.Join(cwd, pkg, "go.pkg"), // file name irrelevant
	)
}

func windowsMSI() error {
	cwd, version, err := environ()
	if err != nil {
		return err
	}

	// Install Wix tools.
	wix := filepath.Join(cwd, "wix")
	defer os.RemoveAll(wix)
	if err := installWix(wix); err != nil {
		return err
	}

	// Write out windows data that is used by the packaging process.
	win := filepath.Join(cwd, "windows")
	defer os.RemoveAll(win)
	if err := writeDataFiles(windowsData, win); err != nil {
		return err
	}

	// Gather files.
	goDir := filepath.Join(cwd, "go")
	appfiles := filepath.Join(win, "AppFiles.wxs")
	if err := runDir(win, filepath.Join(wix, "heat"),
		"dir", goDir,
		"-nologo",
		"-gg", "-g1", "-srd", "-sfrag",
		"-cg", "AppFiles",
		"-template", "fragment",
		"-dr", "INSTALLDIR",
		"-var", "var.SourceDir",
		"-out", appfiles,
	); err != nil {
		return err
	}

	msArch := func() string {
		switch runtime.GOARCH {
		default:
			panic("unknown arch for windows " + runtime.GOARCH)
		case "386":
			return "x86"
		case "amd64":
			return "x64"
		}
	}

	// Build package.
	verMajor, verMinor, verPatch := splitVersion(version)

	if err := runDir(win, filepath.Join(wix, "candle"),
		"-nologo",
		"-arch", msArch(),
		"-dGoVersion="+version,
		fmt.Sprintf("-dWixGoVersion=%v.%v.%v", verMajor, verMinor, verPatch),
		fmt.Sprintf("-dIsWinXPSupported=%v", wixIsWinXPSupported(version)),
		"-dArch="+runtime.GOARCH,
		"-dSourceDir="+goDir,
		filepath.Join(win, "installer.wxs"),
		appfiles,
	); err != nil {
		return err
	}

	msi := filepath.Join(cwd, "msi") // known to cmd/release
	if err := os.Mkdir(msi, 0755); err != nil {
		return err
	}
	return runDir(win, filepath.Join(wix, "light"),
		"-nologo",
		"-dcl:high",
		"-ext", "WixUIExtension",
		"-ext", "WixUtilExtension",
		"AppFiles.wixobj",
		"installer.wixobj",
		"-o", filepath.Join(msi, "go.msi"), // file name irrelevant
	)
}

const wixBinaries = "https://storage.googleapis.com/go-builder-data/wix311-binaries.zip"
const wixSha256 = "da034c489bd1dd6d8e1623675bf5e899f32d74d6d8312f8dd125a084543193de"

// installWix fetches and installs the wix toolkit to the specified path.
func installWix(path string) error {
	// Fetch wix binary zip file.
	body, err := httpGet(wixBinaries)
	if err != nil {
		return err
	}

	// Verify sha256
	sum := sha256.Sum256(body)
	if fmt.Sprintf("%x", sum) != wixSha256 {
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
		b, err := ioutil.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		err = ioutil.WriteFile(filepath.Join(path, name), b, 0644)
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
	body, err := ioutil.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return nil, err
	}
	if r.StatusCode != 200 {
		return nil, errors.New(r.Status)
	}
	return body, nil
}

func run(name string, arg ...string) error {
	cmd := exec.Command(name, arg...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func runDir(dir, name string, arg ...string) error {
	cmd := exec.Command(name, arg...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func cp(dst, src string) error {
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()
	fi, err := sf.Stat()
	if err != nil {
		return err
	}
	tmpDst := dst + ".tmp"
	df, err := os.Create(tmpDst)
	if err != nil {
		return err
	}
	defer df.Close()
	// Windows doesn't implement Fchmod.
	if runtime.GOOS != "windows" {
		if err := df.Chmod(fi.Mode()); err != nil {
			return err
		}
	}
	_, err = io.Copy(df, sf)
	if err != nil {
		return err
	}
	if err := df.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpDst, dst); err != nil {
		return err
	}
	// Ensure the destination has the same mtime as the source.
	return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}

func cpDir(dst, src string) error {
	walk := func(srcPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, srcPath[len(src):])
		if info.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}
		return cp(dstPath, srcPath)
	}
	return filepath.Walk(src, walk)
}

func cpAllDir(dst, basePath string, dirs ...string) error {
	for _, dir := range dirs {
		if err := cpDir(filepath.Join(dst, dir), filepath.Join(basePath, dir)); err != nil {
			return err
		}
	}
	return nil
}

func ext() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

var versionRe = regexp.MustCompile(`^go(\d+(\.\d+)*)`)

// splitVersion splits a Go version string such as "go1.9" or "go1.10.2" (as matched by versionRe)
// into its three parts: major, minor, and patch
// It's based on the Git tag.
func splitVersion(v string) (major, minor, patch int) {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return
	}
	parts := strings.Split(m[1], ".")
	if len(parts) >= 1 {
		major, _ = strconv.Atoi(parts[0])

		if len(parts) >= 2 {
			minor, _ = strconv.Atoi(parts[1])

			if len(parts) >= 3 {
				patch, _ = strconv.Atoi(parts[2])
			}
		}
	}
	return
}

// wixIsWinXPSupported checks if Windows XP
// support is expected from the specified version.
// (WinXP is no longer supported starting Go v1.11)
func wixIsWinXPSupported(v string) bool {
	major, minor, _ := splitVersion(v)
	if major > 1 {
		return false
	}
	if minor >= 11 {
		return false
	}
	return true
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
		if err := ioutil.WriteFile(dst, b, 0755); err != nil {
			return err
		}
	}
	return nil
}

var darwinData = map[string]string{

	"scripts/postinstall": `#!/bin/bash
GOROOT=/usr/local/go
echo "Fixing permissions"
cd $GOROOT
find . -exec chmod ugo+r \{\} \;
find bin -exec chmod ugo+rx \{\} \;
find . -type d -exec chmod ugo+rx \{\} \;
chmod o-w .
`,

	"scripts/preinstall": `#!/bin/bash
GOROOT=/usr/local/go
echo "Removing previous installation"
if [ -d $GOROOT ]; then
	rm -r $GOROOT
fi
`,

	"Distribution": `<?xml version="1.0" encoding="utf-8" standalone="no"?>
<installer-script minSpecVersion="1.000000">
    <title>Go</title>
    <background mime-type="image/png" file="bg.png"/>
    <options customize="never" allow-external-scripts="no"/>
    <domains enable_localSystem="true" />
    <installation-check script="installCheck();"/>
    <script>
function installCheck() {
    if(!(system.compareVersions(system.version.ProductVersion, '10.6.0') >= 0)) {
        my.result.title = 'Unable to install';
        my.result.message = 'Go requires Mac OS X 10.6 or later.';
        my.result.type = 'Fatal';
        return false;
    }
    if(system.files.fileExistsAtPath('/usr/local/go/bin/go')) {
	    my.result.title = 'Previous Installation Detected';
	    my.result.message = 'A previous installation of Go exists at /usr/local/go. This installer will remove the previous installation prior to installing. Please back up any data before proceeding.';
	    my.result.type = 'Warning';
	    return false;
	}
    return true;
}
    </script>
    <choices-outline>
        <line choice="com.googlecode.go.choice"/>
    </choices-outline>
    <choice id="com.googlecode.go.choice" title="Go">
        <pkg-ref id="com.googlecode.go.pkg"/>
    </choice>
    <pkg-ref id="com.googlecode.go.pkg" auth="Root">com.googlecode.go.pkg</pkg-ref>
</installer-script>
`,

	"Resources/bg.png": storageBase + "darwin/bg.png",
}

var windowsData = map[string]string{

	"installer.wxs": `<?xml version="1.0" encoding="UTF-8"?>
<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
<!--
# Copyright 2010 The Go Authors.  All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
-->

<?if $(var.Arch) = 386 ?>
  <?define ProdId = {FF5B30B2-08C2-11E1-85A2-6ACA4824019B} ?>
  <?define UpgradeCode = {1C3114EA-08C3-11E1-9095-7FCA4824019B} ?>
  <?define SysFolder=SystemFolder ?>
<?else?>
  <?define ProdId = {716c3eaa-9302-48d2-8e5e-5cfec5da2fab} ?>
  <?define UpgradeCode = {22ea7650-4ac6-4001-bf29-f4b8775db1c0} ?>
  <?define SysFolder=System64Folder ?>
<?endif?>

<Product
    Id="*"
    Name="Go Programming Language $(var.Arch) $(var.GoVersion)"
    Language="1033"
    Version="$(var.WixGoVersion)"
    Manufacturer="https://golang.org"
    UpgradeCode="$(var.UpgradeCode)" >

<Package
    Id='*'
    Keywords='Installer'
    Description="The Go Programming Language Installer"
    Comments="The Go programming language is an open source project to make programmers more productive."
    InstallerVersion="300"
    Compressed="yes"
    InstallScope="perMachine"
    Languages="1033" />

<Property Id="ARPCOMMENTS" Value="The Go programming language is a fast, statically typed, compiled language that feels like a dynamically typed, interpreted language." />
<Property Id="ARPCONTACT" Value="golang-nuts@googlegroups.com" />
<Property Id="ARPHELPLINK" Value="https://golang.org/help/" />
<Property Id="ARPREADME" Value="https://golang.org" />
<Property Id="ARPURLINFOABOUT" Value="https://golang.org" />
<Property Id="LicenseAccepted">1</Property>
<Icon Id="gopher.ico" SourceFile="images\gopher.ico"/>
<Property Id="ARPPRODUCTICON" Value="gopher.ico" />
<Property Id="EXISTING_GOLANG_INSTALLED">
  <RegistrySearch Id="installed" Type="raw" Root="HKCU" Key="Software\GoProgrammingLanguage" Name="installed" />
</Property>
<Media Id='1' Cabinet="go.cab" EmbedCab="yes" CompressionLevel="high" />
<?if $(var.IsWinXPSupported) = true ?>
    <Condition Message="Windows XP (with Service Pack 2) or greater required.">
        (VersionNT >= 501 AND (WindowsBuild > 2600 OR ServicePackLevel >= 2))
    </Condition>
<?else?>
    <Condition Message="Windows 7 (with Service Pack 1) or greater required.">
        ((VersionNT > 601) OR (VersionNT = 601 AND ServicePackLevel >= 1))
    </Condition>
<?endif?>
<MajorUpgrade AllowDowngrades="yes" />
<SetDirectory Id="INSTALLDIRROOT" Value="[%SYSTEMDRIVE]"/>

<CustomAction
    Id="SetApplicationRootDirectory"
    Property="ARPINSTALLLOCATION"
    Value="[INSTALLDIR]" />

<!-- Define the directory structure and environment variables -->
<Directory Id="TARGETDIR" Name="SourceDir">
  <Directory Id="INSTALLDIRROOT">
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
        Id="GoDocServerStartMenuShortcut"
        Name="GoDocServer"
        Description="Starts the Go documentation server (http://localhost:6060)"
        Show="minimized"
        Arguments='/c start "Godoc Server http://localhost:6060" "[INSTALLDIR]bin\godoc.exe" -http=localhost:6060 -goroot="[INSTALLDIR]." &amp;&amp; start http://localhost:6060'
        Icon="gopher.ico"
        Target="[%ComSpec]" />
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
        Id="GoRoot"
        Action="set"
        Part="all"
        Name="GOROOT"
        Permanent="no"
        System="yes"
        Value="[INSTALLDIR]" />
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
        Value="%GOPATH%\bin" />
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

<!-- Notify top level applications of the new PATH variable (golang.org/issue/18680)  -->
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

// runSelfTests contains the tests for this file, since this file is
// +build ignore. This is called by releaselet_test.go with an
// environment variable set, which func main above recognizes.
func runSelfTests() {
	// Test splitVersion.
	for _, tt := range []struct {
		v                   string
		major, minor, patch int
	}{
		{"go1", 1, 0, 0},
		{"go1.34", 1, 34, 0},
		{"go1.34.7", 1, 34, 7},
	} {
		major, minor, patch := splitVersion(tt.v)
		if major != tt.major || minor != tt.minor || patch != tt.patch {
			log.Fatalf("splitVersion(%q) = %v, %v, %v; want %v, %v, %v",
				tt.v, major, minor, patch, tt.major, tt.minor, tt.patch)
		}
	}

	// Test wixIsWinXPSupported
	for _, tt := range []struct {
		v    string
		want bool
	}{
		{"go1.9", true},
		{"go1.10", true},
		{"go1.11", false},
		{"go1.12", false},
	} {
		got := wixIsWinXPSupported(tt.v)
		if got != tt.want {
			log.Fatalf("wixIsWinXPSupported(%q) = %v; want %v", tt.v, got, tt.want)
		}
	}

	fmt.Println("ok")
}
