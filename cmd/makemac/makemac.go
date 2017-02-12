// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
The makemac command starts OS X VMs for the builders.
It is currently just a thin wrapper around govc.

See https://github.com/vmware/govmomi/tree/master/govc

Usage:

  $ makemac <osx_minor_version>  # e.g, 8, 9, 10, 11, 12

*/
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: makemac <osx_minor_version>\n")
	os.Exit(1)
}

var flagStatus = flag.Bool("status", false, "print status only")

func main() {
	flag.Parse()
	if !(flag.NArg() == 1 || (*flagStatus && flag.NArg() == 0)) {
		usage()
	}
	minor, err := strconv.Atoi(flag.Arg(0))
	if err != nil && !*flagStatus {
		usage()
	}

	ctx := context.Background()
	state, err := getState(ctx)
	if err != nil {
		log.Fatal(err)
	}

	if *flagStatus {
		stj, _ := json.MarshalIndent(state, "", "  ")
		fmt.Printf("%s\n", stj)
		return
	}

	err = state.CreateMac(ctx, minor)
	if err != nil {
		log.Fatal(err)
	}
}

// State is the state of the world.
type State struct {
	mu sync.Mutex

	Hosts  map[string]int    // IP address -> running Mac VM count (including 0)
	VMHost map[string]string // "mac_10_8_host2b" => "10.0.0.0"
	HostIP map[string]string // "host-5" -> "10.0.0.0"
}

// CreateMac creates an Mac VM running OS X 10.<minor>.
func (st *State) CreateMac(ctx context.Context, minor int) (err error) {
	// TODO(bradfitz): return VM name, update state, etc.

	st.mu.Lock()
	defer st.mu.Unlock()

	var guestType string
	switch minor {
	case 8:
		guestType = "darwin12_64Guest"
	case 9:
		guestType = "darwin13_64Guest"
	case 10, 11, 12:
		guestType = "darwin14_64Guest"
	default:
		return fmt.Errorf("unsupported makemac minor OS X version %d", minor)
	}

	builderType := fmt.Sprintf("darwin-amd64-10_%d", minor)
	key, err := ioutil.ReadFile(filepath.Join(os.Getenv("HOME"), "keys", builderType))
	if err != nil {
		return err
	}

	// Find the top-level datastore directory hosting the vmdk COW disk for
	// the linked clone. This is usually named "osx_9_frozen", but may be named
	// with a "_1", "_2", etc suffix. Search for it.
	netAppDir, err := findFrozenDir(ctx, minor)
	if err != nil {
		return fmt.Errorf("failed to find osx_%d_frozen base directory: %v", minor, err)
	}

	hostNum, hostWhich, err := st.pickHost()
	if err != nil {
		return err
	}
	name := fmt.Sprintf("mac_10_%v_host%d%s", minor, hostNum, hostWhich)

	if err := govc(ctx, "vm.create",
		"-m", "4096",
		"-c", "6",
		"-on=false",
		"-net", "dvPortGroup-Private", // 10.50.0.0/16
		"-g", guestType,
		// Put the config on the host's datastore, which
		// forces the VM to run on that host:
		"-ds", fmt.Sprintf("BOOT_%d", hostNum),
		name,
	); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err := govc(ctx, "vm.destroy", name)
			if err != nil {
				log.Printf("failed to destroy %v: %v", name, err)
			}
		}
	}()

	if err := govc(ctx, "vm.change",
		"-e", "smc.present=TRUE",
		"-e", "ich7m.present=TRUE",
		"-e", "firmware=efi",
		"-e", fmt.Sprintf("guestinfo.key-%s=%s", builderType, strings.TrimSpace(string(key))),
		"-e", "guestinfo.name="+name,
		"-vm", name,
	); err != nil {
		return err
	}

	if err := govc(ctx, "device.usb.add", "-vm", name); err != nil {
		return err
	}

	if err := govc(ctx, "vm.disk.attach",
		"-vm", name,
		"-link=true",
		"-persist=false",
		"-ds=Pure1-1",
		"-disk", fmt.Sprintf("%s/osx_%d_frozen.vmdk", netAppDir, minor),
	); err != nil {
		return err
	}

	if err := govc(ctx, "vm.power", "-on", name); err != nil {
		return err
	}
	log.Printf("Success.")
	return nil
}

