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
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	cloudbuild "cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/storage"
	"github.com/google/go-github/v74/github"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/shurcooL/githubv4"
	"go.chromium.org/luci/auth"
	pb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/grpc/prpc"
	"go.chromium.org/luci/swarming/client/swarming"
	"go.opencensus.io/plugin/ochttp"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/criadb"
	"golang.org/x/build/internal/https"
	"golang.org/x/build/internal/metrics"
	"golang.org/x/build/internal/relui"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/relui/protos"
	"golang.org/x/build/internal/relui/sign"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/internal/task"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/build/repos"
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
	signedFilesBase  = flag.String("signed-files-base", "", "Storage for signed files. gs://bucket/path or file:///path/to/signed.")
	servingFilesBase = flag.String("serving-files-base", "", "Storage for serving files. gs://bucket/path or file:///path/to/serving.")
	edgeCacheURL     = flag.String("edge-cache-url", "", "URL release files appear at when published to the CDN, e.g. https://dl.google.com/go.")
	websiteUploadURL = flag.String("website-upload-url", "", "URL to POST website file data to, e.g. https://go.dev/dl/upload.")

	cloudBuildProject = flag.String("cloud-build-project", "", "GCP project to run miscellaneous Cloud Build tasks")
	cloudBuildAccount = flag.String("cloud-build-account", "", "Service account to run miscellaneous Cloud Build tasks")

	swarmingURL     = flag.String("swarming-url", "", "Swarming service to use for tasks")
	swarmingAccount = flag.String("swarming-account", "", "Service account to use for Swarming tasks")
	swarmingPool    = flag.String("swarming-pool", "", "Swarming pool to run tasks in")
	swarmingRealm   = flag.String("swarming-realm", "", "Swarming realm to run tasks in")

	buildbucketHost = flag.String("buildbucket-host", "", "Buildbucket host to use for tasks")
	criaService     = flag.String("cria-service", "chrome-infra-auth", "CrIA service name")
)

