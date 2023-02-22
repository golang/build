// Copyright 2022 Go Authors All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/iapclient"
	"golang.org/x/build/repos"
	"golang.org/x/build/types"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"cloud.google.com/go/storage"
)

type tester struct {
	source string
	repo   string

	coordinator *buildlet.GRPCCoordinatorClient
	gcs         *storage.Client
	http        *http.Client
	gerrit      *gerrit.Client
}

type builderResult struct {
	builderType string
	logURL      string
	passed      bool
	err         error
}

type buildInfo struct {
	revision      string
	branch        string
	changeArchive []byte
	goArchive     []byte
}

func (bi *buildInfo) isSubrepo() bool {
	repo, _, _ := strings.Cut(bi.branch, ".")
	return repos.ByGerritProject[repo] != nil
}

func createBuildletWithRetry(ctx context.Context, coordinator *buildlet.GRPCCoordinatorClient, builderType string) (buildlet.RemoteClient, error) {
	const retries int = 5
	var err error
	for i := 0; i < retries; i++ {
		var c buildlet.RemoteClient
		c, err = coordinator.CreateBuildletWithStatus(ctx, builderType, func(status types.BuildletWaitStatus) {})
		if err == nil {
			return c, nil
		}
		// TODO(roland): we currently only care about retrying when we hit this
		// particular AWS error, but we may want to retry in other cases in the
		// future?
		if !strings.Contains(err.Error(), "ResourceNotReady: failed waiting for successful resource state") {
			return nil, err
		}
		log.Printf("%s: failed to create buildlet (attempt %d): %s", builderType, retries, err)
		time.Sleep(time.Second * 30)
	}
	return nil, fmt.Errorf("failed to create buildlet after %d attempts, last error: %s", retries, err)
}