// govc runs "govc <args...>" and ignores its output, unless there's an error.
func govc(ctx context.Context, args ...string) error {
	fmt.Fprintf(os.Stderr, "$ govc %v\n", strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, "govc", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("govc %s ...: %v, %s", args[0], err, out)
	}
	return nil
}

const hostIPPrefix = "10.88.203." // with fourth octet starting at 10

// st.mu must be held.
func (st *State) pickHost() (hostNum int, hostWhich string, err error) {
	for ip, inUse := range st.Hosts {
		if !strings.HasPrefix(ip, hostIPPrefix) {
			continue
		}
		if inUse >= 2 {
			// Apple policy.
			continue
		}
		hostNum, err = strconv.Atoi(strings.TrimPrefix(ip, hostIPPrefix))
		if err != nil {
			return 0, "", err
		}
		hostNum -= 10   // 10.88.203.11 is "BOOT_1" datastore.
		hostWhich = "a" // unless in use
		if st.whichAInUse(hostNum) {
			hostWhich = "b"
		}
		return
	}
	return 0, "", errors.New("no usable host found")
}

// whichAInUse reports whether a VM is running on the provided hostNum named
// with suffix "_host<n>a".
//
// st.mu must be held
func (st *State) whichAInUse(hostNum int) bool {
	suffix := fmt.Sprintf("_host%da", hostNum)
	for name := range st.VMHost {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// getStat queries govc to find the current state of the hosts and VMs.
func getState(ctx context.Context) (*State, error) {
	st := &State{
		VMHost: make(map[string]string),
		Hosts:  make(map[string]int),
		HostIP: make(map[string]string),
	}

	var hosts elementList
	if err := govcJSONDecode(ctx, &hosts, "ls", "-json", "/MacStadium-ATL/host/MacMini_Cluster"); err != nil {
		return nil, fmt.Errorf("Reading /MacStadium-ATL/host/MacMini_Cluster: %v", err)
	}
	for _, h := range hosts.Elements {
		if h.Object.Self.Type == "HostSystem" {
			ip := path.Base(h.Path)
			st.Hosts[ip] = 0
			st.HostIP[h.Object.Self.Value] = ip
		}
	}

	var vms elementList
	if err := govcJSONDecode(ctx, &vms, "ls", "-json", "/MacStadium-ATL/vm"); err != nil {
		return nil, fmt.Errorf("Reading /MacStadium-ATL/vm: %v", err)
	}
	for _, h := range vms.Elements {
		if h.Object.Self.Type == "VirtualMachine" {
			name := path.Base(h.Path)
			hostID := h.Object.Runtime.Host.Value
			hostIP := st.HostIP[hostID]
			st.VMHost[name] = hostIP
			if hostIP != "" && strings.HasPrefix(name, "mac_10_") {
				st.Hosts[hostIP]++
			}
		}
	}

	return st, nil
}

// objRef is a VMWare "Managed Object Reference".
type objRef struct {
	Type  string // e.g. "VirtualMachine"
	Value string // e.g. "host-12"
}

type elementList struct {
	Elements []*elementJSON `json:"elements"`
}

type elementJSON struct {
	Path   string
	Object struct {
		Self    objRef
		Runtime struct {
			Host objRef // for VMs; not present otherwise
		}
	}
}

// govcJSONDecode runs "govc <args...>" and decodes its JSON output into dst.
func govcJSONDecode(ctx context.Context, dst interface{}, args ...string) error {
	cmd := exec.CommandContext(ctx, "govc", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	err = json.NewDecoder(stdout).Decode(dst)
	cmd.Process.Kill() // usually unnecessary
	if werr := cmd.Wait(); werr != nil && err == nil {
		err = werr
	}
	return err
}

// findFrozenDir returns the name of the top-level directory on the
// Pure1-1 shared datastore containing a directory starting with
// "osx_<minor>_frozen". It might be that just that, or have a suffix
// like "_1" or "_2".
func findFrozenDir(ctx context.Context, minor int) (string, error) {
	out, err := exec.CommandContext(ctx, "govc", "datastore.ls", "-ds=Pure1-1").Output()
	if err != nil {
		return "", err
	}
	prefix := fmt.Sprintf("osx_%d_frozen", minor)
	for _, dir := range strings.Fields(string(out)) {
		if strings.HasPrefix(dir, prefix) {
			return dir, nil
		}
	}
	return "", os.ErrNotExist
}
