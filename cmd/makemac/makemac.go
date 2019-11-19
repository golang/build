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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/types"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
    makemac <osx_minor_version>
    makemac -status
    makemac -auto
`)
	os.Exit(1)
}

var (
	flagStatus   = flag.Bool("status", false, "print status only")
	flagAuto     = flag.Bool("auto", false, "Automatically create & destroy as needed, reacting to https://farmer.golang.org/status/reverse.json status.")
	flagListen   = flag.String("listen", ":8713", "HTTP status port; used by auto mode only")
	flagNuke     = flag.Bool("destroy-all", false, "immediately destroy all running Mac VMs")
	flagBaseDisk = flag.Int("base-disk", 0, "debug mode: if non-zero, print base disk of macOS 10.<value> VM and exit")
)

func main() {
	flag.Parse()
	numArg := flag.NArg()
	ctx := context.Background()
	if *flagBaseDisk != 0 {
		baseDisk, err := findBaseDisk(ctx, *flagBaseDisk)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(baseDisk)
		return
	}
	if *flagStatus {
		numArg++
	}
	if *flagAuto {
		numArg++
	}
	if *flagNuke {
		numArg++
	}
	if numArg != 1 {
		usage()
	}
	if *flagAuto {
		autoLoop()
		return
	}
	if *flagNuke {
		state, err := getState(ctx)
		if err != nil {
			log.Fatal(err)
		}
		if err := state.DestroyAllMacs(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	minor, err := strconv.Atoi(flag.Arg(0))
	if err != nil && !*flagStatus {
		usage()
	}

	state, err := getState(ctx)
	if err != nil {
		log.Fatal(err)
	}

	if *flagStatus {
		stj, _ := json.MarshalIndent(state, "", "  ")
		fmt.Printf("%s\n", stj)
		return
	}

	_, err = state.CreateMac(ctx, minor)
	if err != nil {
		log.Fatal(err)
	}
}

// State is the state of the world.
type State struct {
	mu sync.Mutex

	Hosts  map[string]int    // IP address -> running Mac VM count (including 0)
	VMHost map[string]string // "mac_10_8_host02b" => "10.0.0.0"
	HostIP map[string]string // "host-5" -> "10.0.0.0"
	VMInfo map[string]VMInfo // "mac_10_8_host02b" => ...

	// VMOfSlot maps from a "slot name" to the VMWare VM name.
	//
	// A slot name is a tuple of (host number, "a"|"b"), where "a"
	// and "b" are the two possible guests that can run per host.
	// This slot name of the form "macstadium_host02b" is what's
	// reported as the host name to the coordinator.
	//
	// The map value is the VMWare vm name, such as "mac_10_8_host02b",
	// and is the map key of VMHost and VMInfo above.
	VMOfSlot map[string]string // "macstadium_host02b" => "mac_10_8_host02b"
}

type VMInfo struct {
	IP       string
	BootTime time.Time

	// SlotName is the name of a place where we can run a VM.
	// As of 2017-08-04 we have 20 slots total over 10 physical
	// machines. (Two VMs per physical Mac Mini running ESXi)
	// We use slot names of the form "macstadium_host02b"
	// with a %02d digit host number and suffix 'a' and 'b'
	// for which VM it is on that host.
	//
	// This slot name is also the name passed to the build
	// coordinator as the coordinator's "host name". (which exists
	// both for debugging, and for monitoring last-seen/uptime of
	// dedicated builders.)
	SlotName string
}

// NumCreatableVMs returns the number of VMs that can be created given
// the current capacity.
func (st *State) NumCreatableVMs() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	n := 0
	for _, cur := range st.Hosts {
		if cur < 2 {
			n += 2 - cur
		}
	}
	return n
}

// NumMacVMsOfVersion reports how many VMs are running Mac OS X 10.<ver>.
func (st *State) NumMacVMsOfVersion(ver int) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	prefix := fmt.Sprintf("mac_10_%v_", ver)
	n := 0
	for name := range st.VMInfo {
		if strings.HasPrefix(name, prefix) {
			n++
		}
	}
	return n
}

// DestroyAllMacs runs "govc vm.destroy" on each running Mac VM.
func (st *State) DestroyAllMacs(ctx context.Context) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	var ret error
	for name := range st.VMInfo {
		log.Printf("destroying %s ...", name)
		err := govc(ctx, "vm.destroy", name)
		log.Printf("vm.destroy(%q) = %v", name, err)
		if err != nil && ret == nil {
			ret = err
		}
	}
	return ret
}

// CreateMac creates an Mac VM running OS X 10.<minor>.
func (st *State) CreateMac(ctx context.Context, minor int) (slotName string, err error) {
	// TODO(bradfitz): return VM name, update state, etc.

	st.mu.Lock()
	defer st.mu.Unlock()

	var guestType string
	switch minor {
	case 8:
		guestType = "darwin12_64Guest"
	case 9:
		guestType = "darwin13_64Guest"
	case 10:
		guestType = "darwin14_64Guest"
	case 11:
		guestType = "darwin15_64Guest"
	case 12:
		guestType = "darwin16_64Guest"
	case 13:
		// High Sierra. Requires vSphere 6.7.
		// https://www.virtuallyghetto.com/2018/04/new-vsphere-6-7-apis-worth-checking-out.html
		guestType = "darwin17_64Guest"
	case 14:
		// Mojave. Requires vSphere 6.7.
		// https://www.virtuallyghetto.com/2018/04/new-vsphere-6-7-apis-worth-checking-out.html
		guestType = "darwin18_64Guest"
	case 15:
		// Catalina. Requires vSphere 6.7 update 3.
		// https://docs.macstadium.com/docs/vsphere-67-update-3
		// vSphere 6.7 update 3 does not support the guestid `darwin19_64Guest` (which would be
		// associated with macOS 10.15. It enables the creation of a macOS 10.15 vm via guestid
		// `darwin18_64Guest`.
		// TODO: Add a new GOS definition for darwin19_64 (macOS 10.15) in HWV >= 17
		// https://github.com/vmware/open-vm-tools/commit/6297504ef9e139c68b65afe299136d041d690eeb
		// TODO: investigate updating the guestid when we upgrade vSphere past version 6.7u3.
		guestType = "darwin18_64Guest"
	default:
		return "", fmt.Errorf("unsupported makemac minor OS X version %d", minor)
	}

	hostType := fmt.Sprintf("host-darwin-10_%d", minor)

	key, err := ioutil.ReadFile(filepath.Join(os.Getenv("HOME"), "keys", hostType))
	if err != nil {
		return "", err
	}

	baseDisk, err := findBaseDisk(ctx, minor)
	if err != nil {
		return "", fmt.Errorf("failed to find osx_%d_frozen_nfs base disk: %v", minor, err)
	}

	hostNum, hostWhich, err := st.pickHost()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("mac_10_%v_host%02d%s", minor, hostNum, hostWhich)
	slotName = fmt.Sprintf("macstadium_host%02d%s", hostNum, hostWhich)

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
		return "", err
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
		"-e", fmt.Sprintf("guestinfo.key-%s=%s", hostType, strings.TrimSpace(string(key))),
		"-e", "guestinfo.name="+name,
		"-vm", name,
	); err != nil {
		return "", err
	}

	if err := govc(ctx, "device.usb.add", "-vm", name); err != nil {
		return "", err
	}

	if err := govc(ctx, "vm.disk.attach",
		"-vm", name,
		"-link=true",
		"-persist=false",
		"-ds=GGLGLN-A-001-STV1",
		"-disk", baseDisk,
	); err != nil {
		return "", err
	}

	if err := govc(ctx, "vm.power", "-on", name); err != nil {
		return "", err
	}
	log.Printf("Success.")
	return slotName, nil
}

// govc runs "govc <args...>" and ignores its output, unless there's an error.
func govc(ctx context.Context, args ...string) error {
	fmt.Fprintf(os.Stderr, "$ govc %v\n", strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, "govc", args...).CombinedOutput()
	if err != nil {
		if isFileSystemReadOnly() {
			out = append(out, "; filesystem is read-only"...)
		}
		return fmt.Errorf("govc %s ...: %v, %s", args[0], err, out)
	}
	return nil
}

const hostIPPrefix = "10.88.203." // with fourth octet starting at 10

var errNoHost = errors.New("no usable host found")

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
	return 0, "", errNoHost
}

// whichAInUse reports whether a VM is running on the provided hostNum named
// with suffix "_host<%02d>a", hostnum.
//
// st.mu must be held
func (st *State) whichAInUse(hostNum int) bool {
	suffix := fmt.Sprintf("_host%02da", hostNum)
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
		VMHost:   make(map[string]string),
		Hosts:    make(map[string]int),
		HostIP:   make(map[string]string),
		VMInfo:   make(map[string]VMInfo),
		VMOfSlot: make(map[string]string),
	}

	var hosts elementList
	if err := govcJSONDecode(ctx, &hosts, "ls", "-json", "/MacStadium-ATL/host/MacMini_Cluster"); err != nil {
		return nil, fmt.Errorf("getState: reading /MacStadium-ATL/host/MacMini_Cluster: %v", err)
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
		return nil, fmt.Errorf("getState: reading /MacStadium-ATL/vm: %v", err)
	}
	for _, h := range vms.Elements {
		if h.Object.Self.Type != "VirtualMachine" {
			continue
		}
		name := path.Base(h.Path)
		hostID := h.Object.Runtime.Host.Value
		hostIP := st.HostIP[hostID]
		st.VMHost[name] = hostIP
		if hostIP != "" && strings.HasPrefix(name, "mac_10_") {
			st.Hosts[hostIP]++
			var bootTime time.Time
			if bt := h.Object.Summary.Runtime.BootTime; bt != "" {
				bootTime, _ = time.Parse(time.RFC3339, bt)
			}

			var slotName string
			if p := strings.Index(name, "_host"); p != -1 {
				slotName = "macstadium" + name[p:] // macstadium_host02a

				if exist := st.VMOfSlot[slotName]; exist != "" {
					// Should never happen, but just in case.
					log.Printf("ERROR: existing VM %q found in slot %q; destroying later VM %q", exist, slotName, name)
					err := govc(ctx, "vm.destroy", name)
					log.Printf("vm.destroy(%q) = %v", name, err)
				} else {
					st.VMOfSlot[slotName] = name // macstadium_host02a => mac_10_8_host02a
				}
			}

			vi := VMInfo{
				IP:       hostIP,
				BootTime: bootTime,
				SlotName: slotName,
			}
			st.VMInfo[name] = vi
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
		Summary struct {
			Runtime struct {
				BootTime string // time.RFC3339 format, or empty if not running
			}
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
	if werr := cmd.Wait(); werr != nil && err == nil {
		err = werr
	}
	return err
}

// findBaseDisk returns the path of the vmdk of the most recent
// snapshot of the osx_$(minor)_frozen_nfs VM.
func findBaseDisk(ctx context.Context, minor int) (string, error) {
	vmName := fmt.Sprintf("osx_%d_frozen_nfs", minor)
	out, err := exec.CommandContext(ctx, "govc", "vm.info", "-json", vmName).Output()
	if err != nil {
		return "", err
	}
	var ret struct {
		VirtualMachines []struct {
			Layout struct {
				Snapshot []struct {
					SnapshotFile []string
				}
			}
		}
	}
	if err := json.Unmarshal(out, &ret); err != nil {
		return "", fmt.Errorf("failed to parse vm.info JSON to find base disk: %v", err)
	}
	if n := len(ret.VirtualMachines); n != 1 {
		if n == 0 {
			return "", fmt.Errorf("VM %s not found", vmName)
		}
		return "", fmt.Errorf("len(ret.VirtualMachines) = %d; want 1 in JSON to find base disk: %v", n, err)
	}
	vm := ret.VirtualMachines[0]
	if len(vm.Layout.Snapshot) < 1 {
		return "", fmt.Errorf("VM %s does not have any snapshots; needs at least one", vmName)
	}
	ss := vm.Layout.Snapshot[len(vm.Layout.Snapshot)-1] // most recent snapshot is last in list

	// Now find the first vmdk file, without its [datastore] prefix. The files are listed like:
	/*
	   "SnapshotFile": [
	     "[GGLGLN-A-001-STV1] osx_14_frozen_nfs/osx_14_frozen_nfs-Snapshot2.vmsn",
	     "[GGLGLN-A-001-STV1] osx_14_frozen_nfs/osx_14_frozen_nfs_15.vmdk",
	     "[GGLGLN-A-001-STV1] osx_14_frozen_nfs/osx_14_frozen_nfs_15-000001.vmdk"
	   ]
	*/
	for _, f := range ss.SnapshotFile {
		if strings.HasSuffix(f, ".vmdk") {
			i := strings.Index(f, "] ")
			if i == -1 {
				return "", fmt.Errorf("unexpected vmdk line %q in SnapshotFile", f)
			}
			return f[i+2:], nil
		}
	}
	return "", fmt.Errorf("no VMDK found in snapshot for %v", vmName)
}

const autoAdjustTimeout = 5 * time.Minute

var status struct {
	sync.Mutex
	lastCheck time.Time
	lastLog   string
	lastState *State
	warnings  []string
	errors    []string
}

func init() {
	http.HandleFunc("/stage0/", handleStage0)
	http.HandleFunc("/buildlet.darwin-amd64", handleBuildlet)
	http.Handle("/", onlyAtRoot{http.HandlerFunc(handleStatus)}) // legacy status location
	http.HandleFunc("/status", handleStatus)
}

func dedupLogf(format string, args ...interface{}) {
	s := fmt.Sprintf(format, args...)
	status.Lock()
	defer status.Unlock()
	if s == status.lastLog {
		return
	}
	status.lastLog = s
	log.Print(s)
}

func autoLoop() {
	if addr := *flagListen; addr != "" {
		go func() {
			if err := http.ListenAndServe(*flagListen, nil); err != nil {
				log.Fatalf("ListenAndServe: %v", err)
			}
		}()
	}
	for {
		timer := time.AfterFunc(autoAdjustTimeout, watchdogFail)
		autoAdjust()
		timer.Stop()
		time.Sleep(2 * time.Second)
	}
}

func watchdogFail() {
	stacks := make([]byte, 1<<20)
	stacks = stacks[:runtime.Stack(stacks, true)]
	log.Fatalf("timeout after %v waiting for autoAdjust(). stacks:\n%s",
		autoAdjustTimeout, stacks)
}

func autoAdjust() {
	status.Lock()
	status.lastCheck = time.Now()
	status.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), autoAdjustTimeout)
	defer cancel()

	ro := isFileSystemReadOnly()

	st, err := getState(ctx)
	if err != nil {
		status.Lock()
		if ro {
			status.errors = append(status.errors, "Host filesystem is read-only")
		}
		status.errors = []string{err.Error()}
		status.Unlock()
		log.Print(err)
		return
	}
	var warnings, errors []string
	if ro {
		errors = append(errors, "Host filesystem is read-only")
	}
	defer func() {
		// Set status.lastState once we're now longer using it.
		if st != nil {
			status.Lock()
			status.lastState = st
			status.warnings = warnings
			status.errors = errors
			status.Unlock()
		}
	}()

	req, _ := http.NewRequest("GET", "https://farmer.golang.org/status/reverse.json", nil)
	req = req.WithContext(ctx)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		errors = append(errors, fmt.Sprintf("getting /status/reverse.json from coordinator: %v", err))
		log.Printf("getting reverse status: %v", err)
		return
	}
	defer res.Body.Close()
	var rstat types.ReverseBuilderStatus
	if err := json.NewDecoder(res.Body).Decode(&rstat); err != nil {
		errors = append(errors, fmt.Sprintf("decoding /status/reverse.json from coordinator: %v", err))
		log.Printf("decoding reverse.json: %v", err)
		return
	}

	revHost := make(map[string]*types.ReverseBuilder)
	for hostType, hostStatus := range rstat.HostTypes {
		if !strings.HasPrefix(hostType, "host-darwin-10") {
			continue
		}
		for name, revBuild := range hostStatus.Machines {
			revHost[name] = revBuild
		}
	}

	// Destroy running VMs that appear to be dead and not connected to the coordinator.
	// TODO: do these all concurrently.
	dirty := false
	for name, vi := range st.VMInfo {
		if vi.BootTime.After(time.Now().Add(-3 * time.Minute)) {
			// Recently created. It takes about a minute
			// to boot and connect to the coordinator, so
			// give it 3 minutes of grace before killing
			// it.
			continue
		}
		rh := revHost[name]
		if rh == nil {
			// Look it up by its slot name instead.
			rh = revHost[vi.SlotName]
		}
		if rh == nil {
			log.Printf("Destroying VM %q unknown to coordinator...", name)
			err := govc(ctx, "vm.destroy", name)
			log.Printf("vm.destroy(%q) = %v", name, err)
			dirty = true
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("vm.destroy(%q) = %v", name, err))
			}
		}
	}
	for {
		if dirty {
			st, err = getState(ctx)
			if err != nil {
				errors = append(errors, err.Error())
				log.Print(err)
				return
			}
		}
		canCreate := st.NumCreatableVMs()
		if canCreate <= 0 {
			dedupLogf("All Mac VMs running.")
			return
		}
		ver := wantedMacVersionNext(st, &rstat)

		if ver == 0 {
			dedupLogf("Have capacity for %d more Mac VMs, but none requested by coordinator.", canCreate)
			return
		}
		dedupLogf("Have capacity for %d more Mac VMs; creating requested 10.%d ...", canCreate, ver)
		slotName, err := st.CreateMac(ctx, ver)
		if err != nil {
			errStr := fmt.Sprintf("Error creating 10.%d: %v", ver, err)
			errors = append(errors, errStr)
			log.Print(errStr)
			return
		}
		log.Printf("Created 10.%d VM on %q", ver, slotName)
		dirty = true
	}
}

// wantedMacVersionNext returns the macOS 10.x version to create next,
// or 0 to not make anything. It gets the latest reverse buildlet
// status from the coordinator.
func wantedMacVersionNext(st *State, rstat *types.ReverseBuilderStatus) int {
	// TODO: improve this logic now that the coordinator has a
	// proper scheduler. Instead, don't create anything
	// proactively until there's demand from it from the
	// scheduler. (will need to add that to the coordinator's
	// status JSON) And maybe add a streaming endpoint to the
	// coordinator so we don't need to poll every N seconds. Or
	// just poll every few seconds, perhaps at a lighter endpoint
	// that only does darwin.
	//
	// For now just use the static configuration in
	// dashboard/builders.go of how many are expected, which ends
	// up in ReverseBuilderStatus.
	for hostType, hostStatus := range rstat.HostTypes {
		if !strings.HasPrefix(hostType, "host-darwin-10_") {
			continue
		}
		ver, err := strconv.Atoi(strings.TrimPrefix(hostType, "host-darwin-10_"))
		if err != nil {
			log.Printf("ERROR: unexpected host type %q", hostType)
			continue
		}
		want := hostStatus.Expect - st.NumMacVMsOfVersion(ver)
		if want > 0 {
			return ver
		}
	}
	return 0
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	status.Lock()
	defer status.Unlock()
	w.Header().Set("Content-Type", "application/json")

	// Locking the lastState shouldn't matter since we
	// currently only set status.lastState once the
	// *Status is no longer in use, but lock it anyway, in
	// case usage changes in the future.
	if st := status.lastState; st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
	}

	// TODO: probably more status, as needed.
	res := &struct {
		LastCheck string
		LastLog   string
		LastState *State
		Warnings  []string
		Errors    []string
	}{
		LastCheck: status.lastCheck.UTC().Format(time.RFC3339),
		LastLog:   status.lastLog,
		LastState: status.lastState,
		Warnings:  status.warnings,
		Errors:    status.errors,
	}
	j, _ := json.MarshalIndent(res, "", "\t")
	w.Write(j)
}

// handleStage0 serves the shell script for buildlets to run on boot, based
// on their macOS version.
//
// Starting with the macOS 10.14 (Mojave) image, their baked-in stage0.sh
// script does:
//
//    while true; do (curl http://10.50.0.2:8713/stage0/$(sw_vers -productVersion)| sh); sleep 5; done
func handleStage0(w http.ResponseWriter, r *http.Request) {
	// ver will be like "10.14.4"
	// Nothing currently uses this, but it might be useful in the future.
	ver := strings.TrimPrefix(r.RequestURI, "/stage0/")
	_ = ver

	fmt.Fprintf(w, "set -e\nset -x\n")
	fmt.Fprintf(w, "export GO_BUILDER_ENV=macstadium_vm\n")
	fmt.Fprintf(w, "curl -o buildlet http://10.50.0.2:8713/buildlet.darwin-amd64\n")
	fmt.Fprintf(w, "chmod +x buildlet; ./buildlet")
}

func handleBuildlet(w http.ResponseWriter, r *http.Request) {
	bin, err := getLatestMacBuildlet(r.Context())
	if err != nil {
		log.Printf("error getting buildlet from GCS: %v", err)
		http.Error(w, "error getting buildlet from GCS", 500)
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(bin)))
	w.Write(bin)
}

// buildlet binary caching by its last seen ETag from HEAD responses
var (
	buildletMu   sync.Mutex
	lastEtag     string
	lastBuildlet []byte // last buildlet binary for lastEtag
)

func getLatestMacBuildlet(ctx context.Context) (bin []byte, err error) {
	req, _ := http.NewRequest("HEAD", "https://storage.googleapis.com/go-builder-data/buildlet.darwin-amd64", nil)
	req = req.WithContext(ctx)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%s from HEAD to %s", res.Status, req.URL)
	}
	etag := res.Header.Get("Etag")
	if etag == "" {
		return nil, fmt.Errorf("HEAD of %s lacked ETag", req.URL)
	}

	buildletMu.Lock()
	if etag == lastEtag {
		bin = lastBuildlet
		log.Printf("served cached buildlet of %s", etag)
		buildletMu.Unlock()
		return bin, nil
	}
	buildletMu.Unlock()

	log.Printf("fetching buildlet from GCS...")
	req, _ = http.NewRequest("GET", "https://storage.googleapis.com/go-builder-data/buildlet.darwin-amd64", nil)
	req = req.WithContext(ctx)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%s from GET to %s", res.Status, req.URL)
	}
	etag = res.Header.Get("Etag")
	log.Printf("fetched buildlet from GCS with etag %s", etag)
	if etag == "" {
		return nil, fmt.Errorf("GET of %s lacked ETag", req.URL)
	}
	slurp, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	buildletMu.Lock()
	defer buildletMu.Unlock()
	lastEtag = etag
	lastBuildlet = slurp
	return lastBuildlet, nil
}

// onlyAtRoot is an http.Handler wrapper that enforces that it's
// called at /, else it serves a 404.
type onlyAtRoot struct{ h http.Handler }

func (h onlyAtRoot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	h.h.ServeHTTP(w, r)
}

func isFileSystemReadOnly() bool {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return false
	}
	defer f.Close()
	// Look for line:
	//    /dev/sda1 / ext4 rw,relatime,errors=remount-ro,data=ordered 0 0
	bs := bufio.NewScanner(f)
	for bs.Scan() {
		f := strings.Fields(bs.Text())
		if len(f) < 4 {
			continue
		}
		mountPoint, state := f[1], f[3]
		if mountPoint == "/" {
			return strings.HasPrefix(state, "ro,")
		}
	}
	return false
}
