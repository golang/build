// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// relui is a web interface for managing the release process of Go.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/url"
	"time"

	"cloud.google.com/go/storage"
	"github.com/jackc/pgx/v4/pgxpool"
	"golang.org/x/build"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/https"
	"golang.org/x/build/internal/relui"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/internal/task"
)

var (
	baseURL       = flag.String("base-url", "", "Prefix URL for routing and links.")
	siteTitle     = flag.String("site-title", "Go Releases", "Site title.")
	siteHeaderCSS = flag.String("site-header-css", "", "Site header CSS class name. Can be used to pick a look for the header.")

	downUp      = flag.Bool("migrate-down-up", false, "Run all Up migration steps, then the last down migration step, followed by the final up migration. Exits after completion.")
	migrateOnly = flag.Bool("migrate-only", false, "Exit after running migrations. Migrations are run by default.")
	pgConnect   = flag.String("pg-connect", "", "Postgres connection string or URI. If empty, libpq connection defaults are used.")

	scratchFilesBase = flag.String("scratch-files-base", "", "Storage for scratch files. gs://bucket/path or file:///path/to/scratch.")
	stagingFilesBase = flag.String("staging-files-base", "", "Storage for staging files. gs://bucket/path or file:///path/to/staging.")
)

func main() {
	rand.Seed(time.Now().Unix())
	if err := secret.InitFlagSupport(context.Background()); err != nil {
		log.Fatalln(err)
	}
	gerritAPIFlag := secret.Flag("gerrit-api-secret", "Gerrit API secret to use for workflows that interact with Gerrit.")
	var twitterAPI secret.TwitterCredentials
	secret.JSONVarFlag(&twitterAPI, "twitter-api-secret", "Twitter API secret to use for workflows involving tweeting.")
	masterKey := secret.Flag("builder-master-key", "Builder master key")
	https.RegisterFlags(flag.CommandLine)
	flag.Parse()

	ctx := context.Background()
	if err := relui.InitDB(ctx, *pgConnect); err != nil {
		log.Fatalf("relui.InitDB() = %v", err)
	}
	if *migrateOnly {
		return
	}
	if *downUp {
		if err := relui.MigrateDB(*pgConnect, true); err != nil {
			log.Fatalf("relui.MigrateDB() = %v", err)
		}
		return
	}

	// Define the site header and external service configuration.
	// The site header communicates to humans what will happen
	// when workflows run.
	// Keep these appropriately in sync.
	var (
		siteHeader = relui.SiteHeader{
			Title:    *siteTitle,
			CSSClass: *siteHeaderCSS,
		}
		extCfg = task.ExternalConfig{
			GerritAPI: struct {
				URL  string
				Auth gerrit.Auth
			}{"https://go-review.googlesource.com", gerrit.BasicAuth("git-gobot.golang.org", *gerritAPIFlag)},
			// TODO(go.dev/issue/51150): When twitter client creation is factored out from task package, update code here.
			TwitterAPI: twitterAPI,
		}
	)

	dh := relui.NewDefinitionHolder()
	relui.RegisterMailDLCLDefinition(dh, extCfg)
	relui.RegisterTweetDefinitions(dh, extCfg)
	coordinator := &buildlet.CoordinatorClient{
		Auth: buildlet.UserPass{
			Username: "user-relui",
			Password: key(*masterKey, "user-relui"),
		},
		Instance: build.ProdCoordinator,
	}
	if _, err := coordinator.RemoteBuildlets(); err != nil {
		log.Fatalf("Broken coordinator client: %v", err)
	}
	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatalf("Could not connect to GCS: %v", err)
	}
	releaseTasks := &relui.BuildReleaseTasks{
		CreateBuildlet: coordinator.CreateBuildlet,
		GCSClient:      gcsClient,
		ScratchURL:     *scratchFilesBase,
		StagingURL:     *stagingFilesBase,
	}
	releaseTasks.RegisterBuildReleaseWorkflows(dh)
	db, err := pgxpool.Connect(ctx, *pgConnect)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	w := relui.NewWorker(dh, db, relui.NewPGListener(db))
	go w.Run(ctx)
	if err := w.ResumeAll(ctx); err != nil {
		log.Printf("w.ResumeAll() = %v", err)
	}
	var base *url.URL
	if *baseURL != "" {
		base, err = url.Parse(*baseURL)
		if err != nil {
			log.Fatalf("url.Parse(%q) = %v, %v", *baseURL, base, err)
		}
	}
	s := relui.NewServer(db, w, base, siteHeader)
	if err != nil {
		log.Fatalf("relui.NewServer() = %v", err)
	}
	log.Fatalln(https.ListenAndServe(ctx, s))
}

func key(masterKey, principal string) string {
	h := hmac.New(md5.New, []byte(masterKey))
	io.WriteString(h, principal)
	return fmt.Sprintf("%x", h.Sum(nil))
}
