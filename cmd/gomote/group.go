// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

func group(args []string) error {
	cm := map[string]struct {
		run  func([]string) error
		desc string
	}{
		"create":  {createGroup, "create a new group"},
		"destroy": {destroyGroup, "destroy an existing group (does not destroy gomotes)"},
		"add":     {addToGroup, "add an existing instance to a group"},
		"remove":  {removeFromGroup, "remove an existing instance from a group"},
		"list":    {listGroups, "list existing groups and their details"},
	}
	if len(args) == 0 {
		var cmds []string
		for cmd := range cm {
			cmds = append(cmds, cmd)
		}
		sort.Strings(cmds)
		usageLogger.Printf("Usage of gomote group: gomote [global-flags] group <cmd> [cmd-flags]\n")
		usageLogger.Printf("Commands:\n")
		for _, name := range cmds {
			usageLogger.Printf("  %-8s %s\n", name, cm[name].desc)
		}
		usageLogger.Print()
		os.Exit(1)
	}
	subCmd := args[0]
	sc, ok := cm[subCmd]
	if !ok {
		return fmt.Errorf("unknown sub-command %q\n", subCmd)
	}
	return sc.run(args[1:])
}

func createGroup(args []string) error {
	usage := func() {
		log.Print("group create usage: gomote group create <name>")
		os.Exit(1)
	}
	if len(args) != 1 {
		usage()
	}
	_, err := doCreateGroup(args[0])
	return err
}

func doCreateGroup(name string) (*groupData, error) {
	if _, err := loadGroup(name); err == nil {
		return nil, fmt.Errorf("group %q already exists", name)
	}
	g := &groupData{Name: name}
	return g, updateGroup(g)
}

func destroyGroup(args []string) error {
	usage := func() {
		log.Print("group destroy usage: gomote group destroy <name>")
		os.Exit(1)
	}
	if len(args) != 1 {
		usage()
	}
	name := args[0]
	_, err := loadGroup(name)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("group %q does not exist", name)
	} else if err != nil {
		return fmt.Errorf("loading group %q: %w", name, err)
	}
	if err := deleteGroup(name); err != nil {
		return err
	}
	if os.Getenv("GOMOTE_GROUP") == name {
		log.Print("You may wish to now clear GOMOTE_GROUP.")
	}
	return nil
}

func addToGroup(args []string) error {
	usage := func() {
		log.Print("group add usage: gomote group add [instances ...]")
		os.Exit(1)
	}
	if len(args) == 0 {
		usage()
	}
	if activeGroup == nil {
		log.Print("No active group found. Use -group or GOMOTE_GROUP.")
		usage()
	}
	ctx := context.Background()
	for _, inst := range args {
		if err := doPing(ctx, inst); err != nil {
			return fmt.Errorf("instance %q: %w", inst, err)
		}
		activeGroup.Instances = append(activeGroup.Instances, inst)
	}
	return updateGroup(activeGroup)
}

