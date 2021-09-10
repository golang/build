// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

package relui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	dbpgx "github.com/golang-migrate/migrate/v4/database/pgx"
	"github.com/golang-migrate/migrate/v4/source/iofs"
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
// Any key/value or URI string compatible with libpq is valid.
func (p *PgStore) Connect(ctx context.Context, connString string) error {
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

// InitDB creates and applies all migrations to the database specified
// in conn.
//
// If the database does not exist, one will be created using the
// credentials provided.
//
// Any key/value or URI string compatible with libpq is valid.
func InitDB(ctx context.Context, conn string) error {
	cfg, err := pgx.ParseConfig(conn)
	if err != nil {
		return fmt.Errorf("pgx.ParseConfig() = %w", err)
	}
	if err := CreateDBIfNotExists(ctx, cfg); err != nil {
		return err
	}
	if err := MigrateDB(conn); err != nil {
		return err
	}
	return nil
}

// MigrateDB applies all migrations to the database specified in conn.
//
// Any key/value or URI string compatible with libpq is valid.
func MigrateDB(conn string) error {
	cfg, err := pgx.ParseConfig(conn)
	if err != nil {
		return fmt.Errorf("pgx.ParseConfig() = %w", err)
	}
	db, err := sql.Open("pgx", conn)
	if err != nil {
		return fmt.Errorf("sql.Open(%q, _) = %v, %w", "pgx", db, err)
	}
	mcfg := &dbpgx.Config{
		MigrationsTable: "migrations",
		DatabaseName:    cfg.Database,
	}
	mdb, err := dbpgx.WithInstance(db, mcfg)
	if err != nil {
		return fmt.Errorf("dbpgx.WithInstance(_, %v) = %v, %w", mcfg, mdb, err)
	}
	mfs, err := iofs.New(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("iofs.New(%v, %q) = %v, %w", migrations, "migrations", mfs, err)
	}
	m, err := migrate.NewWithInstance("iofs", mfs, "pgx", mdb)
	if err != nil {
		return fmt.Errorf("migrate.NewWithInstance(%q, %v, %q, %v) = %v, %w", "iofs", migrations, "pgx", mdb, m, err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("m.Up() = %w", err)
	}
	db.Close()
	return nil
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