func main() {
	if err := secret.InitFlagSupport(context.Background()); err != nil {
		log.Fatalln(err)
	}
	sendgridAPIKey := secret.Flag("sendgrid-api-key", "SendGrid API key for workflows involving sending email (can't be used if -mailjet-api-key is set).")
	var mailjetAPIKey secret.MailjetCredentials
	secret.JSONVarFlag(&mailjetAPIKey, "mailjet-api-key", "Mailjet API key for workflows involving sending email (can't be used if -sendgrid-api-key is set).")
	var annMail task.MailHeader
	addressVarFlag(&annMail.From, "announce-mail-from", "The From address to use for the (pre-)announcement mail.")
	addressVarFlag(&annMail.To, "announce-mail-to", "The To address to use for the (pre-)announcement mail.")
	addressListVarFlag(&annMail.BCC, "announce-mail-bcc", "The BCC address list to use for the (pre-)announcement mail.")
	var schedMail task.MailHeader
	addressVarFlag(&schedMail.From, "schedule-mail-from", "The From address to use for the scheduled workflow failure mail.")
	addressVarFlag(&schedMail.To, "schedule-mail-to", "The To address to use for the scheduled workflow failure mail.")
	addressListVarFlag(&schedMail.BCC, "schedule-mail-bcc", "The BCC address list to use for the scheduled workflow failure mail.")
	var goplsAnnMail task.MailHeader
	addressVarFlag(&goplsAnnMail.From, "gopls-announce-mail-from", "The From address to use for the gopls (pre-)announcement mail.")
	addressVarFlag(&goplsAnnMail.To, "gopls-announce-mail-to", "The To address to use for the gopls (pre-)announcement mail.")
	var vscodeGoAnnMail task.MailHeader
	addressVarFlag(&vscodeGoAnnMail.From, "vscode-go-announce-mail-from", "The From address to use for the vscode-go (pre-)announcement mail.")
	addressVarFlag(&vscodeGoAnnMail.To, "vscode-go-announce-mail-to", "The To address to use for the vscode-go (pre-)announcement mail.")
	var twitterAPI secret.TwitterCredentials
	secret.JSONVarFlag(&twitterAPI, "twitter-api-secret", "Twitter API secret to use for workflows involving tweeting.")
	var mastodonAPI secret.MastodonCredentials
	secret.JSONVarFlag(&mastodonAPI, "mastodon-api-secret", "Mastodon API secret to use for workflows involving posting.")
	masterKey := secret.Flag("builder-master-key", "Builder master key")
	var blueskyAPI secret.BlueskyCredentials
	secret.JSONVarFlag(&blueskyAPI, "bluesky-api-secret", "Bluesky API secret to use for workflows involving posting.")
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

	var dbPool db.PGDBTX
	dbPool, err := pgxpool.Connect(ctx, *pgConnect)
	if err != nil {
		log.Fatalln("pgxpool.Connect:", err)
	}
	defer dbPool.Close()
	dbPool = &relui.MetricsDB{PGDBTX: dbPool}

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
		Gitiles: "https://go.googlesource.com", GitilesAuth: creds.TokenSource,
		Client: gerrit.NewClient("https://go-review.googlesource.com", gerrit.OAuth2Auth(creds.TokenSource)),
	}
	privateGerritClient := &task.RealGerritClient{
		Gitiles: "https://go-internal.googlesource.com", GitilesAuth: creds.TokenSource,
		Client: gerrit.NewClient("https://go-internal-review.googlesource.com", gerrit.OAuth2Auth(creds.TokenSource)),
	}
	modproxyTestGerritClient := &task.RealGerritClient{
		Gitiles: "https://golang-modproxy-test.googlesource.com", GitilesAuth: creds.TokenSource,
		Client: gerrit.NewClient("https://golang-modproxy-test-review.googlesource.com", gerrit.OAuth2Auth(creds.TokenSource)),
	}
	gitClient := &task.Git{}
	gitClient.UseOAuth2Auth(creds.TokenSource)
	var mailFunc func(*workflow.TaskContext, task.MailHeader, task.MailContent) error
	switch {
	case *sendgridAPIKey != "" && mailjetAPIKey != (secret.MailjetCredentials{}):
		log.Fatalln("at most one of -sendgrid-api-key and -mailjet-api-key can be set at once")
	case *sendgridAPIKey != "":
		mailFunc = task.NewSendGridMailClient(*sendgridAPIKey).SendMail
	case mailjetAPIKey != (secret.MailjetCredentials{}):
		mailFunc = task.NewMailjetMailClient(mailjetAPIKey).SendMail
	default:
		mailFunc = task.ReleaseCoordinatorAsTheMailSender{ApproveAction: relui.ApproveActionDep(dbPool)}.SendMail
	}
	var mastodonClient task.Poster
	if mastodonAPI != (secret.MastodonCredentials{}) {
		var err error
		mastodonClient, err = task.NewMastodonClient(mastodonAPI)
		if err != nil {
			log.Fatalln("task.NewMastodonClient:", err)
		}
	}
	var blueskyClient task.Poster
	if blueskyAPI != (secret.BlueskyCredentials{}) {
		var err error
		blueskyClient, err = task.NewBlueskyClient(blueskyAPI)
		if err != nil {
			log.Fatalln("task.NewBlueskyClient:", err)
		}
	}
	commTasks := task.CommunicationTasks{
		AnnounceMailTasks: task.AnnounceMailTasks{
			SendMail:           mailFunc,
			AnnounceMailHeader: annMail,
		},
		SocialMediaTasks: task.SocialMediaTasks{
			TwitterClient:  task.NewTwitterClient(twitterAPI),
			MastodonClient: mastodonClient,
			BlueskyClient:  blueskyClient,
		},
	}
	dh := relui.NewDefinitionHolder()
	userPassAuth := buildlet.UserPass{
		Username: "user-relui",
		Password: key(*masterKey, "user-relui"),
	}
	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatalf("Could not connect to GCS: %v", err)
	}
	cbClient, err := cloudbuild.NewClient(ctx)
	if err != nil {
		log.Fatalf("Could not connect to Cloud Build: %v", err)
	}
	cloudBuildClient := &task.RealCloudBuildClient{
		BuildClient:   cbClient,
		StorageClient: gcsClient,
		ScriptProject: *cloudBuildProject,
		ScriptAccount: *cloudBuildAccount,
		ScratchURL:    *scratchFilesBase + "/build-outputs",
	}
	var swarmingClient swarming.Client
	if *swarmingURL != "" {
		var err error
		swarmingClient, err = swarming.NewClient(ctx, swarming.ClientOptions{
			ServiceURL: *swarmingURL,
			Auth: auth.Options{
				GCEAllowAsDefault: true,
			},
		})
		if err != nil {
			log.Fatalln("swarming.NewClient:", err)
		}
	}
	var buildBucketClient task.BuildBucketClient
	if *buildbucketHost != "" {
		luciHTTPClient, err := auth.NewAuthenticator(ctx, auth.SilentLogin, auth.Options{GCEAllowAsDefault: true}).Client()
		if err != nil {
			log.Fatalln("auth.NewAuthenticator:", err)
		}
		buildBucketClient = &task.RealBuildBucketClient{
			BuildersClient: pb.NewBuildersClient(&prpc.Client{
				C:    luciHTTPClient,
				Host: *buildbucketHost,
			}),
			BuildsClient: pb.NewBuildsClient(&prpc.Client{
				C:    luciHTTPClient,
				Host: *buildbucketHost,
			}),
		}
	}

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
	signServer := sign.NewServer()
	protos.RegisterReleaseServiceServer(grpcServer, signServer)
	buildTasks := &relui.BuildReleaseTasks{
		GerritClient:         gerritClient,
		GerritProject:        "go",
		GerritHTTPClient:     oauth2.NewClient(ctx, creds.TokenSource),
		PrivateGerritClient:  privateGerritClient,
		PrivateGerritProject: "go",
		SignService:          signServer,
		GCSClient:            gcsClient,
		ScratchFS: &task.ScratchFS{
			BaseURL: *scratchFilesBase,
			GCS:     gcsClient,
		},
		SignedURL:         *signedFilesBase,
		ServingURL:        *servingFilesBase,
		DownloadURL:       *edgeCacheURL,
		ProxyPrefix:       "https://proxy.golang.org/golang.org/toolchain/@v",
		CloudBuildClient:  cloudBuildClient,
		BuildBucketClient: buildBucketClient,
		SwarmingClient: &task.RealSwarmingClient{
			SwarmingClient: swarmingClient,
			SwarmingURL:    *swarmingURL,
			ServiceAccount: *swarmingAccount,
			Realm:          *swarmingRealm,
			Pool:           *swarmingPool,
		},
		GoogleDockerBuildProject: "symbolic-datum-552",
		GoogleDockerBuildTrigger: "golang-publish-internal-boringcrypto",
		PublishFile: func(f task.WebsiteFile) error {
			return publishFile(*websiteUploadURL, userPassAuth, f)
		},
		ApproveAction: relui.ApproveActionDep(dbPool),
	}
	githubHTTPClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: *githubToken}))
	githubClient := &task.GitHubClient{
		V3: github.NewClient(githubHTTPClient),
		V4: githubv4.NewClient(githubHTTPClient),
	}
	milestoneTasks := &task.MilestoneTasks{
		Client:        githubClient,
		RepoOwner:     "golang",
		RepoName:      "go",
		ApproveAction: relui.ApproveActionDep(dbPool),
	}
	versionTasks := &task.VersionTasks{
		Gerrit:     gerritClient,
		CloudBuild: cloudBuildClient,
		GoProject:  "go",
		GoDirectiveXReposTasks: task.GoDirectiveXReposTasks{
			Gerrit:     gerritClient,
			CloudBuild: cloudBuildClient,
		},
		UpdateProxyTestRepoTasks: task.UpdateProxyTestRepoTasks{
			Gerrit:  modproxyTestGerritClient,
			Project: "latest-go-version", Branch: "main",
			ChangeLink: func(changeID string) string {
				parts := strings.SplitN(changeID, "~", 3)
				if len(parts) != 2 {
					return fmt.Sprintf("(unparseable change ID %q)", changeID)
				}
				return "https://golang-modproxy-test-review.googlesource.com/c/latest-go-version/+/" + parts[1]
			},
		},
	}
	cycleTasks := task.ReleaseCycleTasks{
		Gerrit:        gerritClient,
		GitHub:        githubClient,
		ApproveAction: relui.ApproveActionDep(dbPool),
	}
	if err := relui.RegisterReleaseWorkflows(ctx, dh, buildTasks, milestoneTasks, versionTasks, cycleTasks, commTasks); err != nil {
		log.Fatalf("RegisterReleaseWorkflows: %v", err)
	}

	ignoreProjects := map[string]bool{}
	for p, r := range repos.ByGerritProject {
		ignoreProjects[p] = !r.AutoTagNextMinorVersion
	}
	tagTasks := &task.TagXReposTasks{
		IgnoreProjects: ignoreProjects,
		Gerrit:         gerritClient,
		CloudBuild:     cloudBuildClient,
		BuildBucket:    buildBucketClient,
	}
	dh.RegisterDefinition("Tag x/ repos", tagTasks.NewDefinition())
	dh.RegisterDefinition("Tag a single x/ repo", tagTasks.NewSingleDefinition())

	bundleTasks := &task.BundleNSSRootsTask{
		Gerrit:     gerritClient,
		CloudBuild: cloudBuildClient,
	}
	dh.RegisterDefinition("Update x/crypto NSS root bundle", bundleTasks.NewDefinition())

	// Register a workflow for announcing a blog post.
	dh.RegisterDefinition("Announce blog post", relui.NewAnnounceBlogPostWorkflow(commTasks.SocialMediaTasks))

	releaseVSCodeGoTasks := task.ReleaseVSCodeGoTasks{
		GitHub:             githubClient,
		Gerrit:             gerritClient,
		CloudBuild:         cloudBuildClient,
		ApproveAction:      relui.ApproveActionDep(dbPool),
		SendMail:           mailFunc,
		AnnounceMailHeader: vscodeGoAnnMail,
	}
	dh.RegisterDefinition("Create a vscode-go release candidate", releaseVSCodeGoTasks.NewPrereleaseDefinition())
	dh.RegisterDefinition("Release a vscode-go stable version", releaseVSCodeGoTasks.NewReleaseDefinition())
	dh.RegisterDefinition("Release a vscode-go insider version", releaseVSCodeGoTasks.NewInsiderDefinition())

	tagTelemetryTasks := &task.TagTelemetryTasks{
		Gerrit:     gerritClient,
		CloudBuild: cloudBuildClient,
	}
	dh.RegisterDefinition("Tag a new version of x/telemetry/config (if necessary)", tagTelemetryTasks.NewDefinition())

	goplsTasks := task.ReleaseGoplsTasks{
		GitHub:             githubClient,
		Gerrit:             gerritClient,
		CloudBuild:         cloudBuildClient,
		SendMail:           mailFunc,
		AnnounceMailHeader: goplsAnnMail,
		ApproveAction:      relui.ApproveActionDep(dbPool),
	}
	dh.RegisterDefinition("Prepare a pre-release gopls candidate", goplsTasks.NewPrereleaseDefinition())
	dh.RegisterDefinition("Release gopls", goplsTasks.NewReleaseDefinition())

	privateSyncTask := &task.PrivateMasterSyncTask{
		Git:              gitClient,
		PrivateGerritURL: "https://go-internal.googlesource.com/golang/go-private",
		Ref:              "public",
	}
	dh.RegisterDefinition("Sync go-private master branch with public", privateSyncTask.NewDefinition())

	privateXPatchTask := &task.PrivXPatch{
		Git:           gitClient,
		PublicGerrit:  gerritClient,
		PrivateGerrit: privateGerritClient,
		PublicRepoURL: func(repo string) string {
			return "https://go.googlesource.com/" + repo
		},
		ApproveAction:      relui.ApproveActionDep(dbPool),
		SendMail:           mailFunc,
		AnnounceMailHeader: annMail,
	}
	dh.RegisterDefinition("Publish a private patch to a x/ repo", privateXPatchTask.NewDefinition(tagTasks))

	securityReleaseCoalesceTask := &task.SecurityReleaseCoalesceTask{
		PrivateGerrit: privateGerritClient,
		Version:       versionTasks,
	}
	dh.RegisterDefinition("Prepare internal security release branches (cl-num based)", securityReleaseCoalesceTask.NewDefinition(false))
	dh.RegisterDefinition("Prepare internal security release branches (metadata based)", securityReleaseCoalesceTask.NewDefinition(true))

	var base *url.URL
	if *baseURL != "" {
		base, err = url.Parse(*baseURL)
		if err != nil {
			log.Fatalf("url.Parse(%q) = %v, %v", *baseURL, base, err)
		}
	}
	l := &relui.PGListener{
		DB:                        dbPool,
		BaseURL:                   base,
		ScheduleFailureMailHeader: schedMail,
		SendMail:                  nil, // TODO(go.dev/issue/74777): Restore email notifications about workflow failures.
	}
	w := relui.NewWorker(dh, dbPool, l)
	go w.Run(ctx)
	if err := w.ResumeAll(ctx); err != nil {
		log.Printf("w.ResumeAll() = %v", err)
	}
	var prod bool // are we operating from symbolic-datum-552
	var criaDB *criadb.AuthDatabase
	if metadata.OnGCE() {
		project, err := metadata.ProjectID()
		if err != nil {
			log.Fatalln("failed to read project ID from metadata server")
		}
		prod = project == "symbolic-datum-552"
		if prod {
			criaDB, err = criadb.NewDatabase(*criaService)
			if err != nil {
				log.Fatalf("failed to create cria authdb: %s", err)
			}
		}
	} else {
		criaDB = criadb.NewDevDatabase()
	}
	var h http.Handler = relui.NewServer(dbPool, w, base, siteHeader, ms, criaDB)
	if prod {
		h = access.RequireIAPAuthHandler(h, access.IAPSkipAudienceValidation)
	}
	log.Fatalln(https.ListenAndServe(ctx, &ochttp.Handler{Handler: GRPCHandler(grpcServer, h)}))
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

func publishFile(uploadURL string, auth buildlet.UserPass, f task.WebsiteFile) error {
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
		b, _ := io.ReadAll(resp.Body)
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