// runTests creates a buildlet for the specified builderType, sends a copy of go1.4 and the change tarball to
// the buildlet, and then executes the platform specific 'all' script, streaming the output to a GCS bucket.
// The buildlet is destroyed on return.
func (t *tester) runTests(ctx context.Context, builderType string, info *buildInfo) builderResult {
	log.Printf("%s: creating buildlet", builderType)
	c, err := createBuildletWithRetry(ctx, t.coordinator, builderType)
	if err != nil {
		return builderResult{builderType: builderType, err: fmt.Errorf("failed to create buildlet: %s", err)}
	}
	buildletName := c.RemoteName()
	log.Printf("%s: created buildlet (%s)", builderType, buildletName)
	defer func() {
		if err := c.Close(); err != nil {
			log.Printf("%s: unable to close buildlet %q: %s", builderType, buildletName, err)
		} else {
			log.Printf("%s: destroyed buildlet", builderType)
		}
	}()

	buildConfig, ok := dashboard.Builders[builderType]
	if !ok {
		log.Printf("%s: unknown builder type", builderType)
		return builderResult{builderType: builderType, err: errors.New("unknown builder type")}
	}
	bootstrapURL := buildConfig.GoBootstrapURL(buildenv.Production)
	// Assume if bootstrapURL == "" the buildlet is already bootstrapped
	if bootstrapURL != "" {
		if err := c.PutTarFromURL(ctx, bootstrapURL, "go1.4"); err != nil {
			log.Printf("%s: failed to bootstrap buildlet: %s", builderType, err)
			return builderResult{builderType: builderType, err: fmt.Errorf("failed to bootstrap buildlet: %s", err)}
		}
	}

	suffix := make([]byte, 4)
	rand.Read(suffix)

	var output io.Writer
	var logURL string

	if t.gcs != nil {
		gcsBucket, gcsObject := *gcsBucket, fmt.Sprintf("%s-%x/%s", info.revision, suffix, builderType)
		gcsWriter, err := newLiveWriter(ctx, t.gcs.Bucket(gcsBucket).Object(gcsObject))
		if err != nil {
			log.Printf("%s: failed to create log writer: %s", builderType, err)
			return builderResult{builderType: builderType, err: fmt.Errorf("failed to create log writer: %s", err)}
		}
		defer func() {
			if err := gcsWriter.Close(); err != nil {
				log.Printf("%s: failed to flush GCS writer: %s", builderType, err)
			}
		}()
		logURL = "https://storage.cloud.google.com/" + path.Join(gcsBucket, gcsObject)
		output = gcsWriter
	} else {
		output = &localWriter{buildletName}
	}

	work, err := c.WorkDir(ctx)
	if err != nil {
		log.Printf("%s: failed to retrieve work dir: %s", builderType, err)
		return builderResult{builderType: builderType, err: fmt.Errorf("failed to get work dir: %s", err)}
	}

	env := append(buildConfig.Env(), "GOPATH="+work+"/gopath", "GOROOT_FINAL="+buildConfig.GorootFinal(), "GOROOT="+work+"/go")
	// Because we are unable to determine the internal GCE hostname of the
	// coordinator, we cannot use the same GOPROXY proxy that the public TryBots
	// use to get around the disabled network. Instead of using that proxy
	// proxy, we instead wait to disable the network until right before we
	// actually execute the tests, and manually download module dependencies
	// using "go mod download" if we are testing a subrepo branch.
	var disableNetwork bool
	for i, v := range env {
		if v == "GO_DISABLE_OUTBOUND_NETWORK=1" {
			env = append(env[:i], env[i+1:]...)
			disableNetwork = true
			break
		}
	}
	dirName := "go"

	if info.isSubrepo() {
		dirName = info.branch

		// fetch and build go at master first
		if err := c.PutTar(ctx, bytes.NewReader(info.goArchive), "go"); err != nil {
			log.Printf("%s: failed to upload change archive: %s", builderType, err)
			return builderResult{builderType: builderType, err: fmt.Errorf("failed to upload change archive: %s", err)}
		}
		if err := c.Put(ctx, strings.NewReader("devel "+info.revision), "go/VERSION", 0644); err != nil {
			log.Printf("%s: failed to upload VERSION file: %s", builderType, err)
			return builderResult{builderType: builderType, err: fmt.Errorf("failed to upload VERSION file: %s", err)}
		}

		cmd, args := "go/"+buildConfig.MakeScript(), buildConfig.MakeScriptArgs()
		remoteErr, execErr := c.Exec(ctx, cmd, buildlet.ExecOpts{
			Output:   output,
			ExtraEnv: append(env, "GO_DISABLE_OUTBOUND_NETWORK=0"),
			Args:     args,
			OnStartExec: func() {
				log.Printf("%s: starting make.bash %s", builderType, logURL)
			},
		})
		if execErr != nil {
			log.Printf("%s: failed to execute make.bash: %s", builderType, execErr)
			return builderResult{builderType: builderType, err: fmt.Errorf("failed to execute make.bash: %s", err)}
		}
		if remoteErr != nil {
			log.Printf("%s: make.bash failed: %s", builderType, remoteErr)
			return builderResult{builderType: builderType, err: fmt.Errorf("make.bash failed: %s", remoteErr)}
		}
	}

	if err := c.PutTar(ctx, bytes.NewReader(info.changeArchive), dirName); err != nil {
		log.Printf("%s: failed to upload change archive: %s", builderType, err)
		return builderResult{builderType: builderType, err: fmt.Errorf("failed to upload change archive: %s", err)}
	}

	if !info.isSubrepo() {
		if err := c.Put(ctx, strings.NewReader("devel "+info.revision), "go/VERSION", 0644); err != nil {
			log.Printf("%s: failed to upload VERSION file: %s", builderType, err)
			return builderResult{builderType: builderType, err: fmt.Errorf("failed to upload VERSION file: %s", err)}
		}
	}

	var cmd string
	var args []string
	if info.isSubrepo() {
		cmd, args = "go/bin/go", []string{"test", "./..."}
	} else {
		cmd, args = "go/"+buildConfig.AllScript(), buildConfig.AllScriptArgs()
	}
	opts := buildlet.ExecOpts{
		Output:   output,
		ExtraEnv: env,
		Args:     args,
		OnStartExec: func() {
			log.Printf("%s: starting tests %s", builderType, logURL)
		},
	}
	if info.isSubrepo() {
		opts.Dir = dirName

		remoteErr, execErr := c.Exec(ctx, "go/bin/go", buildlet.ExecOpts{
			Args:     []string{"mod", "download"},
			ExtraEnv: append(env, "GO_DISABLE_OUTBOUND_NETWORK=0"),
			Dir:      dirName,
			Output:   output,
			OnStartExec: func() {
				log.Printf("%s: downloading modules %s", builderType, logURL)
			},
		})
		if execErr != nil {
			log.Printf("%s: failed to execute go mod download: %s", builderType, execErr)
			return builderResult{builderType: builderType, err: fmt.Errorf("failed to execute go mod download: %s", err)}
		}
		if remoteErr != nil {
			log.Printf("%s: go mod download failed: %s", builderType, remoteErr)
			return builderResult{builderType: builderType, err: fmt.Errorf("go mod download failed: %s", remoteErr)}
		}
	}
	if disableNetwork {
		opts.ExtraEnv = append(opts.ExtraEnv, "GO_DISABLE_OUTBOUND_NETWORK=1")
	}
	remoteErr, execErr := c.Exec(ctx, cmd, opts)
	if execErr != nil {
		log.Printf("%s: failed to execute tests: %s", builderType, execErr)
		return builderResult{builderType: builderType, err: fmt.Errorf("failed to execute all.bash: %s", err)}
	}
	if remoteErr != nil {
		log.Printf("%s: tests failed: %s", builderType, remoteErr)
		return builderResult{builderType: builderType, logURL: logURL, passed: false}
	}
	log.Printf("%s: tests succeeded", builderType)
	return builderResult{builderType: builderType, logURL: logURL, passed: true}
}

