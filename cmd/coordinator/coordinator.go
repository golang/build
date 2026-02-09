// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

// The coordinator runs the majority of the Go build system.
//
// It is responsible for finding build work, executing it,
// and displaying the results.
//
// For an overview of the Go build system, see the README at
// the root of the x/build repo.
package main // import "golang.org/x/build/cmd/coordinator"

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/storage"
	"go.chromium.org/luci/auth"
	buildbucketpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/grpc/prpc"
	"go.chromium.org/luci/hardcoded/chromeinfra"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	builddash "golang.org/x/build/cmd/coordinator/internal/dashboard"
	"golang.org/x/build/cmd/coordinator/internal/legacydash"
	"golang.org/x/build/cmd/coordinator/internal/lucipoll"
	"golang.org/x/build/cmd/coordinator/protos"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/internal/buildstats"
	"golang.org/x/build/internal/cloud"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/coordinator/pool/queue"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/coordinator/schedule"
	"golang.org/x/build/internal/gomote"
	gomoteprotos "golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/https"
	"golang.org/x/build/internal/metrics"
	"golang.org/x/build/internal/migration"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/kubernetes/gke"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/repos"
	"golang.org/x/build/revdial/v2"
	"golang.org/x/build/types"
	"golang.org/x/time/rate"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	// eventDone is a build event name meaning the build was
	// completed (either successfully or with remote errors).
	// Notably, it is NOT included for network/communication
	// errors.
	eventDone = "done"

	// eventSkipBuildMissingDep is a build event name meaning
	// the builder type is not applicable to the commit being
	// tested because the commit lacks a necessary dependency
	// in its git history.
	eventSkipBuildMissingDep = "skipped_build_missing_dep"
)

var (
	processStartTime = time.Now()
	processID        = "P" + randHex(9)
)

var sched = schedule.NewScheduler()

var Version string // set by linker -X

// devPause is a debug option to pause for 5 minutes after the build
// finishes before destroying buildlets.
const devPause = false

// stagingTryWork is a debug option to enable or disable running
// trybot work in staging.
//
// If enabled, only open CLs containing "DO NOT SUBMIT" and "STAGING"
// in their commit message (in addition to being marked Run-TryBot+1)
// will be run.
const stagingTryWork = true

var (
	masterKeyFile = flag.String("masterkey", "", "Path to builder master key. Else fetched using GCE project attribute 'builder-master-key'.")
	mode          = flag.String("mode", "", "Valid modes are 'dev', 'prod', or '' for auto-detect. dev means localhost development, not be confused with staging on go-dashboard-dev, which is still the 'prod' mode.")
	buildEnvName  = flag.String("env", "", "The build environment configuration to use. Not required if running in dev mode locally or prod mode on GCE.")
	devEnableGCE  = flag.Bool("dev_gce", false, "Whether or not to enable the GCE pool when in dev mode. The pool is enabled by default in prod mode.")
	devEnableEC2  = flag.Bool("dev_ec2", false, "Whether or not to enable the EC2 pool when in dev mode. The pool is enabled by default in prod mode.")
	sshAddr       = flag.String("ssh_addr", ":2222", "Address the gomote SSH server should listen on")
)

// LOCK ORDER:
//   statusMu, buildStatus.mu, trySet.mu
// (Other locks, such as the remoteBuildlet mutex should
// not be used along with other locks)

var (
	statusMu   sync.Mutex // guards the following four structures; see LOCK ORDER comment above
	status     = map[buildgo.BuilderRev]*buildStatus{}
	statusDone []*buildStatus         // finished recently, capped to maxStatusDone
	tries      = map[tryKey]*trySet{} // trybot builds
	tryList    []tryKey
)

var maintnerClient apipb.MaintnerServiceClient

const (
	maxStatusDone = 30
)

var validHosts = map[string]bool{
	"farmer.golang.org": true,
	"build.golang.org":  true,
}

// hostPathHandler infers the host from the first element of the URL path,
// and rewrites URLs in the output HTML accordingly. It disables response
// compression to simplify the process of link rewriting.
func hostPathHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't bother rewriting ReverseHandler requests. ReverseHandler
		// must be a Hijacker. Other handlers must not be a Hijacker to
		// serve HTTP/2 requests.
		if strings.HasPrefix(r.URL.Path, "/reverse") || strings.HasPrefix(r.URL.Path, "/revdial") {
			h.ServeHTTP(w, r)
			return
		}
		elem, rest := strings.TrimPrefix(r.URL.Path, "/"), ""
		if i := strings.Index(elem, "/"); i >= 0 {
			elem, rest = elem[:i], elem[i+1:]
		}
		if !validHosts[elem] {
			u := "/farmer.golang.org" + r.URL.EscapedPath()
			if r.URL.RawQuery != "" {
				u += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, u, http.StatusTemporaryRedirect)
			return
		}

		r.Host = elem
		r.URL.Host = elem
		r.URL.Path = "/" + rest
		r.Header.Set("Accept-Encoding", "identity") // Disable compression for link rewriting.
		lw := &linkRewriter{ResponseWriter: w, host: r.Host}
		h.ServeHTTP(lw, r)
		lw.Flush()
	})
}

// A linkRewriter is a ResponseWriter that rewrites links in HTML output.
// It rewrites relative links /foo to be /host/foo, and it rewrites any link
// https://h/foo or //h/foo, where h is in validHosts, to be /h/foo.
// This corrects the links to have the right form for the local development mode.
type linkRewriter struct {
	http.ResponseWriter
	host string
	buf  []byte
	ct   string // content-type
}

