// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v4"
)

const dbEnvKey = "RELUI_TEST_DATABASE"

func TestCreateDBIfNotExists(t *testing.T) {
	dbEnv := os.Getenv(dbEnvKey)
	if dbEnv == "" {
		t.Skipf("%q is not set. Skipping.", dbEnvKey)
	}
	if testing.Short() {
		t.Skip("Skipping database tests in short mode.")
	}

	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dbEnv)
	if err != nil {
		t.Fatalf("pgx.ParseConfig(os.Getenv(%q)) = %v, wanted no error", dbEnvKey, err)
	}
	testCfg := cfg.Copy()
	testCfg.Database = "relui-test-nonexistent"
	if err := DropDB(ctx, testCfg); err != nil && !errors.Is(err, errDBNotExist) {
		t.Fatalf("p.DropDB() = %v, wanted %q or nil", err, errDBNotExist)
	}
	exists, err := checkIfDBExists(ctx, testCfg)
	if exists || err != nil {
		t.Fatalf("p.checkIfDBExists() = %t, %v, wanted %t, nil", exists, err, false)
	}
	if err := CreateDBIfNotExists(ctx, testCfg); err != nil {
		t.Errorf("p.CreateDBIfNotExists() = %v, wanted no error", err)
	}
	exists, err = checkIfDBExists(ctx, testCfg)
	if !exists || err != nil {
		t.Fatalf("p.checkIfDBExists() = %t, %v, wanted %t, nil", exists, err, true)
	}
	defer DropDB(ctx, testCfg)
	// Create again with the same name.
	if err := CreateDBIfNotExists(ctx, testCfg); err != nil {
		t.Errorf("p.CreateDBIfNotExists() = %v, wanted no error", err)
	}
	exists, err = checkIfDBExists(ctx, testCfg)
	if !exists || err != nil {
		t.Fatalf("p.checkIfDBExists() = %t, %v, wanted %t, nil", exists, err, true)
	}
}