// gcsLiveWriter is an extremely hacky way of getting live(ish) updating logs while
// using GCS. The buffer is written out to an object every 5 seconds.
type gcsLiveWriter struct {
	obj  *storage.ObjectHandle
	buf  *bytes.Buffer
	mu   *sync.Mutex
	stop chan bool
	err  chan error
}

func newLiveWriter(ctx context.Context, obj *storage.ObjectHandle) (*gcsLiveWriter, error) {
	stopCh, errCh := make(chan bool, 1), make(chan error, 1)
	mu := new(sync.Mutex)
	buf := new(bytes.Buffer)
	write := func(b []byte) error {
		w := obj.NewWriter(ctx)
		w.Write(b)
		if err := w.Close(); err != nil {
			return err
		}
		return nil
	}
	if err := write([]byte{}); err != nil {
		return nil, err
	}
	go func() {
		t := time.NewTicker(time.Second * 5)
		for {
			select {
			case <-stopCh:
				mu.Lock()
				errCh <- write(buf.Bytes())
				mu.Unlock()
			case <-t.C:
				mu.Lock()
				if err := write(buf.Bytes()); err != nil {
					log.Printf("GCS write to %q failed! %s", path.Join(obj.BucketName(), obj.ObjectName()), err)
					errCh <- err
				}
				mu.Unlock()
			}
		}
	}()
	return &gcsLiveWriter{obj: obj, buf: buf, mu: mu, stop: stopCh, err: errCh}, nil
}

func (g *gcsLiveWriter) Write(b []byte) (int, error) {
	g.mu.Lock()
	g.buf.Write(b)
	g.mu.Unlock()
	return len(b), nil
}

func (g *gcsLiveWriter) Close() error {
	g.stop <- true
	return <-g.err
}

type localWriter struct {
	buildlet string
}

func (lw *localWriter) Write(b []byte) (int, error) {
	prefix := []byte(lw.buildlet + ": ")
	var prefixed []byte
	for _, l := range bytes.Split(b, []byte("\n")) {
		prefixed = append(prefixed, append(prefix, append(l, byte('\n'))...)...)
	}

	return os.Stdout.Write(prefixed)
}

// getTar retrieves the tarball for a specific git revision from t.source and returns
// the bytes.
func (t *tester) getTar(revision string) ([]byte, error) {
	tarURL := t.source + "/" + t.repo + "/+archive/" + revision + ".tar.gz"
	req, err := http.NewRequest("GET", tarURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch %q: %v", tarURL, resp.Status)
	}
	defer resp.Body.Close()
	archive, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Check what we got back was actually the archive, since Google's SSO page will
	// return 200.
	_, err = gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}

	return archive, nil
}