func (r *linkRewriter) WriteHeader(code int) {
	if l := r.Header().Get("Location"); l != "" {
		if u, err := url.Parse(l); err == nil {
			if u.Host == "" {
				u.Path = "/" + r.host + u.Path
			} else if validHosts[u.Host] {
				u.Path = "/" + u.Host + u.Path
				u.Scheme, u.Host = "", ""
			}
			r.Header().Set("Location", u.String())
		}
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *linkRewriter) Write(data []byte) (int, error) {
	if r.ct == "" {
		ct := r.Header().Get("Content-Type")
		if ct == "" {
			// Note: should use first 512 bytes, but first write is fine for our purposes.
			ct = http.DetectContentType(data)
		}
		r.ct = ct
	}
	if !strings.HasPrefix(r.ct, "text/html") {
		return r.ResponseWriter.Write(data)
	}
	r.buf = append(r.buf, data...)
	return len(data), nil
}

func (r *linkRewriter) Flush() {
	var repl []string
	for host := range validHosts {
		repl = append(repl, `href="https://`+host, `href="/`+host)
		repl = append(repl, `href="//`+host, `href="/`+host) // Handle scheme-less URLs.
	}
	repl = append(repl, `href="/`, `href="/`+r.host+`/`)
	strings.NewReplacer(repl...).WriteString(r.ResponseWriter, string(r.buf))
	r.buf = nil
}

func main() {
	https.RegisterFlags(flag.CommandLine)
	flag.Parse()

	pool.SetProcessMetadata(processID, processStartTime)

	if Version == "" && *mode == "dev" {
		Version = "dev"
	}
	log.Printf("coordinator version %q starting", Version)

	sc := mustCreateSecretClientOnGCE()
	if sc != nil {
		defer sc.Close()
	}

	mustInitMasterKeyCache(sc)

	// TODO(golang.org/issue/38337): remove package level variables where possible.
	// TODO(golang.org/issue/36841): remove after key functions are moved into
	// a shared package.
	pool.SetBuilderMasterKey(masterKey())
	sp := remote.NewSessionPool(context.Background())
	err := pool.InitGCE(sc, &basePinErr, sp.IsSession, *buildEnvName, *mode)
	if err != nil {
		if *mode == "" {
			*mode = "dev"
		}
		log.Printf("VM support disabled due to error initializing GCE: %v", err)
	} else {
		if *mode == "" {
			*mode = "prod"
		}
	}

	gce := pool.NewGCEConfiguration()

	if gce.BuildEnv().KubeServices.Name != "" {
		goKubeClient, err := gke.NewClient(context.Background(),
			gce.BuildEnv().KubeServices.Name,
			gce.BuildEnv().KubeServices.Location(),
			gke.OptNamespace(gce.BuildEnv().KubeServices.Namespace),
			gke.OptProject(gce.BuildEnv().ProjectName),
			gke.OptTokenSource(gce.GCPCredentials().TokenSource))
		if err != nil {
			log.Fatalf("connecting to GKE failed: %v", err)
		}
		go monitorGitMirror(goKubeClient)
	} else {
		log.Println("Kubernetes services disabled due to empty KubeServices.Name")
	}

	if *mode == "prod" || (*mode == "dev" && *devEnableEC2) {
		// TODO(golang.org/issues/38337) the coordinator will use a package scoped pool
		// until the coordinator is refactored to not require them.
		ec2PoolClose := mustCreateEC2BuildletPool(sc, sp.IsSession)
		defer ec2PoolClose()
	}

	if *mode == "dev" {
		// Replace linux-amd64 with a config using a -localdev reverse
		// buildlet so it is possible to run local builds by starting a
		// local reverse buildlet.
		dashboard.Builders["linux-amd64"] = &dashboard.BuildConfig{
			Name:     "linux-amd64",
			HostType: "host-linux-amd64-localdev",
		}
		dashboard.Builders["linux-amd64-localdev"] = &dashboard.BuildConfig{
			Name:     "linux-amd64",
			HostType: "host-linux-amd64-localdev",
		}
	}

	go pool.CoordinatorProcess().UpdateInstanceRecord()

	switch *mode {
	case "dev", "prod":
		log.Printf("Running in %s mode", *mode)
	default:
		log.Fatalf("Unknown mode: %q", *mode)
	}

	mux := http.NewServeMux()

	if *mode == "dev" {
		// Serve a mock TryBot Status page at /try-dev.
		initTryDev(mux)
	}

	addHealthCheckers(context.Background(), mux, sc)

	gr, err := metrics.GKEResource("coordinator-deployment")
	if err != nil && metadata.OnGCE() {
		log.Println("metrics.GKEResource:", err)
	}
	if ms, err := metrics.NewService(gr, views); err != nil {
		log.Println("failed to initialize metrics:", err)
	} else {
		mux.Handle("/metrics", ms)
		defer ms.Stop()
	}

	dialOpts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithTimeout(10 * time.Second),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{NextProtos: []string{"h2"}})),
	}
	mServer := "maintner.golang.org:443"
	cc, err := grpc.Dial(mServer, dialOpts...)
	if err != nil {
		log.Fatalf("unable to grpc.Dial(%q) = _, %s", mServer, err)
	}
	maintnerClient = apipb.NewMaintnerServiceClient(cc)

	sshCA := mustRetrieveSSHCertificateAuthority()

	var gomoteBucket string
	var opts []grpc.ServerOption
	if *buildEnvName == "" && *mode != "dev" && metadata.OnGCE() {
		projectID, err := metadata.ProjectID()
		if err != nil {
			log.Fatalf("metadata.ProjectID() = %v", err)
		}
		env := buildenv.ByProjectID(projectID)
		gomoteBucket = env.GomoteTransferBucket
		var coordinatorBackend, serviceID = "coordinator-internal-iap", ""
		if serviceID = env.IAPServiceID(coordinatorBackend); serviceID == "" {
			log.Fatalf("unable to retrieve Service ID for backend service=%q", coordinatorBackend)
		}
		opts = append(opts, grpc.UnaryInterceptor(access.RequireIAPAuthUnaryInterceptor(access.IAPSkipAudienceValidation)))
		opts = append(opts, grpc.StreamInterceptor(access.RequireIAPAuthStreamInterceptor(access.IAPSkipAudienceValidation)))
	}
	// grpcServer is a shared gRPC server. It is global, as it needs to be used in places that aren't factored otherwise.
	grpcServer := grpc.NewServer(opts...)

	var luciHTTPClient *http.Client
	switch *mode {
	case "prod":
		var err error
		luciHTTPClient, err = auth.NewAuthenticator(context.Background(), auth.SilentLogin, auth.Options{GCEAllowAsDefault: true}).Client()
		if err != nil {
			log.Fatalln("luci/auth.NewAuthenticator:", err)
		}
	case "dev":
		var err error
		luciHTTPClient, err = auth.NewAuthenticator(context.Background(), auth.SilentLogin, chromeinfra.DefaultAuthOptions()).Client()
		if err != nil {
			log.Fatalln("luci/auth.NewAuthenticator:", err)
		}
	}
	buildersCl := buildbucketpb.NewBuildersClient(&prpc.Client{
		C:    luciHTTPClient,
		Host: "cr-buildbucket.appspot.com",
	})
	buildsCl := buildbucketpb.NewBuildsClient(&prpc.Client{
		C:    luciHTTPClient,
		Host: "cr-buildbucket.appspot.com",
	})
	luciPoll := lucipoll.NewService(maintnerClient, buildersCl, buildsCl)
	dashV1 := legacydash.Handler(gce.GoDSClient(), maintnerClient, luciPoll, string(masterKey()), grpcServer)
	dashV2 := &builddash.Handler{Datastore: gce.GoDSClient(), Maintner: maintnerClient, LUCI: luciPoll}
	gs := &gRPCServer{dashboardURL: "https://build.golang.org"}
	setSessionPool(sp)
	gomoteServer := gomote.New(sp, sched, sshCA, gomoteBucket, mustStorageClient())
	protos.RegisterCoordinatorServer(grpcServer, gs)
	gomoteprotos.RegisterGomoteServiceServer(grpcServer, gomoteServer)
	mux.HandleFunc("/", grpcHandlerFunc(grpcServer, handleStatus)) // Serve a status page at farmer.golang.org.
	mux.Handle("build.golang.org/", dashV1)                        // Serve a build dashboard at build.golang.org.
	mux.Handle("build-staging.golang.org/", dashV1)
	mux.HandleFunc("/builders", handleBuilders)
	mux.HandleFunc("/temporarylogs", handleLogs)
	mux.HandleFunc("/reverse", pool.HandleReverse)
	mux.Handle("/revdial", revdial.ConnHandler())
	mux.HandleFunc("/style.css", handleStyleCSS)
	mux.HandleFunc("/try", serveTryStatus(false))
	mux.HandleFunc("/try.json", serveTryStatus(true))
	mux.HandleFunc("/status/post-submit-active.json", handlePostSubmitActiveJSON)
	mux.Handle("/dashboard", dashV2)
	mux.HandleFunc("/queues", handleQueues)
	if *mode == "dev" {
		// TODO(crawshaw): do more in dev mode
		gce.BuildletPool().SetEnabled(*devEnableGCE)
		if *devEnableGCE || *devEnableEC2 {
			go findWorkLoop()
		}
	} else {
		go gce.BuildletPool().CleanUpOldVMs()

		if gce.InStaging() {
			dashboard.Builders = stagingClusterBuilders()
		}

		go listenAndServeInternalModuleProxy()
		go findWorkLoop()
		go findTryWorkLoop()
		go reportReverseCountMetrics()
		// TODO(cmang): gccgo will need its own findWorkLoop
	}

	ctx := context.Background()
	configureSSHServer := func() (*remote.SSHServer, error) {
		privateKey, publicKey, err := retrieveSSHKeys(ctx, sc, *mode)
		if err != nil {
			return nil, fmt.Errorf("unable to retrieve keys for SSH Server: %v", err)
		}
		return remote.NewSSHServer(*sshAddr, privateKey, publicKey, sshCA, sp)
	}
	sshServ, err := configureSSHServer()
	if err != nil {
		log.Printf("unable to configure SSH server: %s", err)
	} else {
		go func() {
			log.Printf("running SSH server on %s", *sshAddr)
			err := sshServ.ListenAndServe()
			log.Printf("SSH server ended with error: %v", err)
		}()
		defer func() {
			err := sshServ.Close()
			if err != nil {
				log.Printf("unable to close SSH server: %s", err)
			}
		}()
	}
	if *mode == "dev" {
		// Use hostPathHandler in local development mode (only) to improve
		// convenience of testing multiple domains that coordinator serves.
		log.Fatalln(https.ListenAndServe(context.Background(), hostPathHandler(mux)))
	}
	log.Fatalln(https.ListenAndServe(context.Background(), mux))
}

// ignoreAllNewWork, when true, prevents addWork from doing anything.
// It's sometimes set in staging mode when people are debugging
// certain paths.
var ignoreAllNewWork bool

// addWorkTestHook is optionally set by tests.
var addWorkTestHook func(buildgo.BuilderRev, commitDetail)

type commitDetail struct {
	// RevCommitTime is always the git committer time of the associated
	// BuilderRev.Rev.
	RevCommitTime time.Time

	// SubRevCommitTime is always the git committer time of the associated
	// BuilderRev.SubRev, if it exists. Otherwise, it's the zero value.
	SubRevCommitTime time.Time

	// Branch for BuilderRev.Rev.
	RevBranch string

	// Branch for BuilderRev.SubRev, if it exists.
	SubRevBranch string

	// AuthorId is the gerrit-internal ID for the commit author, if
	// available. For sub-repo trybots, this is the author of the
	// commit from the trybot CL.
	AuthorId int64

	// AuthorEmail is the commit author from Gerrit, if available.
	// For sub-repo trybots, this is the author of the
	// commit from the trybot CL.
	AuthorEmail string
}

// addWorkDetail adds some work to (maybe) do, if it's not already
// enqueued and the builders are configured to run the given repo. The
// detail argument is optional and used for scheduling. It's currently
// only used for post-submit builds.
func addWorkDetail(work buildgo.BuilderRev, detail commitDetail) {
	if f := addWorkTestHook; f != nil {
		f(work, detail)
		return
	}
	if ignoreAllNewWork || isBuilding(work) {
		return
	}
	if !mayBuildRev(work) {
		if pool.NewGCEConfiguration().InStaging() {
			if _, ok := dashboard.Builders[work.Name]; ok && logCantBuildStaging.Allow() {
				log.Printf("may not build %v; skipping", work)
			}
		}
		return
	}
	st, err := newBuild(work, detail)
	if err != nil {
		log.Printf("Bad build work params %v: %v", work, err)
		return
	}
	st.start()
}

func stagingClusterBuilders() map[string]*dashboard.BuildConfig {
	m := map[string]*dashboard.BuildConfig{}
	for _, name := range []string{
		"linux-amd64",
		"linux-amd64-sid",
		"linux-amd64-clang",
		"js-wasm-node18",
	} {
		if c, ok := dashboard.Builders[name]; ok {
			m[name] = c
		} else {
			panic(fmt.Sprintf("unknown builder %q", name))
		}
	}

	// Also permit all the reverse buildlets:
	for name, bc := range dashboard.Builders {
		if bc.IsReverse() {
			m[name] = bc
		}
	}
	return m
}

func numCurrentBuilds() int {
	statusMu.Lock()
	defer statusMu.Unlock()
	return len(status)
}

func isBuilding(work buildgo.BuilderRev) bool {
	statusMu.Lock()
	defer statusMu.Unlock()
	_, building := status[work]
	return building
}

var (
	logUnknownBuilder   = rate.NewLimiter(rate.Every(5*time.Second), 2)
	logCantBuildStaging = rate.NewLimiter(rate.Every(1*time.Second), 2)
)

