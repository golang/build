// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

package relui

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
)

var errDBNotExist = errors.New("database does not exist")

// store is a persistence interface for saving data.
type store interface {
}

var _ store = (*PgStore)(nil)

// PgStore is a store backed by a Postgres database.
type PgStore struct {
	db *pgxpool.Pool
}

// Connect connects to the database using the credentials supplied in
// the provided connString.
//
// Any key/value or URI string compatible with libpq is valid. If the
// database does not exist, one will be created using the credentials
// provided.
func (p *PgStore) Connect(ctx context.Context, connString string) error {
	cfg, err := pgx.ParseConfig(connString)
	if err != nil {
		return fmt.Errorf("pgx.ParseConfig() = %w", err)
	}
	if err := CreateDBIfNotExists(ctx, cfg); err != nil {
		return err
	}
	pool, err := pgxpool.Connect(ctx, connString)
	if err != nil {
		return err
	}
	p.db = pool
	return nil
}

// Close closes the pgxpool.Pool.
func (p *PgStore) Close() {
	p.db.Close()
}

// ConnectMaintenanceDB connects to the maintenance database using the
// credentials from cfg. If maintDB is an empty string, the database
// with the name cfg.User will be used.
func ConnectMaintenanceDB(ctx context.Context, cfg *pgx.ConnConfig, maintDB string) (*pgx.Conn, error) {
	cfg = cfg.Copy()
	cfg.Database = maintDB
	return pgx.ConnectConfig(ctx, cfg)
}

// CreateDBIfNotExists checks whether the given dbName is an existing
// database, and creates one if not.
func CreateDBIfNotExists(ctx context.Context, cfg *pgx.ConnConfig) error {
	exists, err := checkIfDBExists(ctx, cfg)
	if err != nil || exists {
		return err
	}
	conn, err := ConnectMaintenanceDB(ctx, cfg, "")
	if err != nil {
		return fmt.Errorf("ConnectMaintenanceDB = %w", err)
	}
	createSQL := fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{cfg.Database}.Sanitize())
	if _, err := conn.Exec(ctx, createSQL); err != nil {
		return fmt.Errorf("conn.Exec(%q) = %w", createSQL, err)
	}
	return nil
}

// DropDB drops the database specified in cfg. An error returned if
// the database does not exist.
func DropDB(ctx context.Context, cfg *pgx.ConnConfig) error {
	exists, err := checkIfDBExists(ctx, cfg)
	if err != nil {
		return fmt.Errorf("p.checkIfDBExists() = %w", err)
	}
	if !exists {
		return errDBNotExist
	}
	conn, err := ConnectMaintenanceDB(ctx, cfg, "")
	if err != nil {
		return fmt.Errorf("ConnectMaintenanceDB = %w", err)
	}
	dropSQL := fmt.Sprintf("DROP DATABASE %s", pgx.Identifier{cfg.Database}.Sanitize())
	if _, err := conn.Exec(ctx, dropSQL); err != nil {
		return fmt.Errorf("conn.Exec(%q) = %w", dropSQL, err)
	}
	return nil
}

func checkIfDBExists(ctx context.Context, cfg *pgx.ConnConfig) (bool, error) {
	conn, err := ConnectMaintenanceDB(ctx, cfg, "")
	if err != nil {
		return false, fmt.Errorf("ConnectMaintenanceDB = %w", err)
	}
	row := conn.QueryRow(ctx, "SELECT 1 from pg_database WHERE datname=$1 LIMIT 1", cfg.Database)
	var exists int
	if err := row.Scan(&exists); err != nil && err != pgx.ErrNoRows {
		return false, fmt.Errorf("row.Scan() = %w", err)
	}
	return exists == 1, nil
}
