// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// relui is a web interface for managing the release process of Go.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/go-github/github"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/shurcooL/githubv4"
	"golang.org/x/build"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/https"
	"golang.org/x/build/internal/relui"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/internal/task"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var (
	baseURL       = flag.String("base-url", "", "Prefix URL for routing and links.")
	siteTitle     = flag.String("site-title", "Go Releases", "Site title.")
	siteHeaderCSS = flag.String("site-header-css", "", "Site header CSS class name. Can be used to pick a look for the header.")

	downUp      = flag.Bool("migrate-down-up", false, "Run all Up migration steps, then the last down migration step, followed by the final up migration. Exits after completion.")
	migrateOnly = flag.Bool("migrate-only", false, "Exit after running migrations. Migrations are run by default.")
	pgConnect   = flag.String("pg-connect", "", "Postgres connection string or URI. If empty, libpq connection defaults are used.")

	scratchFilesBase = flag.String("scratch-files-base", "", "Storage for scratch files. gs://bucket/path or file:///path/to/scratch.")
	servingFilesBase = flag.String("serving-files-base", "", "Storage for serving files. gs://bucket/path or file:///path/to/serving.")
	edgeCacheURL     = flag.String("edge-cache-url", "", "URL release files appear at when published to the CDN, e.g. https://dl.google.com/go.")
	websiteUploadURL = flag.String("website-upload-url", "", "URL to POST website file data to, e.g. https://go.dev/dl/upload.")
)

func main() {
	rand.Seed(time.Now().Unix())
	if err := secret.InitFlagSupport(context.Background()); err != nil {
		log.Fatalln(err)
	}
	sendgridAPIKey := secret.Flag("sendgrid-api-key", "SendGrid API key for workflows involving sending email.")
	var annMail task.MailHeader
	addressVarFlag(&annMail.From, "announce-mail-from", "The From address to use for the announcement mail.")
	addressVarFlag(&annMail.To, "announce-mail-to", "The To address to use for the announcement mail.")
	addressListVarFlag(&annMail.BCC, "announce-mail-bcc", "The BCC address list to use for the announcement mail.")
	var twitterAPI secret.TwitterCredentials
	secret.JSONVarFlag(&twitterAPI, "twitter-api-secret", "Twitter API secret to use for workflows involving tweeting.")
	masterKey := secret.Flag("builder-master-key", "Builder master key")
	githubToken := secret.Flag("github-token", "GitHub API token")
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
	siteHeader := relui.SiteHeader{
		Title:    *siteTitle,
		CSSClass: *siteHeaderCSS,
	}
	creds, err := google.FindDefaultCredentials(ctx, gerrit.OAuth2Scopes...)
	if err != nil {
		log.Fatalf("reading GCP credentials: %v", err)
	}
	gerritClient := &task.RealGerritClient{
		Client: gerrit.NewClient("https://go-review.googlesource.com", gerrit.OAuth2Auth(creds.TokenSource)),
	}
	versionTasks := &task.VersionTasks{
		Gerrit:    gerritClient,
		GoProject: "go",
	}
	commTasks := task.CommunicationTasks{
		AnnounceMailTasks: task.AnnounceMailTasks{
			SendMail:           task.NewSendGridMailClient(*sendgridAPIKey).SendMail,
			AnnounceMailHeader: annMail,
		},
		TweetTasks: task.TweetTasks{
			TwitterClient: task.NewTwitterClient(twitterAPI),
		},
	}
	dh := relui.NewDefinitionHolder()
	relui.RegisterMailDLCLDefinition(dh, versionTasks)
	relui.RegisterCommunicationDefinitions(dh, commTasks)
	relui.RegisterAnnounceMailOnlyDefinitions(dh, commTasks.AnnounceMailTasks)
	relui.RegisterTweetOnlyDefinitions(dh, commTasks.TweetTasks)
	userPassAuth := buildlet.UserPass{
		Username: "user-relui",
		Password: key(*masterKey, "user-relui"),
	}
	coordinator := &buildlet.CoordinatorClient{
		Auth:     userPassAuth,
		Instance: build.ProdCoordinator,
	}
	if _, err := coordinator.RemoteBuildlets(); err != nil {
		log.Fatalf("Broken coordinator client: %v", err)
	}
	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatalf("Could not connect to GCS: %v", err)
	}
	db, err := pgxpool.Connect(ctx, *pgConnect)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	buildTasks := &relui.BuildReleaseTasks{
		GerritHTTPClient: oauth2.NewClient(ctx, creds.TokenSource),
		GerritURL:        "https://go.googlesource.com/go",
		PrivateGerritURL: "https://team.googlesource.com/golang/go-private",
		CreateBuildlet:   coordinator.CreateBuildlet,
		GCSClient:        gcsClient,
		ScratchURL:       *scratchFilesBase,
		ServingURL:       *servingFilesBase,
		DownloadURL:      *edgeCacheURL,
		PublishFile: func(f *relui.WebsiteFile) error {
			return publishFile(*websiteUploadURL, userPassAuth, f)
		},
		ApproveAction: relui.ApproveActionDep(db),
	}
	githubHTTPClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: *githubToken}))
	milestoneTasks := &task.MilestoneTasks{
		Client: &task.GitHubClient{
			V3: github.NewClient(githubHTTPClient),
			V4: githubv4.NewClient(githubHTTPClient),
		},
		RepoOwner: "golang",
		RepoName:  "go",
	}
	if err := relui.RegisterReleaseWorkflows(ctx, dh, buildTasks, milestoneTasks, versionTasks, commTasks); err != nil {
		log.Fatalf("RegisterReleaseWorkflows: %v", err)
	}

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

func publishFile(uploadURL string, auth buildlet.UserPass, f *relui.WebsiteFile) error {
	req, err := json.Marshal(f)
	if err != nil {
		return err
	}
	u, err := url.Parse(uploadURL)
	if err != nil {
		return fmt.Errorf("invalid website upload URL %q: %v", *websiteUploadURL, err)
	}
	q := u.Query()
	q.Set("user", strings.TrimPrefix(auth.Username, "user-"))
	q.Set("key", auth.Password)
	u.RawQuery = q.Encode()
	resp, err := http.Post(u.String(), "application/json", bytes.NewReader(req))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("upload failed to %q: %v\n%s", uploadURL, resp.Status, b)
	}
	return nil
}

// addressVarFlag defines an address flag with specified name and usage string.
// The argument p points to a mail.Address variable in which to store the value of the flag.
func addressVarFlag(p *mail.Address, name, usage string) {
	flag.Func(name, usage, func(s string) error {
		a, err := mail.ParseAddress(s)
		if err != nil {
			return err
		}
		*p = *a
		return nil
	})
}

// addressListVarFlag defines an address list flag with specified name and usage string.
// The argument p points to a []mail.Address variable in which to store the value of the flag.
func addressListVarFlag(p *[]mail.Address, name, usage string) {
	flag.Func(name, usage, func(s string) error {
		as, err := mail.ParseAddressList(s)
		if err != nil {
			return err
		}
		*p = nil // Clear out the list before appending.
		for _, a := range as {
			*p = append(*p, *a)
		}
		return nil
	})
}