// mayBuildRev reports whether the build type & revision should be started.
// It returns true if it's not already building, and if a reverse buildlet is
// required, if an appropriate machine is registered.
func mayBuildRev(rev buildgo.BuilderRev) bool {
	if isBuilding(rev) {
		return false
	}
	if rev.SubName != "" {
		// Don't build repos we don't know about,
		// so importPathOfRepo won't panic later.
		if r, ok := repos.ByGerritProject[rev.SubName]; !ok || r.ImportPath == "" || !r.CoordinatorCanBuild {
			return false
		}
	}
	buildConf, ok := dashboard.Builders[rev.Name]
	if !ok {
		if logUnknownBuilder.Allow() {
			log.Printf("unknown builder %q", rev.Name)
		}
		return false
	}
	gceBuildEnv := pool.NewGCEConfiguration().BuildEnv()
	if gceBuildEnv.MaxBuilds > 0 && numCurrentBuilds() >= gceBuildEnv.MaxBuilds {
		return false
	}
	if buildConf.IsReverse() && !pool.ReversePool().CanBuild(buildConf.HostType) {
		return false
	}
	return true
}

func setStatus(work buildgo.BuilderRev, st *buildStatus) {
	statusMu.Lock()
	defer statusMu.Unlock()
	// TODO: panic if status[work] already exists. audit all callers.
	// For instance, what if a trybot is running, and then the CL is merged
	// and the findWork goroutine picks it up and it has the same commit,
	// because it didn't need to be rebased in Gerrit's cherrypick?
	// Could we then have two running with the same key?
	status[work] = st
}

func markDone(work buildgo.BuilderRev) {
	statusMu.Lock()
	defer statusMu.Unlock()
	st, ok := status[work]
	if !ok {
		return
	}
	delete(status, work)
	if len(statusDone) == maxStatusDone {
		copy(statusDone, statusDone[1:])
		statusDone = statusDone[:len(statusDone)-1]
	}
	statusDone = append(statusDone, st)
}

// statusPtrStr disambiguates which status to return if there are
// multiple in the history (e.g. recent failures where the build
// didn't finish for reasons outside of all.bash failing)
func getStatus(work buildgo.BuilderRev, statusPtrStr string) *buildStatus {
	statusMu.Lock()
	defer statusMu.Unlock()
	match := func(st *buildStatus) bool {
		return statusPtrStr == "" || fmt.Sprintf("%p", st) == statusPtrStr
	}
	if st, ok := status[work]; ok && match(st) {
		return st
	}
	for _, st := range statusDone {
		if st.BuilderRev == work && match(st) {
			return st
		}
	}
	for k, ts := range tries {
		if k.Commit == work.Rev {
			ts.mu.Lock()
			for _, st := range ts.builds {
				if st.BuilderRev == work && match(st) {
					ts.mu.Unlock()
					return st
				}
			}
			ts.mu.Unlock()
		}
	}
	return nil
}

// cancelOnePostSubmitBuildWithHostType tries to cancel one
// post-submit (non trybot) build with the provided host type and
// reports whether it did so.
//
// It currently selects the one that's been running the least amount
// of time, but that's not guaranteed.
func cancelOnePostSubmitBuildWithHostType(hostType string) bool {
	statusMu.Lock()
	defer statusMu.Unlock()
	var best *buildStatus
	for _, st := range status {
		if st.isTry() || st.conf.HostType != hostType {
			continue
		}
		if best == nil || st.startTime.After(best.startTime) {
			best = st
		}
	}
	if best != nil {
		go best.cancelBuild()
	}
	return best != nil
}

type byAge []*buildStatus

func (s byAge) Len() int           { return len(s) }
func (s byAge) Less(i, j int) bool { return s[i].startTime.Before(s[j].startTime) }
func (s byAge) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func serveTryStatus(json bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ts := trySetOfCommitPrefix(r.FormValue("commit"))
		var tss trySetState
		if ts != nil {
			ts.mu.Lock()
			tss = ts.trySetState.clone()
			ts.mu.Unlock()
		}
		if json {
			serveTryStatusJSON(w, r, ts, tss)
			return
		}
		serveTryStatusHTML(w, ts, tss)
	}
}

