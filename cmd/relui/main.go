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

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/storage"
	"github.com/google/go-github/github"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/shurcooL/githubv4"
	"go.opencensus.io/plugin/ochttp"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/access"
	gomotepb "golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/https"
	"golang.org/x/build/internal/iapclient"
	"golang.org/x/build/internal/metrics"
	"golang.org/x/build/internal/relui"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/relui/protos"
	"golang.org/x/build/internal/relui/sign"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/internal/task"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/grpc"
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
	addressVarFlag(&annMail.From, "announce-mail-from", "The From address to use for the (pre-)announcement mail.")
	addressVarFlag(&annMail.To, "announce-mail-to", "The To address to use for the (pre-)announcement mail.")
	addressListVarFlag(&annMail.BCC, "announce-mail-bcc", "The BCC address list to use for the (pre-)announcement mail.")
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
	relui.RegisterCommunicationDefinitions(dh, commTasks)
	userPassAuth := buildlet.UserPass{
		Username: "user-relui",
		Password: key(*masterKey, "user-relui"),
	}
	cc, err := iapclient.GRPCClient(ctx, "build.golang.org:443")
	if err != nil {
		log.Fatalf("Could not connect to coordinator: %v", err)
	}
	coordinator := &buildlet.GRPCCoordinatorClient{
		Client: gomotepb.NewGomoteServiceClient(cc),
	}
	if _, err := coordinator.Client.Authenticate(ctx, &gomotepb.AuthenticateRequest{}); err != nil {
		log.Fatalf("Broken coordinator client: %v", err)
	}
	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatalf("Could not connect to GCS: %v", err)
	}
	var dbPool db.PGDBTX
	dbPool, err = pgxpool.Connect(ctx, *pgConnect)
	if err != nil {
		log.Fatal(err)
	}
	defer dbPool.Close()
	dbPool = &relui.MetricsDB{dbPool}

	var gr *metrics.MonitoredResource
	if metadata.OnGCE() {
		gr, err = metrics.GKEResource("relui-deployment")
		if err != nil {
			log.Println("metrics.GKEResource:", err)
		}
	}
	ms, err := metrics.NewService(gr, relui.Views)
	if err != nil {
		log.Println("failed to initialize metrics:", err)
	} else {
		defer ms.Stop()
	}
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(access.RequireIAPAuthUnaryInterceptor(access.IAPSkipAudienceValidation)),
		grpc.StreamInterceptor(access.RequireIAPAuthStreamInterceptor(access.IAPSkipAudienceValidation)))
	protos.RegisterReleaseServiceServer(grpcServer, sign.NewServer())
	buildTasks := &relui.BuildReleaseTasks{
		GerritClient:     gerritClient,
		GerritHTTPClient: oauth2.NewClient(ctx, creds.TokenSource),
		GerritURL:        "https://go.googlesource.com/go",
		PrivateGerritURL: "https://team.googlesource.com/golang/go-private",
		CreateBuildlet:   coordinator.CreateBuildlet,
		GCSClient:        gcsClient,
		ScratchURL:       *scratchFilesBase,
		ServingURL:       *servingFilesBase,
		DownloadURL:      *edgeCacheURL,
		PublishFile: func(f *task.WebsiteFile) error {
			return publishFile(*websiteUploadURL, userPassAuth, f)
		},
		ApproveAction: relui.ApproveActionDep(dbPool),
	}
	githubHTTPClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: *githubToken}))
	milestoneTasks := &task.MilestoneTasks{
		Client: &task.GitHubClient{
			V3: github.NewClient(githubHTTPClient),
			V4: githubv4.NewClient(githubHTTPClient),
		},
		RepoOwner:     "golang",
		RepoName:      "go",
		ApproveAction: relui.ApproveActionDep(dbPool),
	}
	if err := relui.RegisterReleaseWorkflows(ctx, dh, buildTasks, milestoneTasks, versionTasks, commTasks); err != nil {
		log.Fatalf("RegisterReleaseWorkflows: %v", err)
	}

	w := relui.NewWorker(dh, dbPool, relui.NewPGListener(dbPool))
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
	s := relui.NewServer(dbPool, w, base, siteHeader, ms)
	log.Fatalln(https.ListenAndServe(ctx, &ochttp.Handler{Handler: GRPCHandler(grpcServer, s)}))
}

// GRPCHandler creates handler which intercepts requests intended for a GRPC server and directs the calls to the server.
// All other requests are directed toward the passed in handler.
func GRPCHandler(gs *grpc.Server, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			gs.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func key(masterKey, principal string) string {
	h := hmac.New(md5.New, []byte(masterKey))
	io.WriteString(h, principal)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func publishFile(uploadURL string, auth buildlet.UserPass, f *task.WebsiteFile) error {
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