func removeFromGroup(args []string) error {
	usage := func() {
		log.Print("group remove usage: gomote group remove [instances ...]")
		os.Exit(1)
	}
	if len(args) == 0 {
		usage()
	}
	if activeGroup == nil {
		log.Print("No active group found. Use -group or GOMOTE_GROUP.")
		usage()
	}
	var errs []error
	newInstances := make([]string, 0, len(activeGroup.Instances))
	for _, inst := range activeGroup.Instances {
		remove := false
		for _, rmInst := range args {
			if inst == rmInst {
				remove = true
				break
			}
		}
		if remove {
			if err := pruneFromGroup(inst, activeGroup.Name); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		newInstances = append(newInstances, inst)
	}
	activeGroup.Instances = newInstances
	return errors.Join(errs...)
}

func listGroups(args []string) error {
	usage := func() {
		log.Print("group list usage: gomote group list")
		os.Exit(1)
	}
	if len(args) != 0 {
		usage()
	}
	groups, err := loadAllGroups()
	if err != nil {
		return err
	}
	// N.B. Glob ignores I/O errors, so no matches also means the directory
	// does not exist.
	emit := func(name, inst string) {
		fmt.Printf("%s\t%s\t\n", name, inst)
	}
	emit("Name", "Instances")
	for _, g := range groups {
		sort.Strings(g.Instances)
		emitted := false
		for _, inst := range g.Instances {
			if !emitted {
				emit(g.Name, inst)
			} else {
				emit("", inst)
			}
			emitted = true
		}
		if !emitted {
			emit(g.Name, "(none)")
		}
	}
	if len(groups) == 0 {
		fmt.Println("(none)")
	}
	return nil
}

type groupData struct {
	// User-provided name of the group.
	Name string

	// Instances is a list of instances in the group.
	Instances []string
}

func (g *groupData) has(inst string) bool {
	for _, i := range g.Instances {
		if inst == i {
			return true
		}
	}
	return false
}

const groupDirSuffix = ".instances"

func loadAllGroups() ([]*groupData, error) {
	dir, err := groupDir()
	if err != nil {
		return nil, fmt.Errorf("acquiring group directory: %w", err)
	}
	// N.B. Glob ignores I/O errors, so no matches also means the directory
	// does not exist.
	matches, _ := filepath.Glob(filepath.Join(dir, "*"+groupDirSuffix))
	var groups []*groupData
	for _, match := range matches {
		g, err := loadGroupFromDirectory(match)
		if err != nil {
			return nil, fmt.Errorf("reading group file for %q: %w", match, err)
		}
		groups = append(groups, g)
	}
	return groups, nil
}

func loadGroup(name string) (*groupData, error) {
	fname, err := groupDirPath(name)
	if err != nil {
		return nil, fmt.Errorf("loading group %q: %w", name, err)
	}
	g, err := loadGroupFromDirectory(fname)
	if err != nil {
		return nil, fmt.Errorf("loading group %q: %w", name, err)
	}
	return g, nil
}

func loadGroupFromDirectory(dname string) (*groupData, error) {
	name, ok := strings.CutSuffix(filepath.Base(dname), groupDirSuffix)
	if !ok {
		return nil, fmt.Errorf("directory does not have the expected suffix %s: %s", groupDirSuffix, dname)
	}
	entries, err := os.ReadDir(dname)
	if err != nil {
		return nil, err
	}
	var instances []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			// Filter out hidden files, such as .DS_Store on macOS.
			continue
		}
		instances = append(instances, entry.Name())
	}
	g := &groupData{Name: name, Instances: instances}

	// On every load, ping for liveness and prune.
	//
	// Otherwise, we can get into situations where we sometimes
	// don't have an accurate record.
	ctx := context.Background()
	newInstances := make([]string, 0, len(g.Instances))
	for _, inst := range g.Instances {
		err := doPing(ctx, inst)
		if instanceDoesNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		newInstances = append(newInstances, inst)
	}
	g.Instances = newInstances
	return g, nil
}

func updateGroup(data *groupData) error {
	dname, err := groupDirPath(data.Name)
	if err != nil {
		return fmt.Errorf("storing group %q: %w", data.Name, err)
	}
	if err := os.MkdirAll(dname, 0755); err != nil {
		return fmt.Errorf("storing group %q: %w", data.Name, err)
	}
	entries, err := os.ReadDir(dname)
	if err != nil {
		return err
	}
	storedInstances := make(map[string]struct{})
	for _, entry := range entries {
		storedInstances[entry.Name()] = struct{}{}
	}
	var errs []error
	for _, inst := range data.Instances {
		if _, ok := storedInstances[inst]; !ok {
			f, err := os.Create(filepath.Join(dname, inst))
			if os.IsExist(err) {
				// Concurrent updates from multiple gomote processes may result in a collision, or
				// this entry is stale. Both are fine.
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("storing group %q: %w", data.Name, err))
			}
			f.Close()
		}
	}
	return errors.Join(errs...)
}

func pruneFromGroup(inst, groupName string) error {
	dname, err := groupDirPath(groupName)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dname, inst))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func pruneFromAllGroups(instances ...string) error {
	groups, err := loadAllGroups()
	if err != nil {
		return err
	}
	var errs []error
	for _, inst := range instances {
		for _, g := range groups {
			if slices.Contains(g.Instances, inst) {
				if err := pruneFromGroup(inst, g.Name); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

func deleteGroup(name string) error {
	dname, err := groupDirPath(name)
	if err != nil {
		return fmt.Errorf("deleting group %q: %w", name, err)
	}
	// Atomically make the group disappear by changing its suffix,
	// then delete its contents.
	if err := os.Rename(dname, dname+".deleting"); err != nil {
		return fmt.Errorf("deleting group %q: %w", name, err)
	}
	if err := os.RemoveAll(dname + ".deleting"); err != nil {
		return fmt.Errorf("deleting group %q: %w", name, err)
	}
	return nil
}

func groupDirPath(name string) (string, error) {
	dir, err := groupDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%s%s", name, groupDirSuffix)), nil
}

func groupDir() (string, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfgDir, "gomote", "groups"), nil
}