// tss is a clone that does not require ts' lock.
func serveTryStatusJSON(w http.ResponseWriter, r *http.Request, ts *trySet, tss trySetState) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == "OPTIONS" {
		// This is likely a pre-flight CORS request.
		return
	}
	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
		Payload any    `json:"payload,omitempty"`
	}
	if ts == nil {
		var buf bytes.Buffer
		resp.Error = "TryBot result not found (already done, invalid, or not yet discovered from Gerrit). Check Gerrit for results."
		if err := json.NewEncoder(&buf).Encode(resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write(buf.Bytes())
		return
	}
	type litebuild struct {
		Name      string    `json:"name"`
		StartTime time.Time `json:"startTime"`
		Done      bool      `json:"done"`
		Succeeded bool      `json:"succeeded"`
	}
	var result struct {
		ChangeID string      `json:"changeId"`
		Commit   string      `json:"commit"`
		Builds   []litebuild `json:"builds"`
	}
	result.Commit = ts.Commit
	result.ChangeID = ts.ChangeID

	for _, bs := range tss.builds {
		var lb litebuild
		bs.mu.Lock()
		lb.Name = bs.Name
		lb.StartTime = bs.startTime
		if !bs.done.IsZero() {
			lb.Done = true
			lb.Succeeded = bs.succeeded
		}
		bs.mu.Unlock()
		result.Builds = append(result.Builds, lb)
	}
	resp.Success = true
	resp.Payload = result
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(resp); err != nil {
		log.Printf("Could not encode JSON response: %v", err)
		http.Error(w, "error encoding JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(buf.Bytes())
}

// Styles unique to the trybot status page.
const tryStatusCSS = `
<style>
p {
	line-height: 1.15em;
}

table {
	font-size: 11pt;
}

.nobr {
	white-space: nowrap;
}

</style>
`

// tss is a clone that does not require ts' lock.
func serveTryStatusHTML(w http.ResponseWriter, ts *trySet, tss trySetState) {
	if ts == nil {
		http.Error(w, "TryBot result not found (already done, invalid, or not yet discovered from Gerrit). Check Gerrit for results.", http.StatusNotFound)
		return
	}
	buf := new(bytes.Buffer)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteString("<!DOCTYPE html><head><title>trybot status</title>")
	buf.WriteString(`<link rel="stylesheet" href="/style.css"/>`)
	buf.WriteString(tryStatusCSS)
	buf.WriteString("</head><body>")
	fmt.Fprintf(buf, "[<a href='/'>homepage</a>] &gt; %s\n", ts.ChangeID)
	fmt.Fprintf(buf, "<h1>Trybot Status</h1>")
	fmt.Fprintf(buf, "<p>Change-ID: <a href='https://go-review.googlesource.com/#/q/%s'>%s</a><br />\n", ts.ChangeID, ts.ChangeID)
	fmt.Fprintf(buf, "Commit: <a href='https://go-review.googlesource.com/#/q/%s'>%s</a></p>\n", ts.Commit, ts.Commit)
	fmt.Fprintf(buf, "<p>Builds remaining: %d</p>\n", tss.remain)
	fmt.Fprintf(buf, "<h4>Builds</h4>\n")
	fmt.Fprintf(buf, "<table cellpadding=5 border=0>\n")
	for _, bs := range tss.builds {
		var status string
		bs.mu.Lock()
		if !bs.done.IsZero() {
			if bs.succeeded {
				status = "pass"
			} else {
				status = "<b>FAIL</b>"
			}
		} else {
			status = fmt.Sprintf("<i>running</i> %s", time.Since(bs.startTime).Round(time.Second))
		}
		if u := bs.logURL; u != "" {
			status = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(u), status)
		}
		bs.mu.Unlock()
		fmt.Fprintf(buf, "<tr><td class=\"nobr\">&#8226; %s</td><td>%s</td></tr>\n",
			html.EscapeString(bs.NameAndBranch()), status)
	}
	fmt.Fprintf(buf, "</table>\n")
	fmt.Fprintf(buf, "<h4>Full Detail</h4><table cellpadding=5 border=1>\n")
	for _, bs := range tss.builds {
		status := "<i>(running)</i>"
		bs.mu.Lock()
		if !bs.done.IsZero() {
			if bs.succeeded {
				status = "pass"
			} else {
				status = "<b>FAIL</b>"
			}
		}
		bs.mu.Unlock()
		fmt.Fprintf(buf, "<tr valign=top><td align=left>%s</td><td align=center>%s</td><td><pre>%s</pre></td></tr>\n",
			html.EscapeString(bs.NameAndBranch()),
			status,
			bs.HTMLStatusTruncated())
	}
	fmt.Fprintf(buf, "</table>")
	w.Write(buf.Bytes())
}

func trySetOfCommitPrefix(commitPrefix string) *trySet {
	if commitPrefix == "" {
		return nil
	}
	statusMu.Lock()
	defer statusMu.Unlock()
	for k, ts := range tries {
		if strings.HasPrefix(k.Commit, commitPrefix) {
			return ts
		}
	}
	return nil
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	br := buildgo.BuilderRev{
		Name:    r.FormValue("name"),
		Rev:     r.FormValue("rev"),
		SubName: r.FormValue("subName"), // may be empty
		SubRev:  r.FormValue("subRev"),  // may be empty
	}
	st := getStatus(br, r.FormValue("st"))
	if st == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeStatusHeader(w, st)

	nostream := r.FormValue("nostream") != ""
	if nostream || !st.isRunning() {
		if nostream {
			fmt.Fprintf(w, "\n\n(live streaming disabled; reload manually to see status)\n")
		}
		w.Write(st.output.Bytes())
		return
	}

	if !st.hasEvent("make_and_test") && !st.hasEvent("make_cross_compile_kube") {
		fmt.Fprintf(w, "\n\n(buildlet still starting; no live streaming. reload manually to see status)\n")
		return
	}

	w.(http.Flusher).Flush()

	output := st.output.Reader()
	go func() {
		<-r.Context().Done()
		output.Close()
	}()
	buf := make([]byte, 65536)
	for {
		n, err := output.Read(buf)
		if _, err2 := w.Write(buf[:n]); err2 != nil {
			return
		}
		w.(http.Flusher).Flush()
		if err != nil {
			break
		}
	}
}

func writeStatusHeader(w http.ResponseWriter, st *buildStatus) {
	st.mu.Lock()
	defer st.mu.Unlock()
	fmt.Fprintf(w, "  builder: %s\n", st.Name)
	fmt.Fprintf(w, "      rev: %s\n", st.Rev)
	workaroundFlush(w)
	fmt.Fprintf(w, " buildlet: %s\n", st.bc)
	fmt.Fprintf(w, "  started: %v\n", st.startTime)
	done := !st.done.IsZero()
	if done {
		fmt.Fprintf(w, "    ended: %v\n", st.done)
		fmt.Fprintf(w, "  success: %v\n", st.succeeded)
	} else {
		fmt.Fprintf(w, "   status: still running\n")
	}
	if len(st.events) > 0 {
		io.WriteString(w, "\nEvents:\n")
		st.writeEventsLocked(w, false, 0)
	}
	io.WriteString(w, "\nBuild log:\n")
	workaroundFlush(w)
}

// workaroundFlush is an unnecessary flush to work around a bug in Chrome.
// See https://code.google.com/p/chromium/issues/detail?id=2016 for the details.
// In summary: a couple unnecessary chunk flushes bypass the content type
// sniffing which happen (even if unused?), even if you set nosniff as we do
// in func handleLogs.
func workaroundFlush(w http.ResponseWriter) {
	w.(http.Flusher).Flush()
}

// findWorkLoop polls https://build.golang.org/?mode=json looking for
// new post-submit work for the main dashboard. It does not support
// gccgo. This is separate from trybots, which populates its work from
// findTryWorkLoop.
func findWorkLoop() {
	// TODO: remove this hard-coded 15 second ticker and instead
	// do some new streaming gRPC call to maintnerd to subscribe
	// to new commits.
	ticker := time.NewTicker(15 * time.Second)
	// We wait for the ticker first, before looking for work, to
	// give findTryWork a head start. Because try work is more
	// important and the scheduler can't (yet?) preempt an
	// existing post-submit build to take it over for a trybot, we
	// want to make sure that reverse buildlets get assigned to
	// trybots/slowbots first on start-up.
	for range ticker.C {
		if err := findWork(); err != nil {
			log.Printf("failed to find new work: %v", err)
		}
	}
}

// findWork polls the https://build.golang.org/ dashboard once to find
// post-submit work to do. It's called in a loop by findWorkLoop.
func findWork() error {
	var bs types.BuildStatus
	if err := dash("GET", "", url.Values{
		"mode":   {"json"},
		"branch": {"mixed"},
	}, nil, &bs); err != nil {
		return err
	}
	knownToDashboard := map[string]bool{} // keys are builder
	for _, b := range bs.Builders {
		knownToDashboard[b] = true
	}

	var goRevisions []string           // revisions of repo "go", branch "master"
	var goRevisionsTypeParams []string // revisions of repo "go", branch "dev.typeparams" golang.org/issue/46786 and golang.org/issue/46864
	seenSubrepo := make(map[string]bool)
	commitTime := make(map[string]string)   // git rev => "2019-11-20T22:54:54Z" (time.RFC3339 from build.golang.org's JSON)
	commitBranch := make(map[string]string) // git rev => "master"

	add := func(br buildgo.BuilderRev) {
		var d commitDetail
		var err error
		if revCommitTime := commitTime[br.Rev]; revCommitTime != "" {
			d.RevCommitTime, err = time.Parse(time.RFC3339, revCommitTime)
			if err != nil {
				// Log the error, but ignore it. We can tolerate the lack of a commit time.
				log.Printf("failure parsing commit time %q for %q: %v", revCommitTime, br.Rev, err)
			}
		}
		d.RevBranch = commitBranch[br.Rev]
		if br.SubRev != "" {
			if subRevCommitTime := commitTime[br.SubRev]; subRevCommitTime != "" {
				d.SubRevCommitTime, err = time.Parse(time.RFC3339, subRevCommitTime)
				if err != nil {
					// Log the error, but ignore it. We can tolerate the lack of a commit time.
					log.Printf("failure parsing commit time %q for %q: %v", subRevCommitTime, br.SubRev, err)
				}
			}
			d.SubRevBranch = commitBranch[br.SubRev]
		}
		addWorkDetail(br, d)
	}

	for _, br := range bs.Revisions {
		if r, ok := repos.ByGerritProject[br.Repo]; !ok || !r.CoordinatorCanBuild {
			continue
		}
		if br.Repo == "grpc-review" {
			// Skip the grpc repo. It's only for reviews
			// for now (using LetsUseGerrit).
			continue
		}
		commitTime[br.Revision] = br.Date
		commitBranch[br.Revision] = br.Branch
		awaitSnapshot := false
		if br.Repo == "go" {
			if br.Branch == "master" {
				goRevisions = append(goRevisions, br.Revision)
			} else if br.Branch == "dev.typeparams" {
				goRevisionsTypeParams = append(goRevisionsTypeParams, br.Revision)
			}
		} else {
			// If this is the first time we've seen this sub-repo
			// in this loop, then br.GoRevision is the go repo
			// HEAD.  To save resources, we only build subrepos
			// against HEAD once we have a snapshot.
			// The next time we see this sub-repo in this loop, the
			// GoRevision is one of the release branches, for which
			// we may not have a snapshot (if the release was made
			// a long time before this builder came up), so skip
			// the snapshot check.
			awaitSnapshot = !seenSubrepo[br.Repo]
			seenSubrepo[br.Repo] = true
		}

		if len(br.Results) != len(bs.Builders) {
			return errors.New("bogus JSON response from dashboard: results is too long.")
		}
		for i, res := range br.Results {
			if res != "" {
				// It's either "ok" or a failure URL.
				continue
			}
			builder := bs.Builders[i]
			builderInfo, ok := dashboard.Builders[builder]
			if !ok {
				// Not managed by the coordinator.
				continue
			}
			if !builderInfo.BuildsRepoPostSubmit(br.Repo, br.Branch, br.GoBranch) {
				continue
			}
			var rev buildgo.BuilderRev
			if br.Repo == "go" {
				rev = buildgo.BuilderRev{
					Name: builder,
					Rev:  br.Revision,
				}
			} else {
				rev = buildgo.BuilderRev{
					Name:    builder,
					Rev:     br.GoRevision,
					SubName: br.Repo,
					SubRev:  br.Revision,
				}
				if awaitSnapshot &&
					// If this is a builder that snapshots after
					// make.bash but the snapshot doesn't yet exist,
					// then skip. But some builders on slow networks
					// don't snapshot, so don't wait for them. They'll
					// need to run make.bash first for x/ repos tests.
					!builderInfo.SkipSnapshot && !rev.SnapshotExists(context.TODO(), pool.NewGCEConfiguration().BuildEnv()) {
					continue
				}
			}
			add(rev)
		}
	}

	// And to bootstrap new builders, see if we have any builders
	// that the dashboard doesn't know about.
	for b, builderInfo := range dashboard.Builders {
		if knownToDashboard[b] {
			// no need to bootstrap.
			continue
		}
		if builderInfo.BuildsRepoPostSubmit("go", "master", "master") {
			for _, rev := range goRevisions {
				add(buildgo.BuilderRev{Name: b, Rev: rev})
			}
		} else if builderInfo.BuildsRepoPostSubmit("go", "dev.typeparams", "dev.typeparams") {
			// schedule builds on dev.typeparams branch
			// golang.org/issue/46786 and golang.org/issue/46864
			for _, rev := range goRevisionsTypeParams {
				add(buildgo.BuilderRev{Name: b, Rev: rev})
			}
		}
	}
	return nil
}

// findTryWorkLoop is a goroutine which loops periodically and queries
// Gerrit for TryBot work.
func findTryWorkLoop() {
	if pool.NewGCEConfiguration().TryDepsErr() != nil {
		return
	}
	ticker := time.NewTicker(1 * time.Second)
	for {
		if err := findTryWork(); err != nil {
			log.Printf("failed to find trybot work: %v", err)
		}
		<-ticker.C
	}
}

func findTryWork() error {
	isStaging := pool.NewGCEConfiguration().InStaging()
	if isStaging && !stagingTryWork {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // should be milliseconds
	defer cancel()
	tryRes, err := maintnerClient.GoFindTryWork(ctx, &apipb.GoFindTryWorkRequest{ForStaging: isStaging})
	if err != nil {
		return err
	}

	now := time.Now()

	statusMu.Lock()
	defer statusMu.Unlock()

	tryList = tryList[:0]
	for _, work := range tryRes.Waiting {
		if work.ChangeId == "" || work.Commit == "" {
			log.Printf("Warning: skipping incomplete %#v", work)
			continue
		}
		if r, ok := repos.ByGerritProject[work.Project]; !ok || !r.CoordinatorCanBuild {
			continue
		}
		key := tryWorkItemKey(work)
		tryList = append(tryList, key)
		if ts, ok := tries[key]; ok {
			// already in progress
			ts.wantedAsOf = now
			continue
		} else {
			ts := newTrySet(work)
			ts.wantedAsOf = now
			tries[key] = ts
		}
	}
	for k, ts := range tries {
		if ts.wantedAsOf != now {
			delete(tries, k)
			go ts.cancelBuilds()
		}
	}
	return nil
}

type tryKey struct {
	Project  string // "go", "net", etc
	Branch   string // master
	ChangeID string // I1a27695838409259d1586a0adfa9f92bccf7ceba
	Commit   string // ecf3dffc81dc21408fb02159af352651882a8383
}

// ChangeTriple returns the Gerrit (project, branch, change-ID) triple
// uniquely identifying this change. Several Gerrit APIs require this
// form of if there are multiple changes with the same Change-ID.
func (k *tryKey) ChangeTriple() string {
	return fmt.Sprintf("%s~%s~%s", k.Project, k.Branch, k.ChangeID)
}

// trySet is a the state of a set of builds of different
// configurations, all for the same (Change-ID, Commit) pair.  The
// sets which are still wanted (not already submitted or canceled) are
// stored in the global 'tries' map.
type trySet struct {
	// immutable
	tryKey
	tryID    string                   // "T" + 9 random hex
	slowBots []*dashboard.BuildConfig // any opt-in slower builders to run in a trybot run
	xrepos   []*buildStatus           // any opt-in x/ repo builds to run in a trybot run

	// wantedAsOf is guarded by statusMu and is used by
	// findTryWork. It records the last time this tryKey was still
	// wanted.
	wantedAsOf time.Time

	// mu guards the following fields.
	// See LOCK ORDER comment above.
	mu       sync.Mutex
	canceled bool // try run is no longer wanted and its builds were canceled
	trySetState
	errMsg bytes.Buffer
}

type trySetState struct {
	remain int
	failed []string // builder names, with optional " ($branch)" suffix
	builds []*buildStatus
}

func (ts trySetState) clone() trySetState {
	return trySetState{
		remain: ts.remain,
		failed: append([]string(nil), ts.failed...),
		builds: append([]*buildStatus(nil), ts.builds...),
	}
}

func tryWorkItemKey(work *apipb.GerritTryWorkItem) tryKey {
	return tryKey{
		Project:  work.Project,
		Branch:   work.Branch,
		ChangeID: work.ChangeId,
		Commit:   work.Commit,
	}
}

var testingKnobSkipBuilds bool

// newTrySet creates a new trySet group of builders for a given
// work item, the (Project, Branch, Change-ID, Commit) tuple.
// It also starts goroutines for each build.
//
// Must hold statusMu.
func newTrySet(work *apipb.GerritTryWorkItem) *trySet {
	goBranch := work.Branch
	var subBranch string // branch of subrepository, empty for main Go repo.
	if work.Project != "go" && len(work.GoBranch) > 0 {
		// work.GoBranch is non-empty when work.Project != "go",
		// so prefer work.GoBranch[0] over work.Branch for goBranch.
		goBranch = work.GoBranch[0]
		subBranch = work.Branch
	}
	tryBots := dashboard.TryBuildersForProject(work.Project, work.Branch, goBranch)
	slowBots, invalidSlowBots := slowBotsFromComments(work)
	builders := joinBuilders(tryBots, slowBots)

	key := tryWorkItemKey(work)
	log.Printf("Starting new trybot set for %v (ignored invalid terms = %q)", key, invalidSlowBots)
	ts := &trySet{
		tryKey: key,
		tryID:  "T" + randHex(9),
		trySetState: trySetState{
			builds: make([]*buildStatus, 0, len(builders)),
		},
		slowBots: slowBots,
	}

	// Defensive check that the input is well-formed.
	// Each GoCommit should have a GoBranch and a GoVersion.
	// There should always be at least one GoVersion.
	if len(work.GoBranch) < len(work.GoCommit) {
		log.Printf("WARNING: len(GoBranch) of %d != len(GoCommit) of %d", len(work.GoBranch), len(work.GoCommit))
		work.GoCommit = work.GoCommit[:len(work.GoBranch)]
	}
	if len(work.GoVersion) < len(work.GoCommit) {
		log.Printf("WARNING: len(GoVersion) of %d != len(GoCommit) of %d", len(work.GoVersion), len(work.GoCommit))
		work.GoCommit = work.GoCommit[:len(work.GoVersion)]
	}
	if len(work.GoVersion) == 0 {
		log.Print("WARNING: len(GoVersion) is zero, want at least one")
		work.GoVersion = []*apipb.MajorMinor{{}}
	}

	addBuilderToSet := func(bs *buildStatus, brev buildgo.BuilderRev) {
		bs.trySet = ts
		status[brev] = bs

		idx := len(ts.builds)
		ts.builds = append(ts.builds, bs)
		ts.remain++
		if testingKnobSkipBuilds {
			return
		}
		go bs.start() // acquires statusMu itself, so in a goroutine
		go ts.awaitTryBuild(idx, bs, brev)
	}

	var mainBuildGoCommit string
	if key.Project != "go" && len(work.GoCommit) > 0 {
		// work.GoCommit is non-empty when work.Project != "go".
		// For the main build, use the first GoCommit, which represents Go tip (master branch).
		mainBuildGoCommit = work.GoCommit[0]
	}

	// Start the main TryBot build using the selected builders.
	// There may be additional builds, those are handled below.
	if !testingKnobSkipBuilds {
		go ts.notifyStarting(invalidSlowBots)
	}
	for _, bconf := range builders {
		goVersion := types.MajorMinor{Major: int(work.GoVersion[0].Major), Minor: int(work.GoVersion[0].Minor)}
		if goVersion.Less(bconf.MinimumGoVersion) {
			continue
		}
		brev := tryKeyToBuilderRev(bconf.Name, key, mainBuildGoCommit)
		bs, err := newBuild(brev, commitDetail{RevBranch: goBranch, SubRevBranch: subBranch, AuthorEmail: work.AuthorEmail})
		if err != nil {
			log.Printf("can't create build for %q: %v", brev, err)
			continue
		}
		addBuilderToSet(bs, brev)
	}

	// If this is a golang.org/x repo and there's more than one GoCommit,
	// that means we're testing against prior releases of Go too.
	// The version selection logic is currently in maintapi's GoFindTryWork implementation.
	if key.Project != "go" && len(work.GoCommit) >= 2 {
		// linuxBuilder is the standard builder for this purpose.
		linuxBuilder := dashboard.Builders["linux-amd64"]

		for i, goRev := range work.GoCommit {
			if i == 0 {
				// Skip the i==0 element, which was already handled above.
				continue
			}
			branch := work.GoBranch[i]
			if !linuxBuilder.BuildsRepoTryBot(key.Project, "master", branch) {
				continue
			}
			goVersion := types.MajorMinor{Major: int(work.GoVersion[i].Major), Minor: int(work.GoVersion[i].Minor)}
			if goVersion.Less(linuxBuilder.MinimumGoVersion) {
				continue
			}
			brev := tryKeyToBuilderRev(linuxBuilder.Name, key, goRev)
			bs, err := newBuild(brev, commitDetail{RevBranch: branch, SubRevBranch: subBranch, AuthorEmail: work.AuthorEmail})
			if err != nil {
				log.Printf("can't create build for %q: %v", brev, err)
				continue
			}
			addBuilderToSet(bs, brev)
		}
	}

	// For the Go project on the "master" branch,
	// use the TRY= syntax to test against x repos.
	if branch := key.Branch; key.Project == "go" && branch == "master" {
		// customBuilder optionally specifies the builder to use for the build
		// (empty string means to use the default builder).
		addXrepo := func(project, customBuilder string) *buildStatus {
			// linux-amd64 is the default builder as it is the fastest and least
			// expensive.
			builder := dashboard.Builders["linux-amd64"]
			if customBuilder != "" {
				b, ok := dashboard.Builders[customBuilder]
				if !ok {
					log.Printf("can't resolve requested builder %q", customBuilder)
					return nil
				}
				builder = b
			}

			if testingKnobSkipBuilds {
				return nil
			}
			if !builder.BuildsRepoPostSubmit(project, branch, branch) {
				log.Printf("builder %q isn't configured to build %q@%q", builder.Name, project, branch)
				return nil
			}
			rev, err := getRepoHead(project)
			if err != nil {
				log.Printf("can't determine repo head for %q: %v", project, err)
				return nil
			}
			brev := buildgo.BuilderRev{
				Name:    builder.Name,
				Rev:     work.Commit,
				SubName: project,
				SubRev:  rev,
			}
			// getRepoHead always fetches master, so use that as the SubRevBranch.
			bs, err := newBuild(brev, commitDetail{RevBranch: branch, SubRevBranch: "master", AuthorEmail: work.AuthorEmail})
			if err != nil {
				log.Printf("can't create x/%s trybot build for go/master commit %s: %v", project, rev, err)
				return nil
			}
			addBuilderToSet(bs, brev)
			return bs
		}

		// First, add the opt-in x repos.
		repoBuilders := xReposFromComments(work)
		for rb := range repoBuilders {
			if bs := addXrepo(rb.Project, rb.Builder); bs != nil {
				ts.xrepos = append(ts.xrepos, bs)
			}
		}

		// Always include the default x/tools builder. See golang.org/issue/34348.
		// Do not add it to the trySet's list of opt-in x repos, however.
		if haveDefaultToolsBuild := repoBuilders[xRepoAndBuilder{Project: "tools"}]; !haveDefaultToolsBuild {
			addXrepo("tools", "")
		}
	}

	return ts
}

// Note: called in some paths where statusMu is held; do not make RPCs.
func tryKeyToBuilderRev(builder string, key tryKey, goRev string) buildgo.BuilderRev {
	// This function is called from within newTrySet, holding statusMu, s
	if key.Project == "go" {
		return buildgo.BuilderRev{
			Name: builder,
			Rev:  key.Commit,
		}
	}
	return buildgo.BuilderRev{
		Name:    builder,
		Rev:     goRev,
		SubName: key.Project,
		SubRev:  key.Commit,
	}
}

// joinBuilders joins sets of builders into one set.
// The resulting set contains unique builders sorted by name.
func joinBuilders(sets ...[]*dashboard.BuildConfig) []*dashboard.BuildConfig {
	byName := make(map[string]*dashboard.BuildConfig)
	for _, set := range sets {
		for _, bc := range set {
			byName[bc.Name] = bc
		}
	}
	var all []*dashboard.BuildConfig
	for _, bc := range byName {
		all = append(all, bc)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all
}

// state returns a copy of the trySet's state.
func (ts *trySet) state() trySetState {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.trySetState.clone()
}

// tryBotsTag returns a Gerrit tag for the TryBots state s. See Issue 39828 and
// https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#review-input.
func tryBotsTag(s string) string {
	return "autogenerated:trybots~" + s
}

func isTryBotsTag(s string) bool {
	return strings.HasPrefix(s, "autogenerated:trybots~")
}

// A commentThread is a thread of Gerrit comments.
type commentThread struct {
	// root is the first comment in the thread.
	root gerrit.CommentInfo
	// thread is a list of all the comments in the thread, including the root,
	// sorted chronologically.
	thread []gerrit.CommentInfo
	// unresolved is the thread unresolved state, based on the last comment.
	unresolved bool
}

// listPatchSetThreads returns a list of PATCHSET_LEVEL comment threads, sorted
// by the time at which they were started.
func listPatchSetThreads(gerritClient *gerrit.Client, changeID string) ([]*commentThread, error) {
	comments, err := gerritClient.ListChangeComments(context.Background(), changeID)
	if err != nil {
		return nil, err
	}
	patchSetComments := comments["/PATCHSET_LEVEL"]
	if len(patchSetComments) == 0 {
		return nil, nil
	}

	// The API doesn't sort comments chronologically, but "the state of
	// resolution of a comment thread is stored in the last comment in that
	// thread chronologically", so first of all sort them by time.
	sort.Slice(patchSetComments, func(i, j int) bool {
		return patchSetComments[i].Updated.Time().Before(patchSetComments[j].Updated.Time())
	})

	// roots is a map of message IDs to their thread root.
	roots := make(map[string]string)
	threads := make(map[string]*commentThread)
	var result []*commentThread
	for _, c := range patchSetComments {
		if c.InReplyTo == "" {
			roots[c.ID] = c.ID
			threads[c.ID] = &commentThread{
				root:       c,
				thread:     []gerrit.CommentInfo{c},
				unresolved: *c.Unresolved,
			}
			if c.Unresolved != nil {
				threads[c.ID].unresolved = *c.Unresolved
			}
			result = append(result, threads[c.ID])
			continue
		}

		root, ok := roots[c.InReplyTo]
		if !ok {
			return nil, fmt.Errorf("%s has no parent", c.ID)
		}
		roots[c.ID] = root
		threads[root].thread = append(threads[root].thread, c)
		if c.Unresolved != nil {
			threads[root].unresolved = *c.Unresolved
		}
	}

	return result, nil
}

func (ts *trySet) statusPage() string {
	return "https://farmer.golang.org/try?commit=" + ts.Commit[:8]
}

// notifyStarting runs in its own goroutine and posts to Gerrit that
// the trybots have started on the user's CL with a link of where to watch.
func (ts *trySet) notifyStarting(invalidSlowBots []string) {
	name := "TryBots"
	if len(ts.slowBots) > 0 {
		name = "SlowBots"
	}
	msg := name + " beginning. Status page: " + ts.statusPage() + "\n"

	if len(invalidSlowBots) > 0 {
		msg += fmt.Sprintf("Note that the following SlowBot terms didn't match any existing builder name or slowbot alias: %s.\n", strings.Join(invalidSlowBots, ", "))
	}

	// If any of the requested SlowBot builders
	// have a known issue, give users a warning.
	for _, b := range ts.slowBots {
		if len(b.KnownIssues) > 0 {
			issueBlock := new(strings.Builder)
			fmt.Fprintf(issueBlock, "Note that builder %s has known issues:\n", b.Name)
			for _, i := range b.KnownIssues {
				fmt.Fprintf(issueBlock, "\thttps://go.dev/issue/%d\n", i)
			}
			msg += issueBlock.String()
		}
	}

	unresolved := true
	ri := gerrit.ReviewInput{
		Tag: tryBotsTag("beginning"),
		Comments: map[string][]gerrit.CommentInput{
			"/PATCHSET_LEVEL": {{Message: msg, Unresolved: &unresolved}},
		},
	}

	// Mark as resolved old TryBot threads that don't have human comments on them.
	gerritClient := pool.NewGCEConfiguration().GerritClient()
	if patchSetThreads, err := listPatchSetThreads(gerritClient, ts.ChangeTriple()); err == nil {
		for _, t := range patchSetThreads {
			if !t.unresolved {
				continue
			}
			hasHumanComments := false
			for _, c := range t.thread {
				if !isTryBotsTag(c.Tag) {
					hasHumanComments = true
					break
				}
			}
			if hasHumanComments {
				continue
			}
			unresolved := false
			ri.Comments["/PATCHSET_LEVEL"] = append(ri.Comments["/PATCHSET_LEVEL"], gerrit.CommentInput{
				InReplyTo:  t.root.ID,
				Message:    "Superseded.",
				Unresolved: &unresolved,
			})
		}
	} else {
		log.Printf("Error getting Gerrit threads on %s: %v", ts.ChangeTriple(), err)
	}

	if err := gerritClient.SetReview(context.Background(), ts.ChangeTriple(), ts.Commit, ri); err != nil {
		log.Printf("Error leaving Gerrit comment on %s: %v", ts.Commit[:8], err)
	}
}

// awaitTryBuild runs in its own goroutine and waits for a build in a
// trySet to complete.
//
// If the build fails without getting to the end, it sleeps and
// reschedules it, as long as it's still wanted.
func (ts *trySet) awaitTryBuild(idx int, bs *buildStatus, brev buildgo.BuilderRev) {
	for {
	WaitCh:
		for {
			timeout := time.NewTimer(10 * time.Minute)
			select {
			case <-bs.ctx.Done():
				timeout.Stop()
				break WaitCh
			case <-timeout.C:
				if !ts.wanted() {
					// Build was canceled.
					return
				}
			}
		}

		if bs.hasEvent(eventDone) || bs.hasEvent(eventSkipBuildMissingDep) {
			ts.noteBuildComplete(bs)
			return
		}

		// TODO(bradfitz): rethink this logic. we should only
		// start a new build if the old one appears dead or
		// hung.

		// Sleep a bit and retry.
		time.Sleep(30 * time.Second)
		if !ts.wanted() {
			return
		}
		bs, _ = newBuild(brev, bs.commitDetail)
		bs.trySet = ts
		go bs.start()
		ts.mu.Lock()
		ts.builds[idx] = bs
		ts.mu.Unlock()
	}
}

// wanted reports whether this trySet is still active.
//
// If the commit has been submitted, or change abandoned, or the
// checkbox unchecked, wanted returns false.
func (ts *trySet) wanted() bool {
	statusMu.Lock()
	defer statusMu.Unlock()
	_, ok := tries[ts.tryKey]
	return ok
}

// cancelBuilds run in its own goroutine and cancels this trySet's
// currently-active builds because they're no longer wanted.
func (ts *trySet) cancelBuilds() {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Only cancel the builds once. And note that they're canceled so we
	// can avoid spamming Gerrit later if they come back as failed.
	if ts.canceled {
		return
	}
	ts.canceled = true

	for _, bs := range ts.builds {
		go bs.cancelBuild()
	}
}

func (ts *trySet) noteBuildComplete(bs *buildStatus) {
	bs.mu.Lock()
	var (
		succeeded = bs.succeeded
		buildLog  = bs.output.String()
	)
	bs.mu.Unlock()

	ts.mu.Lock()
	ts.remain--
	remain := ts.remain
	if !succeeded {
		ts.failed = append(ts.failed, bs.NameAndBranch())
	}
	numFail := len(ts.failed)
	canceled := ts.canceled
	ts.mu.Unlock()

	if canceled {
		// Be quiet and don't spam Gerrit.
		return
	}

	const failureFooter = "Consult https://build.golang.org/ to see whether they are new failures. Keep in mind that TryBots currently test *exactly* your git commit, without rebasing. If your commit's git parent is old, the failure might've already been fixed.\n"

	s1 := sha1.New()
	io.WriteString(s1, buildLog)
	objName := fmt.Sprintf("%s/%s_%x.log", bs.Rev[:8], bs.Name, s1.Sum(nil)[:4])
	wr, logURL := newBuildLogBlob(objName)
	if _, err := io.WriteString(wr, buildLog); err != nil {
		log.Printf("Failed to write to GCS: %v", err)
		return
	}
	if err := wr.Close(); err != nil {
		log.Printf("Failed to write to GCS: %v", err)
		return
	}

	bs.mu.Lock()
	bs.logURL = logURL
	bs.mu.Unlock()

	if !succeeded {
		ts.mu.Lock()
		fmt.Fprintf(&ts.errMsg, "Failed on %s: %s\n", bs.NameAndBranch(), logURL)
		ts.mu.Unlock()
	}

	postInProgressMessage := !succeeded && numFail == 1 && remain > 0
	postFinishedMessage := remain == 0

	if !postInProgressMessage && !postFinishedMessage {
		return
	}

	var (
		gerritMsg   = &strings.Builder{}
		gerritTag   string
		gerritScore int
	)

	if postInProgressMessage {
		fmt.Fprintf(gerritMsg, "Build is still in progress... "+
			"Status page: https://farmer.golang.org/try?commit=%s\n"+
			"Failed on %s: %s\n"+
			"Other builds still in progress; subsequent failure notices suppressed until final report.\n\n"+
			failureFooter, ts.Commit[:8], bs.NameAndBranch(), logURL)
		gerritTag = tryBotsTag("progress")
	}

	if postFinishedMessage {
		name := "TryBots"
		if len(ts.slowBots) > 0 {
			name = "SlowBots"
		}

		if numFail == 0 {
			gerritScore = 1
			fmt.Fprintf(gerritMsg, "%s are happy.\n", name)
			gerritTag = tryBotsTag("happy")
		} else {
			gerritScore = -1
			ts.mu.Lock()
			errMsg := ts.errMsg.String()
			ts.mu.Unlock()
			fmt.Fprintf(gerritMsg, "%d of %d %s failed.\n%s\n"+failureFooter,
				numFail, len(ts.builds), name, errMsg)
			gerritTag = tryBotsTag("failed")
		}
		fmt.Fprintln(gerritMsg)
		if len(ts.slowBots) > 0 {
			fmt.Fprintf(gerritMsg, "SlowBot builds that ran:\n")
			for _, c := range ts.slowBots {
				fmt.Fprintf(gerritMsg, "* %s\n", c.Name)
			}
		}
		if len(ts.xrepos) > 0 {
			fmt.Fprintf(gerritMsg, "Also tested the following repos:\n")
			for _, st := range ts.xrepos {
				fmt.Fprintf(gerritMsg, "* %s\n", st.NameAndBranch())
			}
		}
	}

	var inReplyTo string
	gerritClient := pool.NewGCEConfiguration().GerritClient()
	if patchSetThreads, err := listPatchSetThreads(gerritClient, ts.ChangeTriple()); err == nil {
		for _, t := range patchSetThreads {
			if t.root.Tag == tryBotsTag("beginning") && strings.Contains(t.root.Message, ts.statusPage()) {
				inReplyTo = t.root.ID
			}
		}
	} else {
		log.Printf("Error getting Gerrit threads on %s: %v", ts.ChangeTriple(), err)
	}

	// Mark resolved if TryBots are happy.
	unresolved := gerritScore != 1

	ri := gerrit.ReviewInput{
		Tag: gerritTag,
		Comments: map[string][]gerrit.CommentInput{
			"/PATCHSET_LEVEL": {{
				InReplyTo:  inReplyTo,
				Message:    gerritMsg.String(),
				Unresolved: &unresolved,
			}},
		},
	}
	if gerritScore != 0 {
		ri.Labels = map[string]int{
			"TryBot-Result": gerritScore,
		}
	}
	if err := gerritClient.SetReview(context.Background(), ts.ChangeTriple(), ts.Commit, ri); err != nil {
		log.Printf("Error leaving Gerrit comment on %s: %v", ts.Commit[:8], err)
	}
}

// getBuildlets creates up to n buildlets and sends them on the returned channel
// before closing the channel.
func getBuildlets(ctx context.Context, n int, schedTmpl *queue.SchedItem, lg pool.Logger) <-chan buildlet.Client {
	ch := make(chan buildlet.Client) // NOT buffered
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			sp := lg.CreateSpan("get_helper", fmt.Sprintf("helper %d/%d", i+1, n))
			schedItem := *schedTmpl // copy; GetBuildlet takes ownership
			schedItem.IsHelper = i > 0
			bc, err := sched.GetBuildlet(ctx, &schedItem)
			sp.Done(err)
			if err != nil {
				if err != context.Canceled {
					log.Printf("failed to get a %s buildlet: %v", schedItem.HostType, err)
				}
				return
			}
			lg.LogEventTime("empty_helper_ready", bc.Name())
			select {
			case ch <- bc:
			case <-ctx.Done():
				lg.LogEventTime("helper_killed_before_use", bc.Name())
				bc.Close()
				return
			}
		}(i)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	return ch
}

type testSet struct {
	st        *buildStatus
	items     []*testItem
	testStats *buildstats.TestStats

	mu           sync.Mutex
	inOrder      [][]*testItem
	biggestFirst [][]*testItem
}

// cancelAll cancels all pending tests.
func (s *testSet) cancelAll() {
	for _, ti := range s.items {
		ti.tryTake() // ignore return value
	}
}

func (s *testSet) testsToRunInOrder() (chunk []*testItem, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inOrder == nil {
		s.initInOrder()
	}
	return s.testsFromSlice(s.inOrder)
}

func (s *testSet) testsToRunBiggestFirst() (chunk []*testItem, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.biggestFirst == nil {
		s.initBiggestFirst()
	}
	return s.testsFromSlice(s.biggestFirst)
}

func (s *testSet) testsFromSlice(chunkList [][]*testItem) (chunk []*testItem, ok bool) {
	for _, candChunk := range chunkList {
		for _, ti := range candChunk {
			if ti.tryTake() {
				chunk = append(chunk, ti)
			}
		}
		if len(chunk) > 0 {
			return chunk, true
		}
	}
	return nil, false
}

func (s *testSet) initInOrder() {
	names := make([]string, len(s.items))
	namedItem := map[string]*testItem{}
	for i, ti := range s.items {
		names[i] = ti.name.Old
		namedItem[ti.name.Old] = ti
	}

	// First do the go_test:* ones. partitionGoTests
	// only returns those, which are the ones we merge together.
	stdSets := partitionGoTests(s.testStats.Duration, s.st.BuilderRev.Name, names)
	for _, set := range stdSets {
		tis := make([]*testItem, len(set))
		for i, name := range set {
			tis[i] = namedItem[name]
		}
		s.inOrder = append(s.inOrder, tis)
	}

	// Then do the misc tests, which are always by themselves.
	// (No benefit to merging them)
	for _, ti := range s.items {
		if !strings.HasPrefix(ti.name.Old, "go_test:") {
			s.inOrder = append(s.inOrder, []*testItem{ti})
		}
	}
}

func partitionGoTests(testDuration func(string, string) time.Duration, builderName string, tests []string) (sets [][]string) {
	var srcTests []string
	var cmdTests []string
	for _, name := range tests {
		if strings.HasPrefix(name, "go_test:cmd/") {
			cmdTests = append(cmdTests, name)
		} else if strings.HasPrefix(name, "go_test:") {
			srcTests = append(srcTests, name)
		}
	}
	sort.Strings(srcTests)
	sort.Strings(cmdTests)
	goTests := append(srcTests, cmdTests...)

	const sizeThres = 10 * time.Second

	var curSet []string
	var curDur time.Duration

	flush := func() {
		if len(curSet) > 0 {
			sets = append(sets, curSet)
			curSet = nil
			curDur = 0
		}
	}
	for _, testName := range goTests {
		d := testDuration(builderName, testName)
		if curDur+d > sizeThres {
			flush() // no-op if empty
		}
		curSet = append(curSet, testName)
		curDur += d
	}

	flush()
	return
}

func (s *testSet) initBiggestFirst() {
	items := append([]*testItem(nil), s.items...)
	sort.Sort(sort.Reverse(byTestDuration(items)))
	for _, item := range items {
		s.biggestFirst = append(s.biggestFirst, []*testItem{item})
	}
}

type testItem struct {
	set      *testSet
	name     distTestName
	duration time.Duration // optional approximate size

	take chan token // buffered size 1: sending takes ownership of rest of fields:

	done    chan token // closed when done; guards output & failed
	numFail int        // how many times it's failed to execute

	// groupSize is the number of tests which were run together
	// along with this one with "go dist test".
	// This is 1 for non-std/cmd tests, and usually >1 for std/cmd tests.
	groupSize   int
	shardIPPort string // buildlet's IPPort, for debugging

	// the following are only set for the first item in a group:
	output       []byte
	remoteErr    error         // real test failure (not a communications failure)
	execDuration time.Duration // actual time
}

func (ti *testItem) tryTake() bool {
	select {
	case ti.take <- token{}:
		return true
	default:
		return false
	}
}

// retry reschedules the test to run again, if a machine died before
// or during execution, so its results aren't yet known.
// The caller must own the 'take' semaphore.
func (ti *testItem) retry() {
	// release it to make it available for somebody else to try later:
	<-ti.take
}

func (ti *testItem) failf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	ti.output = []byte(msg)
	ti.remoteErr = errors.New(msg)
	close(ti.done)
}

// distTestName is the name of a dist test as discovered from 'go tool dist test -list'.
type distTestName struct {
	Old string // Old is dist test name converted to Go 1.20 format, like "go_test:sort" or "reboot".
	Raw string // Raw is unmodified name from dist, suitable as an argument back to 'go tool dist test'.
}

type byTestDuration []*testItem

func (s byTestDuration) Len() int           { return len(s) }
func (s byTestDuration) Less(i, j int) bool { return s[i].duration < s[j].duration }
func (s byTestDuration) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type eventAndTime struct {
	t    time.Time
	evt  string // "get_source", "make_and_test", "make", etc
	text string // optional detail text
}

var nl = []byte("\n")

// getRepoHead returns the commit hash of the latest master HEAD
// for the given repo ("go", "tools", "sys", etc).
func getRepoHead(repo string) (string, error) {
	// This gRPC call should only take a couple milliseconds, but set some timeout
	// to catch network problems. 5 seconds is overkill.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := maintnerClient.GetRef(ctx, &apipb.GetRefRequest{
		GerritServer:  "go.googlesource.com",
		GerritProject: repo,
		Ref:           "refs/heads/master",
	})
	if err != nil {
		return "", fmt.Errorf("looking up ref for %q: %v", repo, err)
	}
	if res.Value == "" {
		return "", fmt.Errorf("no master ref found for %q", repo)
	}
	return res.Value, nil
}