// run tests the specific revision on the builders specified.
func (t *tester) run(ctx context.Context, revision, branch string, builders []string) ([]builderResult, error) {
	changeArchive, err := t.getTar(revision)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve change archive: %s", err)
	}

	info := &buildInfo{
		revision:      revision,
		branch:        branch,
		changeArchive: changeArchive,
	}

	if branch != "master" {
		goArchive, err := t.getTar("master")
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve go master archive: %s", err)
		}
		info.goArchive = goArchive
	}

	wg := new(sync.WaitGroup)
	resultsCh := make(chan builderResult, len(builders))
	for _, bt := range builders {
		wg.Add(1)
		go func(bt string) {
			defer wg.Done()
			result := t.runTests(ctx, bt, info) // have a proper timeout
			resultsCh <- result
		}(bt)
	}
	wg.Wait()
	close(resultsCh)
	results := make([]builderResult, 0, len(builders))
	for result := range resultsCh {
		results = append(results, result)
	}

	return results, nil
}

// commentBeginning send the review message indicating the trybots are beginning.
func (t *tester) commentBeginning(ctx context.Context, change *gerrit.ChangeInfo) error {
	// It would be nice to do a similar thing to the coordinator, using comment
	// threads that can be resolved, but that is slightly more complex than what
	// we really need to start with.
	//
	// Similarly it would be nice to comment links to logs earlier.
	return t.gerrit.SetReview(ctx, change.ID, change.CurrentRevision, gerrit.ReviewInput{
		Message: "TryBots beginning",
	})
}

// commentResults sends the review message containing the results for the change
// and applies the TryBot-Result label.
func (t *tester) commentResults(ctx context.Context, change *gerrit.ChangeInfo, results []builderResult) error {
	state := "succeeded"
	label := 1
	buf := new(bytes.Buffer)
	w := tabwriter.NewWriter(buf, 0, 0, 1, ' ', 0)
	for _, res := range results {
		s := "pass"
		context := res.logURL
		if res.err != nil {
			s = "error"
			state = "failed"
			label = -1
			context = res.err.Error()
		} else if !res.passed {
			s = "failed"
			state = "failed"
			label = -1
		}
		fmt.Fprintf(w, "    %s\t[%s]\t%s\n", res.builderType, s, context)
	}
	w.Flush()

	comment := fmt.Sprintf("Tests %s\n\n%s", state, buf.String())
	if err := t.gerrit.SetReview(ctx, change.ID, change.CurrentRevision, gerrit.ReviewInput{
		Message: comment,
		Labels:  map[string]int{"TryBot-Result": label},
	}); err != nil {
		return err
	}

	return nil
}

// findChanges queries a gerrit instance for changes which should be tested, returning a
// slice of revisions for each change.
func (t *tester) findChanges(ctx context.Context) ([]*gerrit.ChangeInfo, error) {
	return t.gerrit.QueryChanges(
		ctx,
		fmt.Sprintf("project:%s status:open label:Run-TryBot+1 -label:TryBot-Result-1 -label:TryBot-Result+1", t.repo),
		gerrit.QueryChangesOpt{Fields: []string{"CURRENT_REVISION"}},
	)
}

var (
	username = flag.String("user", "user-security", "Coordinator username")

	gerritURL = flag.String("gerrit", "https://team-review.googlesource.com", "URL for the gerrit instance")
	sourceURL = flag.String("source", "https://team.googlesource.com", "URL for the source instance")
	repoName  = flag.String("repo", "golang/go-private", "Gerrit repository name")

	gcsBucket = flag.String("gcs", "", "GCS bucket path for logs")

	revision    = flag.String("revision", "", "Revision to test, when running in one-shot mode")
	buildersStr = flag.String("builders", "", "Comma separated list of builder types to test against by default")
)

