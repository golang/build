// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
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
	return g, storeGroup(g)
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
	return storeGroup(activeGroup)
}

func removeFromGroup(args []string) error {
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
			continue
		}
		newInstances = append(newInstances, inst)
	}
	activeGroup.Instances = newInstances
	return storeGroup(activeGroup)
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
	Name string `json:"name"`

	// Instances is a list of instances in the group.
	Instances []string `json:"instances"`
}

func (g *groupData) has(inst string) bool {
	for _, i := range g.Instances {
		if inst == i {
			return true
		}
	}
	return false
}

func loadAllGroups() ([]*groupData, error) {
	dir, err := groupDir()
	if err != nil {
		return nil, fmt.Errorf("acquiring group directory: %w", err)
	}
	// N.B. Glob ignores I/O errors, so no matches also means the directory
	// does not exist.
	matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var groups []*groupData
	for _, match := range matches {
		g, err := loadGroupFromFile(match)
		if err != nil {
			return nil, fmt.Errorf("reading group file for %q: %w", match, err)
		}
		groups = append(groups, g)
	}
	return groups, nil
}

func loadGroup(name string) (*groupData, error) {
	fname, err := groupFilePath(name)
	if err != nil {
		return nil, fmt.Errorf("loading group %q: %w", name, err)
	}
	g, err := loadGroupFromFile(fname)
	if err != nil {
		return nil, fmt.Errorf("loading group %q: %w", name, err)
	}
	return g, nil
}

func loadGroupFromFile(fname string) (*groupData, error) {
	f, err := os.Open(fname)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	g := new(groupData)
	if err := json.NewDecoder(f).Decode(g); err != nil {
		return nil, err
	}
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
	return g, storeGroup(g)
}

func storeGroup(data *groupData) error {
	fname, err := groupFilePath(data.Name)
	if err != nil {
		return fmt.Errorf("storing group %q: %w", data.Name, err)
	}
	if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
		return fmt.Errorf("storing group %q: %w", data.Name, err)
	}
	f, err := os.Create(fname)
	if err != nil {
		return fmt.Errorf("storing group %q: %w", data.Name, err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(data); err != nil {
		return fmt.Errorf("storing group %q: %w", data.Name, err)
	}
	return nil
}

func deleteGroup(name string) error {
	fname, err := groupFilePath(name)
	if err != nil {
		return fmt.Errorf("deleting group %q: %w", name, err)
	}
	if err := os.Remove(fname); err != nil {
		return fmt.Errorf("deleting group %q: %w", name, err)
	}
	return nil
}

func groupFilePath(name string) (string, error) {
	dir, err := groupDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%s.json", name)), nil
}

func groupDir() (string, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfgDir, "gomote", "groups"), nil
}