// newBuildLogBlob creates a new object to record a public build log.
// The objName should be a Google Cloud Storage object name.
// When developing on localhost, the WriteCloser may be of a different type.
func newBuildLogBlob(objName string) (obj io.WriteCloser, url_ string) {
	if *mode == "dev" {
		// TODO(bradfitz): write to disk or something, or
		// something testable. Maybe memory.
		return struct {
			io.Writer
			io.Closer
		}{
			os.Stderr,
			io.NopCloser(nil),
		}, "devmode://build-log/" + objName
	}
	if pool.NewGCEConfiguration().StorageClient() == nil {
		panic("nil storageClient in newFailureBlob")
	}
	bucket := pool.NewGCEConfiguration().BuildEnv().LogBucket

	wr := pool.NewGCEConfiguration().StorageClient().Bucket(bucket).Object(objName).NewWriter(context.Background())
	wr.ContentType = "text/plain; charset=utf-8"

	return wr, fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucket, objName)
}

func randHex(n int) string {
	buf := make([]byte, n/2+1)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("randHex: %v", err)
	}
	return fmt.Sprintf("%x", buf)[:n]
}

// importPathOfRepo returns the Go import path corresponding to the
// root of the given non-"go" repo (Gerrit project). Because it's a Go
// import path, it always has forward slashes and no trailing slash.
//
// For example:
//
//	"net"    -> "golang.org/x/net"
//	"crypto" -> "golang.org/x/crypto"
//	"dl"     -> "golang.org/dl"
func importPathOfRepo(repo string) string {
	r := repos.ByGerritProject[repo]
	if r == nil {
		// mayBuildRev prevents adding work for repos we don't know about,
		// so this shouldn't happen. If it does, a panic will be useful.
		panic(fmt.Sprintf("importPathOfRepo(%q) on unknown repo %q", repo, repo))
	}
	if r.ImportPath == "" {
		// Likewise. This shouldn't happen.
		panic(fmt.Sprintf("importPathOfRepo(%q) doesn't have an ImportPath", repo))
	}
	return r.ImportPath
}