// allowedBuilders contains the set of builders which are acceptable to use for testing
// PRIVATE track security changes. These builders should, generally, be controlled by
// Google.
var allowedBuilders = map[string]bool{
	"js-wasm": true,

	"linux-386":            true,
	"linux-386-longtest":   true,
	"linux-amd64":          true,
	"linux-amd64-longtest": true,

	"linux-amd64-bullseye": true,

	"darwin-amd64-12_0": true,
	"darwin-arm64-12":   true,

	"windows-386-2012":   true,
	"windows-amd64-2016": true,
	"windows-arm64-11":   true,
}

// firstClassBuilders is the default set of builders to test against,
// representing the first class ports as defined by the port policy.
var firstClassBuilders = []string{
	"linux-386",
	"linux-amd64-longtest-race",
	"linux-arm-aws",
	"linux-arm64",

	"darwin-amd64-12_0",
	"darwin-arm64-12",

	"windows-386-2012",
	"windows-amd64-longtest",
}

func main() {
	flag.Parse()
	ctx, cancel := context.WithCancel(context.Background())

	// When kubernetes attempts to kill a workload (i.e. during a restart or
	// rollout) it sends a SIGTERM, followed by a SIGKILL after a specified
	// timeout. In order to cleanly shutdown the service, as well as destroying
	// any created buildlets etc, cancel the global context we pass around,
	// which should cascade down.
	sigtermChan := make(chan os.Signal, 1)
	signal.Notify(sigtermChan, syscall.SIGTERM)
	go func() {
		<-sigtermChan
		// Cancelling the context should cause the program to exit, either via
		// a error leading to a log.Fatalf, or the select loop hitting ctx.Done.
		// TODO(roland): we may want to make the shutdown somewhat more graceful,
		// perhaps commenting that the current run was aborted if we are in the
		// middle of one, but for now just exiting cleanly is better than nothing.
		cancel()
	}()

	creds, err := google.FindDefaultCredentials(ctx, gerrit.OAuth2Scopes...)
	if err != nil {
		log.Fatalf("reading GCP credentials: %v", err)
	}
	gerritClient := gerrit.NewClient(*gerritURL, gerrit.OAuth2Auth(creds.TokenSource))
	httpClient := oauth2.NewClient(ctx, creds.TokenSource)

	var builders []string
	if *buildersStr != "" {
		for _, b := range strings.Split(*buildersStr, ",") {
			if !allowedBuilders[b] {
				log.Fatalf("builder type %q not allowed", b)
			}
			builders = append(builders, b)
		}

	} else {
		builders = firstClassBuilders
	}

	var gcsClient *storage.Client
	if *gcsBucket != "" {
		gcsClient, err = storage.NewClient(ctx)
		if err != nil {
			log.Fatalf("Could not connect to GCS: %v", err)
		}
	}

	cc, err := iapclient.GRPCClient(ctx, "build.golang.org:443")
	if err != nil {
		log.Fatalf("Could not connect to coordinator: %v", err)
	}
	b := buildlet.GRPCCoordinatorClient{
		Client: protos.NewGomoteServiceClient(cc),
	}

	t := &tester{
		source:      strings.TrimSuffix(*sourceURL, "/"),
		repo:        *repoName,
		coordinator: &b,
		http:        httpClient,
		gcs:         gcsClient,
		gerrit:      gerritClient,
	}

	if *revision != "" {
		if _, err := t.run(ctx, *revision, "", builders); err != nil {
			log.Fatal(err)
		}
	} else {
		ticker := time.NewTicker(time.Minute)
		for {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
			changes, err := t.findChanges(ctx)
			if err != nil {
				log.Fatalf("findChanges failed: %v", err)
			}
			log.Printf("found %d changes", len(changes))

			for _, change := range changes {
				log.Printf("testing CL %d patchset %d (%s)", change.ChangeNumber, change.Revisions[change.CurrentRevision].PatchSetNumber, change.CurrentRevision)
				if err := t.commentBeginning(ctx, change); err != nil {
					log.Fatalf("commentBeginning failed: %v", err)
				}
				results, err := t.run(ctx, change.CurrentRevision, change.Branch, builders)
				if err != nil {
					log.Fatalf("run failed: %v", err)
				}
				if err := t.commentResults(ctx, change, results); err != nil {
					log.Fatalf("commentResults failed: %v", err)
				}
			}
		}
	}
}