// slowBotsFromComments looks at the Gerrit comments in work,
// and returns all build configurations that were explicitly
// requested to be tested as SlowBots via the TRY= syntax. It
// also returns any build terms that are not a valid builder
// or alias.
func slowBotsFromComments(work *apipb.GerritTryWorkItem) (builders []*dashboard.BuildConfig, invalidTryTerms []string) {
	tryTerms := latestTryTerms(work)
	invalidTryTerms = slices.Clone(tryTerms)
	for _, bc := range dashboard.Builders {
		for _, term := range tryTerms {
			if bc.MatchesSlowBotTerm(term) {
				invalidTryTerms = slices.DeleteFunc(invalidTryTerms, func(e string) bool {
					return e == term
				})
				builders = append(builders, bc)
				break
			}
		}
	}
	sort.Slice(builders, func(i, j int) bool {
		return builders[i].Name < builders[j].Name
	})
	return builders, invalidTryTerms
}

type xRepoAndBuilder struct {
	Project string // "net", "tools", etc.
	Builder string // Builder to use. Empty string means default builder.
}

func (rb xRepoAndBuilder) String() string {
	if rb.Builder == "" {
		return rb.Project
	}
	return rb.Project + "@" + rb.Builder
}

// xReposFromComments looks at the TRY= comments from Gerrit (in work) and
// returns any additional subrepos that should be tested. The TRY= comments
// are expected to be of the format TRY=x/foo or TRY=x/foo@builder where foo is
// the name of the subrepo and builder is a builder name. If no builder is
// provided, a default builder is used.
func xReposFromComments(work *apipb.GerritTryWorkItem) map[xRepoAndBuilder]bool {
	xrepos := make(map[xRepoAndBuilder]bool)
	for _, term := range latestTryTerms(work) {
		if len(term) < len("x/_") || term[:2] != "x/" {
			continue
		}
		parts := strings.SplitN(term, "@", 2)
		xrepo := parts[0][2:]
		builder := "" // By convention, this means the default builder.
		if len(parts) > 1 {
			builder = parts[1]
		}
		xrepos[xRepoAndBuilder{
			Project: xrepo,
			Builder: builder,
		}] = true
	}
	return xrepos
}

// latestTryTerms returns the terms that follow the TRY= syntax in Gerrit comments.
func latestTryTerms(work *apipb.GerritTryWorkItem) []string {
	tryMsg := latestTryMessage(work) // "aix, darwin, linux-386-387, arm64, x/tools"
	if tryMsg == "" {
		return nil
	}
	if len(tryMsg) > 1<<10 { // arbitrary sanity
		return nil
	}
	return strings.FieldsFunc(tryMsg, func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsNumber(c) && c != '-' && c != '_' && c != '/' && c != '@'
	})
}

func latestTryMessage(work *apipb.GerritTryWorkItem) string {
	// Prioritize exact version matches first
	for i := len(work.TryMessage) - 1; i >= 0; i-- {
		m := work.TryMessage[i]
		if m.Version == work.Version {
			return m.Message
		}
	}
	// Otherwise the latest message at all
	for i := len(work.TryMessage) - 1; i >= 0; i-- {
		m := work.TryMessage[i]
		if m.Message != "" {
			return m.Message
		}
	}
	return ""
}

// handlePostSubmitActiveJSON serves JSON with the info for which builds
// are currently building. The build.golang.org dashboard renders these as little
// blue gophers that link to the each build's status.
// TODO: this a transitional step on our way towards merging build.golang.org into
// this codebase; see https://github.com/golang/go/issues/34744#issuecomment-563398753.
func handlePostSubmitActiveJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(activePostSubmitBuilds())
}

func activePostSubmitBuilds() []types.ActivePostSubmitBuild {
	var ret []types.ActivePostSubmitBuild
	statusMu.Lock()
	defer statusMu.Unlock()
	for _, st := range status {
		if st.isTry() || !st.HasBuildlet() {
			continue
		}
		st.mu.Lock()
		logsURL := st.logsURLLocked()
		st.mu.Unlock()

		var commit, goCommit string
		if st.IsSubrepo() {
			commit, goCommit = st.SubRev, st.Rev
		} else {
			commit = st.Rev
		}
		ret = append(ret, types.ActivePostSubmitBuild{
			StatusURL: logsURL,
			Builder:   st.Name,
			Commit:    commit,
			GoCommit:  goCommit,
		})
	}
	return ret
}

func mustCreateSecretClientOnGCE() *secret.Client {
	if !metadata.OnGCE() {
		return nil
	}
	return secret.MustNewClient()
}

func mustCreateEC2BuildletPool(sc *secret.Client, isRemoteBuildlet func(instName string) bool) (close func()) {
	if migration.StopEC2BuildletPool {
		log.Println("not creating EC2 buildlet pool")
		return func() {}
	}

	awsKeyID, err := sc.Retrieve(context.Background(), secret.NameAWSKeyID)
	if err != nil {
		log.Fatalf("unable to retrieve secret %q: %s", secret.NameAWSKeyID, err)
	}

	awsAccessKey, err := sc.Retrieve(context.Background(), secret.NameAWSAccessKey)
	if err != nil {
		log.Fatalf("unable to retrieve secret %q: %s", secret.NameAWSAccessKey, err)
	}

	awsClient, err := cloud.NewAWSClient(buildenv.Production.AWSRegion, awsKeyID, awsAccessKey, cloud.WithRateLimiter(cloud.DefaultEC2LimitConfig))
	if err != nil {
		log.Fatalf("unable to create AWS client: %s", err)
	}

	ec2Pool, err := pool.NewEC2Buildlet(awsClient, buildenv.Production, dashboard.Hosts, isRemoteBuildlet)
	if err != nil {
		log.Fatalf("unable to create EC2 buildlet pool: %s", err)
	}
	return ec2Pool.Close
}

func mustRetrieveSSHCertificateAuthority() (privateKey []byte) {
	privateKey, _, err := remote.SSHKeyPair()
	if err != nil {
		log.Fatalf("unable to create SSH CA cert: %s", err)
	}
	return
}

func mustStorageClient() *storage.Client {
	if metadata.OnGCE() {
		return pool.NewGCEConfiguration().StorageClient()
	}
	storageClient, err := storage.NewClient(context.Background(), option.WithoutAuthentication())
	if err != nil {
		log.Fatalf("unable to create storage client: %s", err)
	}
	return storageClient
}

func fromSecret(ctx context.Context, sc *secret.Client, secretName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return sc.Retrieve(ctx, secretName)
}

func retrieveSSHKeys(ctx context.Context, sc *secret.Client, m string) (publicKey, privateKey []byte, err error) {
	if m == "dev" {
		return remote.SSHKeyPair()
	} else if metadata.OnGCE() {
		privateKeyS, err := fromSecret(ctx, sc, secret.NameGomoteSSHPrivateKey)
		if err != nil {
			return nil, nil, err
		}
		publicKeyS, err := fromSecret(ctx, sc, secret.NameGomoteSSHPublicKey)
		if err != nil {
			return nil, nil, err
		}
		return []byte(privateKeyS), []byte(publicKeyS), nil
	}
	return nil, nil, fmt.Errorf("unable to retrieve ssh keys")
}
