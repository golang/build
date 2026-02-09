// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"log"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/errorreporting"
	"go4.org/syncutil"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/internal/buildstats"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/coordinator/pool/queue"
	"golang.org/x/build/internal/coordinator/schedule"
	"golang.org/x/build/internal/migration"
	"golang.org/x/build/internal/singleflight"
	"golang.org/x/build/internal/sourcecache"
	"golang.org/x/build/internal/spanlog"
	"golang.org/x/build/livelog"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/types"
	"golang.org/x/mod/semver"
	perfstorage "golang.org/x/perf/storage"
)

// newBuild constructs a new *buildStatus from rev and commit details.
// detail may be only partially populated, but it must have at least RevBranch set.
// If rev.SubRev is set, then detail.SubRevBranch must also be set.
func newBuild(rev buildgo.BuilderRev, detail commitDetail) (*buildStatus, error) {
	// Note: can't acquire statusMu in newBuild, as this is called
	// from findTryWork -> newTrySet, which holds statusMu.

	conf, ok := dashboard.Builders[rev.Name]
	if !ok {
		return nil, fmt.Errorf("unknown builder type %q", rev.Name)
	}
	if rev.Rev == "" {
		return nil, fmt.Errorf("required field Rev is empty; got %+v", rev)
	}
	if detail.RevBranch == "" {
		return nil, fmt.Errorf("required field RevBranch is empty; got %+v", detail)
	}
	if rev.SubRev != "" && detail.SubRevBranch == "" {
		return nil, fmt.Errorf("field SubRevBranch is empty, required because SubRev is present; got %+v", detail)
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &buildStatus{
		buildID:      "B" + randHex(9),
		BuilderRev:   rev,
		commitDetail: detail,
		conf:         conf,
		startTime:    time.Now(),
		ctx:          ctx,
		cancel:       cancel,
	}, nil
}

// buildStatus is the status of a build.
type buildStatus struct {
	// Immutable:
	buildgo.BuilderRev
	commitDetail
	buildID   string // "B" + 9 random hex
	conf      *dashboard.BuildConfig
	startTime time.Time // actually time of newBuild (~same thing)
	trySet    *trySet   // or nil

	onceInitHelpers sync.Once // guards call of onceInitHelpersFunc
	helpers         <-chan buildlet.Client
	ctx             context.Context    // used to start the build
	cancel          context.CancelFunc // used to cancel context; for use by setDone only

	hasBuildlet int32 // atomic: non-zero if this build has a buildlet; for status.go.

	mu              sync.Mutex       // guards following
	canceled        bool             // whether this build was forcefully canceled, so errors should be ignored
	schedItem       *queue.SchedItem // for the initial buildlet (ignoring helpers for now)
	logURL          string           // if non-empty, permanent URL of log
	bc              buildlet.Client  // nil initially, until pool returns one
	done            time.Time        // finished running
	succeeded       bool             // set when done
	output          livelog.Buffer   // stdout and stderr
	events          []eventAndTime
	useSnapshotMemo map[string]bool // memoized result of useSnapshotFor(rev), where the key is rev
}

func (st *buildStatus) NameAndBranch() string {
	result := st.Name
	if st.RevBranch != "master" {
		// For the common and currently-only case of
		// "release-branch.go1.15" say "linux-amd64 (Go 1.15.x)"
		const releasePrefix = "release-branch.go"
		if after, ok := strings.CutPrefix(st.RevBranch, releasePrefix); ok {
			result = fmt.Sprintf("%s (Go %s.x)", st.Name, after)
		} else {
			// But if we ever support building other branches,
			// fall back to something verbose until we add a
			// special case:
			result = fmt.Sprintf("%s (go branch %s)", st.Name, st.RevBranch)
		}
	}
	// For an x repo running on a CL in a different repo,
	// add a prefix specifying the name of the x repo.
	if st.SubName != "" && st.trySet != nil && st.SubName != st.trySet.Project {
		result = "(x/" + st.SubName + ") " + result
	}
	return result
}

// cancelBuild marks a build as no longer wanted, cancels its context,
// and tears down its buildlet.
func (st *buildStatus) cancelBuild() {
	st.mu.Lock()
	if st.canceled {
		// Already done. Shouldn't happen currently, but make
		// it safe for duplicate calls in the future.
		st.mu.Unlock()
		return
	}

	st.canceled = true
	st.output.Close()
	// cancel the context, which stops the creation of helper
	// buildlets, etc. The context isn't plumbed everywhere yet,
	// so we also forcefully close its buildlet out from under it
	// to trigger a failure. When we get the failure later, we
	// just ignore it (knowing that the canceled bit was set
	// true).
	st.cancel()
	bc := st.bc
	st.mu.Unlock()

	if bc != nil {
		// closing the buildlet may be slow (up to ~10 seconds
		// on a wedged buildlet) so run it in its own
		// goroutine, so we're not holding st.mu for too long.
		bc.Close()
	}
}

func (st *buildStatus) setDone(succeeded bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.canceled {
		return
	}
	st.succeeded = succeeded
	st.done = time.Now()
	st.output.Close()
	st.cancel()
}

func (st *buildStatus) isRunning() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.isRunningLocked()
}

func (st *buildStatus) isRunningLocked() bool { return st.done.IsZero() }

func (st *buildStatus) logf(format string, args ...any) {
	log.Printf("[build %s %s]: %s", st.Name, st.Rev, fmt.Sprintf(format, args...))
}

// start starts the build in a new goroutine.
// The buildStatus's context is closed when the build is complete,
// successfully or not.
func (st *buildStatus) start() {
	setStatus(st.BuilderRev, st)
	go func() {
		err := st.build()
		if err == errSkipBuildDueToDeps {
			st.setDone(true)
		} else {
			if err != nil {
				fmt.Fprintf(st, "\n\nError: %v\n", err)
				log.Println(st.BuilderRev, "failed:", err)
			}
			st.setDone(err == nil)
			pool.CoordinatorProcess().PutBuildRecord(st.buildRecord())
		}
		markDone(st.BuilderRev)
	}()
}

func (st *buildStatus) buildletPool() pool.Buildlet {
	return pool.ForHost(st.conf.HostConfig())
}

func (st *buildStatus) expectedMakeBashDuration() time.Duration {
	// TODO: base this on historical measurements, instead of statically configured.
	// TODO: move this to dashboard/builders.go? But once we base on historical
	// measurements, it'll need GCE services (bigtable/bigquery?), so it's probably
	// better in this file.
	goos, goarch := st.conf.GOOS(), st.conf.GOARCH()

	if goos == "linux" {
		if goarch == "arm" {
			return 4 * time.Minute
		}
		return 45 * time.Second
	}
	return 60 * time.Second
}

func (st *buildStatus) expectedBuildletStartDuration() time.Duration {
	// TODO: move this to dashboard/builders.go? But once we base on historical
	// measurements, it'll need GCE services (bigtable/bigquery?), so it's probably
	// better in this file.
	p := st.buildletPool()
	switch p.(type) {
	case *pool.GCEBuildlet:
		if strings.HasPrefix(st.Name, "android-") {
			// about a minute for buildlet + minute for Android emulator to be usable
			return 2 * time.Minute
		}
		return time.Minute
	case *pool.EC2Buildlet:
		// lack of historical data. 2 * time.Minute is a safe overestimate
		return 2 * time.Minute
	case *pool.ReverseBuildletPool:
		goos, arch := st.conf.GOOS(), st.conf.GOARCH()
		if goos == "darwin" {
			if arch == "arm" || arch == "arm64" {
				// iOS; idle or it's not.
				return 0
			}
			if arch == "amd64" || arch == "386" {
				return 0 // TODO: remove this once we're using VMware
				// return 1 * time.Minute // VMware boot of hermetic OS X
			}
		}
	}
	return 0
}

// getHelpersReadySoon waits a bit (as a function of the build
// configuration) and starts getting the buildlets for test sharding
// ready, such that they're ready when make.bash is done. But we don't
// want to start too early, lest we waste idle resources during make.bash.
func (st *buildStatus) getHelpersReadySoon() {
	if st.IsSubrepo() || st.conf.NumTestHelpers(st.isTry()) == 0 || st.conf.IsReverse() {
		return
	}
	time.AfterFunc(st.expectedMakeBashDuration()-st.expectedBuildletStartDuration(),
		func() {
			st.LogEventTime("starting_helpers")
			st.getHelpers() // and ignore the result.
		})
}

// getHelpers returns a channel of buildlet test helpers, with an item
// sent as they become available. The channel is closed at the end.
func (st *buildStatus) getHelpers() <-chan buildlet.Client {
	st.onceInitHelpers.Do(st.onceInitHelpersFunc)
	return st.helpers
}

func (st *buildStatus) onceInitHelpersFunc() {
	schedTmpl := &queue.SchedItem{
		BuilderRev: st.BuilderRev,
		HostType:   st.conf.HostType,
		IsTry:      st.isTry(),
		CommitTime: st.commitTime(),
		Branch:     st.RevBranch,
		Repo:       st.RepoOrGo(),
		User:       st.AuthorEmail,
	}
	st.helpers = getBuildlets(st.ctx, st.conf.NumTestHelpers(st.isTry()), schedTmpl, st)
}

// useSnapshot reports whether this type of build uses a snapshot of
// make.bash if it exists and that the snapshot exists.
func (st *buildStatus) useSnapshot() bool {
	return st.useSnapshotFor(st.Rev)
}

func (st *buildStatus) useSnapshotFor(rev string) bool {
	if st.conf.SkipSnapshot {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if b, ok := st.useSnapshotMemo[rev]; ok {
		return b
	}
	br := st.BuilderRev
	br.Rev = rev
	b := br.SnapshotExists(context.TODO(), pool.NewGCEConfiguration().BuildEnv())
	if st.useSnapshotMemo == nil {
		st.useSnapshotMemo = make(map[string]bool)
	}
	st.useSnapshotMemo[rev] = b
	return b
}

func (st *buildStatus) forceSnapshotUsage() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.useSnapshotMemo == nil {
		st.useSnapshotMemo = make(map[string]bool)
	}
	st.useSnapshotMemo[st.Rev] = true
}

func (st *buildStatus) checkDep(ctx context.Context, dep string) (have bool, err error) {
	span := st.CreateSpan("ask_maintner_has_ancestor")
	defer func() { span.Done(err) }()
	fails := 0
	for {
		res, err := maintnerClient.HasAncestor(ctx, &apipb.HasAncestorRequest{
			Commit:   st.Rev,
			Ancestor: dep,
		})
		if err != nil {
			fails++
			if fails == 3 {
				span.Done(err)
				return false, err
			}
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(1 * time.Second):
			}
			continue
		}
		if res.UnknownCommit {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(1 * time.Second):
			}
			continue
		}
		return res.HasAncestor, nil
	}
}

var errSkipBuildDueToDeps = errors.New("build was skipped due to missing deps")

func (st *buildStatus) getBuildlet() (buildlet.Client, error) {
	schedItem := &queue.SchedItem{
		HostType:   st.conf.HostType,
		IsTry:      st.trySet != nil,
		BuilderRev: st.BuilderRev,
		CommitTime: st.commitTime(),
		Repo:       st.RepoOrGo(),
		Branch:     st.RevBranch,
		User:       st.AuthorEmail,
	}
	st.mu.Lock()
	st.schedItem = schedItem
	st.mu.Unlock()

	sp := st.CreateSpan("get_buildlet")
	bc, err := sched.GetBuildlet(st.ctx, schedItem)
	sp.Done(err)
	if err != nil {
		err = fmt.Errorf("failed to get a buildlet: %v", err)
		go st.reportErr(err)
		return nil, err
	}
	atomic.StoreInt32(&st.hasBuildlet, 1)

	st.mu.Lock()
	st.bc = bc
	st.mu.Unlock()
	st.LogEventTime("using_buildlet", bc.IPPort())

	return bc, nil
}

func (st *buildStatus) build() error {
	if deps := st.conf.GoDeps; len(deps) > 0 {
		ctx, cancel := context.WithTimeout(st.ctx, 30*time.Second)
		defer cancel()
		for _, dep := range deps {
			has, err := st.checkDep(ctx, dep)
			if err != nil {
				fmt.Fprintf(st, "Error checking whether commit %s includes ancestor %s: %v\n", st.Rev, dep, err)
				return err
			}
			if !has {
				st.LogEventTime(eventSkipBuildMissingDep)
				fmt.Fprintf(st, "skipping build; commit %s lacks ancestor %s\n", st.Rev, dep)
				return errSkipBuildDueToDeps
			}
		}
		cancel()
	}

	pool.CoordinatorProcess().PutBuildRecord(st.buildRecord())

	bc, err := st.getBuildlet()
	if err != nil {
		return err
	}
	defer bc.Close()

	if st.useSnapshot() {
		if err := st.writeGoSnapshot(); err != nil {
			return err
		}
	} else {
		// Write the Go source and bootstrap tool chain in parallel.
		var grp syncutil.Group
		grp.Go(st.writeGoSource)
		grp.Go(st.writeBootstrapToolchain)
		if err := grp.Err(); err != nil {
			return err
		}
	}

	execStartTime := time.Now()
	fmt.Fprintf(st, "%s at %v", st.Name, st.Rev)
	if st.IsSubrepo() {
		fmt.Fprintf(st, " building %v at %v", st.SubName, st.SubRev)
	}
	fmt.Fprint(st, "\n\n")

	makeTest := st.CreateSpan("make_and_test") // warning: magic event named used by handleLogs

	remoteErr, err := st.runAllSharded()
	makeTest.Done(err)

	// bc (aka st.bc) may be invalid past this point, so let's
	// close it to make sure we don't accidentally use it.
	bc.Close()

	doneMsg := "all tests passed"
	if remoteErr != nil {
		doneMsg = "with test failures"
	} else if err != nil {
		doneMsg = "comm error: " + err.Error()
	}
	// If a build fails multiple times due to communication
	// problems with the buildlet, assume something's wrong with
	// the buildlet or machine and fail the build, rather than
	// looping forever. This promotes the err (communication
	// error) to a remoteErr (an error that occurred remotely and
	// is terminal).
	if rerr := st.repeatedCommunicationError(err); rerr != nil {
		remoteErr = rerr
		err = nil
		doneMsg = "communication error to buildlet (promoted to terminal error): " + rerr.Error()
		fmt.Fprintf(st, "\n%s\n", doneMsg)
	}
	if err != nil {
		// Return the error *before* we create the magic
		// "done" event. (which the try coordinator looks for)
		return err
	}
	st.LogEventTime(eventDone, doneMsg)

	if devPause {
		st.LogEventTime("DEV_MAIN_SLEEP")
		time.Sleep(5 * time.Minute)
	}

	if st.trySet == nil {
		buildLog := st.logs()
		if remoteErr != nil {
			// If we just have the line-or-so little
			// banner at top, that means we didn't get any
			// interesting output from the remote side, so
			// include the remoteErr text.  Otherwise,
			// assume that remoteErr is redundant with the
			// buildlog text itself.
			if strings.Count(buildLog, "\n") < 10 {
				buildLog += "\n" + remoteErr.Error()
			}
		}
		if err := recordResult(st.BuilderRev, remoteErr == nil, buildLog, time.Since(execStartTime)); err != nil {
			if remoteErr != nil {
				return fmt.Errorf("Remote error was %q but failed to report it to the dashboard: %v", remoteErr, err)
			}
			return fmt.Errorf("Build succeeded but failed to report it to the dashboard: %v", err)
		}
	}
	if remoteErr != nil {
		return remoteErr
	}
	return nil
}

func (st *buildStatus) HasBuildlet() bool { return atomic.LoadInt32(&st.hasBuildlet) != 0 }

// useKeepGoingFlag reports whether this build should use -k flag of 'go tool
// dist test', which makes it keep going even when some tests have failed.
func (st *buildStatus) useKeepGoingFlag() bool {
	// For now, keep going for post-submit builders on release branches,
	// because we prioritize seeing more complete test results over failing fast.
	// Later on, we may start doing this all post-submit builders on all branches.
	// See golang.org/issue/14305.
	//
	// TODO(golang.org/issue/36181): A more ideal long term solution is one that reports
	// a failure fast, but still keeps going to make all other test results available.
	return !st.isTry() && strings.HasPrefix(st.branch(), "release-branch.go")
}

// isTry reports whether the build is a part of a TryBot (pre-submit) run.
// It may be a normal TryBot (part of the default try set) or a SlowBot.
func (st *buildStatus) isTry() bool { return st.trySet != nil }

// isSlowBot reports whether the build is an explicitly requested SlowBot.
func (st *buildStatus) isSlowBot() bool {
	if st.trySet == nil {
		return false
	}
	return slices.Contains(st.trySet.slowBots, st.conf)
}

func (st *buildStatus) buildRecord() *types.BuildRecord {
	rec := &types.BuildRecord{
		ID:        st.buildID,
		ProcessID: processID,
		StartTime: st.startTime,
		IsTry:     st.isTry(),
		IsSlowBot: st.isSlowBot(),
		GoRev:     st.Rev,
		Rev:       st.SubRevOrGoRev(),
		Repo:      st.RepoOrGo(),
		Builder:   st.Name,
		OS:        st.conf.GOOS(),
		Arch:      st.conf.GOARCH(),
	}

	// Log whether we used COS, so we can do queries to analyze
	// Kubernetes vs COS performance for containers.
	if st.conf.IsContainer() && pool.ForHost(st.conf.HostConfig()) == pool.NewGCEConfiguration().BuildletPool() {
		rec.ContainerHost = "cos"
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	// TODO: buildlet instance name
	if !st.done.IsZero() {
		rec.EndTime = st.done
		rec.LogURL = st.logURL
		rec.Seconds = rec.EndTime.Sub(rec.StartTime).Seconds()
		if st.succeeded {
			rec.Result = "ok"
		} else {
			rec.Result = "fail"
		}
	}
	return rec
}

func (st *buildStatus) SpanRecord(sp *schedule.Span, err error) *types.SpanRecord {
	rec := &types.SpanRecord{
		BuildID: st.buildID,
		IsTry:   st.isTry(),
		GoRev:   st.Rev,
		Rev:     st.SubRevOrGoRev(),
		Repo:    st.RepoOrGo(),
		Builder: st.Name,
		OS:      st.conf.GOOS(),
		Arch:    st.conf.GOARCH(),

		Event:     sp.Event(),
		Detail:    sp.OptText(),
		StartTime: sp.Start(),
		EndTime:   sp.End(),
		Seconds:   sp.End().Sub(sp.Start()).Seconds(),
	}
	if err != nil {
		rec.Error = err.Error()
	}
	return rec
}

// goBuilder returns a GoBuilder for this buildStatus.
func (st *buildStatus) goBuilder() buildgo.GoBuilder {
	goDevDLBootstrap := strings.HasPrefix(
		st.conf.GoBootstrapURL(pool.NewGCEConfiguration().BuildEnv()), "https://go.dev/dl/")
	return buildgo.GoBuilder{
		Logger:           st,
		BuilderRev:       st.BuilderRev,
		Conf:             st.conf,
		Goroot:           "go",
		GoDevDLBootstrap: goDevDLBootstrap,
		Force:            true,
	}
}

// runAllSharded runs make.bash and then shards the test execution.
// remoteErr and err are as described at the top of this file.
//
// After runAllSharded returns, the caller must assume that st.bc
// might be invalid (It's possible that only one of the helper
// buildlets survived).
func (st *buildStatus) runAllSharded() (remoteErr, err error) {
	st.getHelpersReadySoon()

	if !st.useSnapshot() {
		remoteErr, err = st.goBuilder().RunMake(st.ctx, st.bc, st)
		if err != nil {
			return nil, err
		}
		if remoteErr != nil {
			return fmt.Errorf("build failed: %v", remoteErr), nil
		}
	}
	if st.conf.StopAfterMake {
		return nil, nil
	}

	if err := st.doSnapshot(st.bc); err != nil {
		return nil, err
	}

	switch {
	case st.conf.RunBench:
		remoteErr, err = st.runBenchmarkTests()
	case st.IsSubrepo():
		remoteErr, err = st.runSubrepoTests()
	case st.conf.IsCrossCompileOnly():
		remoteErr, err = st.buildTestPackages()
	default:
		// Only run platform tests if we're not cross-compiling.
		// dist can't actually build test packages without running them yet.
		// See #58297.
		remoteErr, err = st.runTests(st.getHelpers())
	}

	if err == errBuildletsGone {
		// Don't wrap this error. TODO: use xerrors.
		return nil, errBuildletsGone
	}
	if err != nil {
		return nil, fmt.Errorf("runTests: %v", err)
	}
	if remoteErr != nil {
		return fmt.Errorf("tests failed: %v", remoteErr), nil
	}
	return nil, nil
}

// buildTestPackages runs `go tool dist test -compile-only`, which builds all standard
// library test packages but does not run any tests. Used in cross-compilation modes.
func (st *buildStatus) buildTestPackages() (remoteErr, err error) {
	if st.RevBranch == "release-branch.go1.20" {
		// Go 1.20 doesn't support `go tool dist test -compile-only` very well.
		// TODO(mknyszek): Remove this condition when Go 1.20 is no longer supported.
		return nil, nil
	}
	sp := st.CreateSpan("build_test_pkgs")
	remoteErr, err = st.bc.Exec(st.ctx, path.Join("go", "bin", "go"), buildlet.ExecOpts{
		Output: st,
		Debug:  true,
		Args:   []string{"tool", "dist", "test", "-compile-only"},
	})
	if err != nil {
		sp.Done(err)
		return nil, err
	}
	if remoteErr != nil {
		sp.Done(remoteErr)
		return fmt.Errorf("go tool dist test -compile-only failed: %v", remoteErr), nil
	}
	sp.Done(nil)
	return nil, nil
}

func (st *buildStatus) doSnapshot(bc buildlet.Client) error {
	// If we're using a pre-built snapshot, don't make another.
	if st.useSnapshot() {
		return nil
	}
	if st.conf.SkipSnapshot {
		return nil
	}
	if pool.NewGCEConfiguration().BuildEnv().SnapBucket == "" {
		// Build environment isn't configured to do snapshots.
		return nil
	}
	if err := st.cleanForSnapshot(bc); err != nil {
		return fmt.Errorf("cleanForSnapshot: %v", err)
	}
	if err := st.writeSnapshot(bc); err != nil {
		return fmt.Errorf("writeSnapshot: %v", err)
	}
	return nil
}

func (st *buildStatus) writeGoSnapshot() (err error) {
	return st.writeGoSnapshotTo(st.Rev, "go")
}

func (st *buildStatus) writeGoSnapshotTo(rev, dir string) (err error) {
	sp := st.CreateSpan("write_snapshot_tar")
	defer func() { sp.Done(err) }()

	snapshotURL := pool.NewGCEConfiguration().BuildEnv().SnapshotURL(st.Name, rev)

	if err := st.bc.PutTarFromURL(st.ctx, snapshotURL, dir); err != nil {
		return fmt.Errorf("failed to put baseline snapshot to buildlet: %v", err)
	}
	return nil
}

func (st *buildStatus) writeGoSource() error {
	return st.writeGoSourceTo(st.bc, st.Rev, "go")
}

func (st *buildStatus) writeGoSourceTo(bc buildlet.Client, rev, dir string) error {
	// Write the VERSION file.
	sp := st.CreateSpan("write_version_tar")
	if err := bc.PutTar(st.ctx, buildgo.VersionTgz(rev), dir); err != nil {
		return sp.Done(fmt.Errorf("writing VERSION tgz: %v", err))
	}

	srcTar, err := sourcecache.GetSourceTgz(st, "go", rev)
	if err != nil {
		return err
	}
	sp = st.CreateSpan("write_go_src_tar")
	if err := bc.PutTar(st.ctx, srcTar, dir); err != nil {
		return sp.Done(fmt.Errorf("writing tarball from Gerrit: %v", err))
	}
	return sp.Done(nil)
}

func (st *buildStatus) writeBootstrapToolchain() error {
	u := st.conf.GoBootstrapURL(pool.NewGCEConfiguration().BuildEnv())
	if u == "" {
		return nil
	}
	const bootstrapDir = "go1.4" // might be newer; name is the default
	sp := st.CreateSpan("write_go_bootstrap_tar")
	return sp.Done(st.bc.PutTarFromURL(st.ctx, u, bootstrapDir))
}

func (st *buildStatus) cleanForSnapshot(bc buildlet.Client) error {
	sp := st.CreateSpan("clean_for_snapshot")
	return sp.Done(bc.RemoveAll(st.ctx,
		"go/doc/gopher",
		"go/pkg/bootstrap",
	))
}

func (st *buildStatus) writeSnapshot(bc buildlet.Client) (err error) {
	sp := st.CreateSpan("write_snapshot_to_gcs")
	defer func() { sp.Done(err) }()
	// A typical Go snapshot tarball in April 2022 is around 150 MB in size.
	// Builders with a fast uplink speed can upload the tar within seconds or minutes.
	// Reverse builders might be far away on the network, so be more lenient for them.
	// (Fast builds require a sufficiently fast uplink speed or turning off snapshots,
	// so the timeout here is mostly an upper bound to prevent infinite hangs.)
	timeout := 5 * time.Minute
	if st.conf.IsReverse() {
		timeout *= 3
	}
	ctx, cancel := context.WithTimeout(st.ctx, timeout)
	defer cancel()

	tsp := st.CreateSpan("fetch_snapshot_reader_from_buildlet")
	tgz, err := bc.GetTar(ctx, "go")
	tsp.Done(err)
	if err != nil {
		return err
	}
	defer tgz.Close()

	sc := pool.NewGCEConfiguration().StorageClient()
	if sc == nil {
		return errors.New("GCE configuration missing storage client")
	}
	bucket := pool.NewGCEConfiguration().BuildEnv().SnapBucket
	if bucket == "" {
		return errors.New("build environment missing snapshot bucket")
	}
	wr := sc.Bucket(bucket).Object(st.SnapshotObjectName()).NewWriter(ctx)
	wr.ContentType = "application/octet-stream"
	if n, err := io.Copy(wr, tgz); err != nil {
		st.logf("failed to write snapshot to GCS after copying %d bytes: %v", n, err)
		return err
	}

	return wr.Close()
}

// toolchainBaselineCommit determines the toolchain baseline commit for this
// benchmark run.
func (st *buildStatus) toolchainBaselineCommit() (baseline string, err error) {
	sp := st.CreateSpan("list_go_releases")
	defer func() { sp.Done(err) }()

	// TODO(prattmic): Cache responses for a while. These won't change often.
	res, err := maintnerClient.ListGoReleases(st.ctx, &apipb.ListGoReleasesRequest{})
	if err != nil {
		return "", err
	}

	releases := res.GetReleases()
	if len(releases) == 0 {
		return "", fmt.Errorf("no Go releases: %v", res)
	}

	if st.RevBranch == "master" {
		// Testing master, baseline is latest release.
		return releases[0].GetTagCommit(), nil
	}

	// Testing release branch. Baseline is latest patch version of this
	// release.
	for _, r := range releases {
		if st.RevBranch == r.GetBranchName() {
			return r.GetTagCommit(), nil
		}
	}

	return "", fmt.Errorf("cannot find latest release for %s", st.RevBranch)
}

// Temporarily hard-code the subrepo baseline commits to use.
//
// TODO(rfindley): in the future, we should use the latestRelease method to
// automatically choose the latest patch release of the previous minor version
// (e.g. v0.11.x while we're working on v0.12.y).
var subrepoBaselines = map[string]string{
	"tools": "6ce74ceaddcc4ff081d22ae134f4264a667d394f", // gopls@v0.11.0, with additional instrumentation for memory and CPU usage
}

// subrepoBaselineCommit determines the baseline commit for this subrepo benchmark run.
func (st *buildStatus) subrepoBaselineCommit() (baseline string, err error) {
	commit, ok := subrepoBaselines[st.SubName]
	if !ok {
		return "", fmt.Errorf("unknown subrepo for benchmarking %q", st.SubName)
	}
	return commit, nil
}

// latestRelease returns the latest release version for a module in subrepo. If
// submodule is non-empty, it is the path to a subdirectory containing the
// submodule of interest (for example submodule is "gopls" if we are
// considering the module golang.org/x/tools/gopls). Otherwise the module is
// assumed to be at repo root.
//
// It is currently unused, but preserved for future use by the
// subrepoBaselineCommit method.
func (st *buildStatus) latestRelease(submodule string) (string, error) {
	// Baseline is the latest gopls release tag (but not prerelease).
	gerritClient := pool.NewGCEConfiguration().GerritClient()
	tags, err := gerritClient.GetProjectTags(st.ctx, st.SubName)
	if err != nil {
		return "", fmt.Errorf("error fetching tags for %q: %w", st.SubName, err)
	}

	var versions []string
	revisions := make(map[string]string)
	prefix := "refs/tags"
	if submodule != "" {
		prefix += "/" + submodule // e.g. gopls tags are "gopls/vX.Y.Z"
	}
	for ref, ti := range tags {
		if !strings.HasPrefix(ref, prefix) {
			continue
		}
		version := ref[len(prefix):]
		versions = append(versions, version)
		revisions[version] = ti.Revision
	}

	semver.Sort(versions)

	// Return latest non-prerelease version.
	for i := len(versions) - 1; i >= 0; i-- {
		ver := versions[i]
		if !semver.IsValid(ver) {
			continue
		}
		if semver.Prerelease(ver) != "" {
			continue
		}
		return revisions[ver], nil
	}

	return "", fmt.Errorf("no valid versions found in %+v", versions)
}

// reportErr reports an error to Stackdriver.
func (st *buildStatus) reportErr(err error) {
	gceErrsClient := pool.NewGCEConfiguration().ErrorsClient()
	if gceErrsClient == nil {
		// errorsClient is nil in dev environments.
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = fmt.Errorf("buildID: %v, name: %s, hostType: %s, error: %v", st.buildID, st.conf.Name, st.conf.HostType, err)
	gceErrsClient.ReportSync(ctx, errorreporting.Entry{Error: err})
}

// distTestList uses 'go tool dist test -list' to get a list of dist test names.
//
// As of Go 1.21, the dist test naming pattern has changed to always be in the
// form of "<pkg>[:<variant>]", where "<pkg>" means what used to be previously
// named "go_test:<pkg>". distTestList maps those new dist test names back to
// that previous format, a combination of "go_test[_bench]:<pkg>" and others.
func (st *buildStatus) distTestList() (names []distTestName, remoteErr, err error) {
	workDir, err := st.bc.WorkDir(st.ctx)
	if err != nil {
		err = fmt.Errorf("distTestList, WorkDir: %v", err)
		return
	}
	goroot := st.conf.FilePathJoin(workDir, "go")

	args := []string{"tool", "dist", "test", "--no-rebuild", "--list"}
	if st.conf.IsRace() {
		args = append(args, "--race")
	}
	if st.conf.CompileOnly {
		args = append(args, "--compile-only")
	}
	var buf bytes.Buffer
	remoteErr, err = st.bc.Exec(st.ctx, "./go/bin/go", buildlet.ExecOpts{
		Output:      &buf,
		ExtraEnv:    append(st.conf.Env(), "GOROOT="+goroot),
		OnStartExec: func() { st.LogEventTime("discovering_tests") },
		Path:        []string{st.conf.FilePathJoin("$WORKDIR", "go", "bin"), "$PATH"},
		Args:        args,
	})
	if remoteErr != nil {
		remoteErr = fmt.Errorf("Remote error: %v, %s", remoteErr, buf.Bytes())
		err = nil
		return
	}
	if err != nil {
		err = fmt.Errorf("Exec error: %v, %s", err, buf.Bytes())
		return
	}
	// To avoid needing to update all the existing dist test adjust policies,
	// it's easier to remap new dist test names in "<pkg>[:<variant>]" format
	// to ones used in Go 1.20 and prior. Do that for now.
	for _, test := range go120DistTestNames(strings.Fields(buf.String())) {
		isNormalTry := st.isTry() && !st.isSlowBot()
		if !st.conf.ShouldRunDistTest(test.Old, isNormalTry) {
			continue
		}
		names = append(names, test)
	}
	return names, nil, nil
}

// go120DistTestNames converts a list of dist test names from
// an arbitrary Go distribution to the format used in Go 1.20
// and prior versions. (Go 1.21 introduces a simpler format.)
//
// This exists only to avoid rewriting current dist adjust policies.
// We wish to avoid new dist adjust policies, but if they're truly needed,
// they can choose to start using new dist test names instead.
func go120DistTestNames(names []string) []distTestName {
	if len(names) == 0 {
		// Only happens if there's a problem, but no need to panic.
		return nil
	} else if strings.HasPrefix(names[0], "go_test:") {
		// In Go 1.21 and newer no dist tests have a "go_test:" prefix.
		// In Go 1.20 and older, go tool dist test -list always returns
		// at least one "go_test:*" test first.
		// So if we see it, the list is already in Go 1.20 format.
		var s []distTestName
		for _, old := range names {
			s = append(s, distTestName{old, old})
		}
		return s
	}
	// Remap the new Go 1.21+ dist test names to old ones.
	var s []distTestName
	for _, new := range names {
		var old string
		switch pkg, variant, _ := strings.Cut(new, ":"); {
		// Special cases. Enough to cover what's used by old dist
		// adjust policies. Not much use in going far beyond that.
		case variant == "nolibgcc":
			old = "nolibgcc:" + pkg
		case variant == "race":
			old = "race"
		case variant == "moved_goroot":
			old = "moved_goroot"
		case pkg == "cmd/internal/testdir":
			if variant == "" {
				// Handle this too for when we stop doing special-case sharding only for testdir inside dist.
				variant = "0_1"
			}
			old = "test:" + variant
		case pkg == "cmd/api" && variant == "check":
			old = "api"
		case pkg == "cmd/internal/bootstrap_test":
			old = "reboot"

		// Easy regular cases.
		case variant == "":
			old = "go_test:" + pkg
		case variant == "racebench":
			old = "go_test_bench:" + pkg

		// Neither a known special case nor a regular case.
		default:
			old = new // Less bad than leaving it empty.
		}
		s = append(s, distTestName{Old: old, Raw: new})
	}
	return s
}

type token struct{}

// newTestSet returns a new testSet given the dist test names (from "go tool dist test -list")
// and benchmark items.
func (st *buildStatus) newTestSet(testStats *buildstats.TestStats, names []distTestName) (*testSet, error) {
	set := &testSet{
		st:        st,
		testStats: testStats,
	}
	for _, name := range names {
		set.items = append(set.items, &testItem{
			set:      set,
			name:     name,
			duration: testStats.Duration(st.BuilderRev.Name, name.Old),
			take:     make(chan token, 1),
			done:     make(chan token),
		})
	}
	return set, nil
}

var (
	testStats       atomic.Value // of *buildstats.TestStats
	testStatsLoader singleflight.Group
)

func getTestStats(sl spanlog.Logger) *buildstats.TestStats {
	sp := sl.CreateSpan("get_test_stats")
	ts, ok := testStats.Load().(*buildstats.TestStats)
	if ok && ts.AsOf.After(time.Now().Add(-1*time.Hour)) {
		sp.Done(nil)
		return ts
	}
	v, err, _ := testStatsLoader.Do("", func() (any, error) {
		log.Printf("getTestStats: reloading from BigQuery...")
		sp := sl.CreateSpan("query_test_stats")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		ts, err := buildstats.QueryTestStats(ctx, pool.NewGCEConfiguration().BuildEnv())
		sp.Done(err)
		if err != nil {
			log.Printf("getTestStats: error: %v", err)
			return nil, err
		}
		testStats.Store(ts)
		return ts, nil
	})
	if err != nil {
		sp.Done(err)
		return nil
	}
	sp.Done(nil)
	return v.(*buildstats.TestStats)
}

func (st *buildStatus) runSubrepoTests() (remoteErr, err error) {
	st.LogEventTime("fetching_subrepo", st.SubName)

	workDir, err := st.bc.WorkDir(st.ctx)
	if err != nil {
		err = fmt.Errorf("error discovering workdir for helper %s: %v", st.bc.IPPort(), err)
		return nil, err
	}
	goroot := st.conf.FilePathJoin(workDir, "go")
	gopath := st.conf.FilePathJoin(workDir, "gopath")

	// A goTestRun represents a single invocation of the 'go test' command.
	type goTestRun struct {
		Dir      string   // Directory where 'go test' should be executed.
		Patterns []string // Import path patterns to provide to 'go test'.
	}
	// Test all packages selected by the "./..." pattern at the repository root.
	// (If there are modules in subdirectories, they'll be found and handled below.)
	repoPath := importPathOfRepo(st.SubName)
	testRuns := []goTestRun{{
		Dir:      "gopath/src/" + repoPath,
		Patterns: []string{"./..."},
	}}

	// Check out the provided sub-repo to the buildlet's workspace so we
	// can find go.mod files and run tests in it.
	{
		tgz, err := sourcecache.GetSourceTgz(st, st.SubName, st.SubRev)
		if errors.As(err, new(sourcecache.TooBigError)) {
			// Source being too big is a non-retryable error.
			return err, nil
		} else if err != nil {
			return nil, err
		}
		err = st.bc.PutTar(st.ctx, tgz, "gopath/src/"+repoPath)
		if err != nil {
			return nil, err
		}
	}

	// Look for inner modules, in order to test them too. See golang.org/issue/32528.
	sp := st.CreateSpan("listing_subrepo_modules", st.SubName)
	err = st.bc.ListDir(st.ctx, "gopath/src/"+repoPath, buildlet.ListDirOpts{Recursive: true}, func(e buildlet.DirEntry) {
		goModFile := path.Base(e.Name()) == "go.mod" && !e.IsDir()
		if !goModFile {
			return
		}
		// Found a go.mod file in a subdirectory, which indicates the root of a module.
		modulePath := path.Join(repoPath, path.Dir(e.Name()))
		if modulePath == repoPath {
			// This is the go.mod file at the repository root.
			// It's already a part of testRuns, so skip it.
			return
		} else if ignoredByGoTool(modulePath) || isVendored(modulePath) {
			// go.mod file is in a directory we're not looking to support, so skip it.
			return
		}
		// Add an additional test run entry that will test all packages in this module.
		testRuns = append(testRuns, goTestRun{
			Dir:      "gopath/src/" + modulePath,
			Patterns: []string{"./..."},
		})
	})
	sp.Done(err)
	if err != nil {
		return nil, err
	}

	// Finally, execute all of the test runs.
	// If any fail, keep going so that all test results are included in the output.

	sp = st.CreateSpan("running_subrepo_tests", st.SubName)
	defer func() { sp.Done(err) }()

	env := append(st.conf.Env(),
		"GOROOT="+goroot,
		"GOPATH="+gopath,
	)
	env = append(env, st.modulesEnv()...)

	args := []string{"test"}
	if st.conf.CompileOnly {
		// Build all packages, but avoid running the binary by executing /bin/true for the tests.
		// We assume for a compile-only build we're just running on a Linux system.
		args = append(args, "-exec", "/bin/true")
	} else {
		if !st.conf.IsLongTest() {
			args = append(args, "-short")
		}
		if st.conf.IsRace() {
			args = append(args, "-race")
		}
		if scale := st.conf.GoTestTimeoutScale(); scale != 1 {
			const goTestDefaultTimeout = 10 * time.Minute // Default value taken from Go 1.20.
			args = append(args, "-timeout="+(goTestDefaultTimeout*time.Duration(scale)).String())
		}
	}

	var remoteErrors []error
	for _, tr := range testRuns {
		rErr, err := st.bc.Exec(st.ctx, "./go/bin/go", buildlet.ExecOpts{
			Debug:    true, // make buildlet print extra debug in output for failures
			Output:   st,
			Dir:      tr.Dir,
			ExtraEnv: env,
			Path:     []string{st.conf.FilePathJoin("$WORKDIR", "go", "bin"), "$PATH"},
			Args:     append(args, tr.Patterns...),
		})
		if err != nil {
			// A network/communication error. Give up here;
			// the caller can retry as it sees fit.
			return nil, err
		} else if rErr != nil {
			// An error occurred remotely and is terminal, but we want to
			// keep testing other packages and report their failures too,
			// rather than stopping short.
			remoteErrors = append(remoteErrors, rErr)
		}
	}
	if len(remoteErrors) > 0 {
		return multiError(remoteErrors), nil
	}
	return nil, nil
}

// ignoredByGoTool reports whether the given import path corresponds
// to a directory that would be ignored by the go tool.
//
// The logic of the go tool for ignoring directories is documented at
// https://golang.org/cmd/go/#hdr-Package_lists_and_patterns:
//
//	Directory and file names that begin with "." or "_" are ignored
//	by the go tool, as are directories named "testdata".
func ignoredByGoTool(importPath string) bool {
	for el := range strings.SplitSeq(importPath, "/") {
		if strings.HasPrefix(el, ".") || strings.HasPrefix(el, "_") || el == "testdata" {
			return true
		}
	}
	return false
}

// isVendored reports whether the given import path corresponds
// to a Go package that is inside a vendor directory.
//
// The logic for what is considered a vendor directory is documented at
// https://golang.org/cmd/go/#hdr-Vendor_Directories.
func isVendored(importPath string) bool {
	return strings.HasPrefix(importPath, "vendor/") ||
		strings.Contains(importPath, "/vendor/")
}

// multiError is a concatenation of multiple errors.
// There must be one or more errors, and all must be non-nil.
type multiError []error

// Error concatenates all error strings into a single string,
// using a semicolon and space as a separator.
func (m multiError) Error() string {
	if len(m) == 1 {
		return m[0].Error()
	}

	var b strings.Builder
	for i, e := range m {
		if i != 0 {
			b.WriteString("; ")
		}
		b.WriteString(e.Error())
	}
	return b.String()
}

// internalModuleProxy returns the GOPROXY environment value to use for
// most module-enabled tests.
//
// We go through an internal (10.0.0.0/8) proxy that then hits
// https://proxy.golang.org/ so we're still able to firewall
// non-internal outbound connections on builder nodes.
//
// This internalModuleProxy func in prod mode (when running on GKE) returns an
// http URL to the current GKE pod's IP with a Kubernetes NodePort service port
// that forwards back to the coordinator's 8123. See comment below.
func internalModuleProxy() string {
	// We run a NodePort service on each GKE node
	// (cmd/coordinator/module-proxy-service.yaml) on port 30157
	// that maps back the coordinator's port 8123. (We could round
	// robin over all the GKE nodes' IPs if we wanted, but the
	// coordinator is running on GKE so our node by definition is
	// up, so just use it. It won't be much traffic.)
	// TODO: migrate to a GKE internal load balancer with an internal static IP
	// once we migrate symbolic-datum-552 off a Legacy VPC network to the modern
	// scheme that supports internal static IPs.
	return "http://" + pool.NewGCEConfiguration().GKENodeHostname() + ":30157"
}

// modulesEnv returns the extra module-specific environment variables
// to append to tests.
func (st *buildStatus) modulesEnv() (env []string) {
	// GOPROXY
	switch {
	case st.SubName == "" && !st.conf.OutboundNetworkAllowed():
		env = append(env, "GOPROXY=off")
	case st.conf.PrivateGoProxy():
		// Don't add GOPROXY, the builder is pre-configured.
	case pool.NewGCEConfiguration().BuildEnv() == nil || !pool.NewGCEConfiguration().BuildEnv().IsProd:
		// Dev mode; use the system default.
		env = append(env, "GOPROXY="+os.Getenv("GOPROXY"))
	case st.conf.IsGCE() && !migration.StopInternalModuleProxy:
		// On GCE; the internal proxy is accessible, prefer that.
		env = append(env, "GOPROXY="+internalModuleProxy())
	default:
		// Everything else uses the public proxy.
		env = append(env, "GOPROXY=https://proxy.golang.org")

		if migration.StopInternalModuleProxy {
			// If the internal module proxy is stopped, we can't disable outbound
			// network without also breaking downloading of module dependencies
			// (since proxy.golang.org would be inaccessible). Disabling outbound
			// network is a nice-to-have to detect tests that accidentally try to
			// use the internet in -short test mode, and that functionality is
			// handled by LUCI, so it's fine not to do it in the coordinator now.
			env = append(env, "GO_DISABLE_OUTBOUND_NETWORK=0")
		}
	}

	return env
}

// runBenchmarkTests runs benchmarks from x/benchmarks when RunBench is set.
func (st *buildStatus) runBenchmarkTests() (remoteErr, err error) {
	if st.SubName == "" {
		return nil, fmt.Errorf("benchmark tests must run on a subrepo")
	}

	// Repository under test.
	//
	// When running benchmarks, there are numerous variables:
	//
	// * Go experiment version
	// * Go baseline version
	// * Subrepo experiment version (if benchmarking subrepo)
	// * Subrepo baseline version (if benchmarking subrepo)
	// * x/benchmarks version (which defines which benchmarks run and how
	//   regardless of which repo is under test)
	//
	// For benchmarking of the main Go repo, the first three are used.
	// Ideally, the coordinator scheduler would handle the combinatorics on
	// testing these. Unfortunately, the coordinator doesn't handle
	// three-way combinations. By running Go benchmarks as a "subrepo test"
	// for x/benchmark, we can at least get the scheduler to handle the
	// x/benchmarks version (st.SubRev) and Go experiment version (st.Rev).
	// The Go baseline version is simply selected as the most recent
	// previous release tag (e.g., 1.18.x on release-branch.go1.18) at the
	// time this test runs (st.installBaselineToolchain below).
	//
	// When benchmarking a subrepo, we want to compare a subrepo experiment
	// version vs subrepo baseline version (_not_ compare a single subrepo
	// version vs baseline/experiment Go versions). We do need to build the
	// subrepo with some version of Go, so we choose to use the latest
	// released version at the time of testing (same as Go baseline above).
	// We'd like the coordinator to handle the combination of x/benchmarks
	// and x/<subrepo>, however the coordinator can't do multiple subrepo
	// combinations.
	//
	// Thus, we run these as typical subrepo builders, which gives us the
	// subrepo experiment version and a Go experiment version (which we
	// will ignore). The Go baseline version is selected as above, and the
	// subrepo baseline version is selected as the latest (non-pre-release)
	// tag in the subrepo.
	//
	// This setup is suboptimal because the caller is installing an
	// experiment Go version that we won't use when building the subrepo
	// (we'll use the Go baseline version). We'll also end up with
	// duplicate runs with identical subrepo experiment/baseline and
	// x/benchmarks versions, as builds will trigger on every commit to the
	// Go repo. Limiting subrepo builders to release branches can
	// significantly reduce the number of Go commit triggers.
	//
	// TODO(prattmic): Cleaning this up is good future work, but these
	// deficiencies are not particularly problematic and avoid the need for
	// major changes in other parts of the coordinator.
	repo := st.SubName
	if repo == "benchmarks" {
		repo = "go"
	}

	const (
		baselineDir        = "go-baseline"
		benchmarksDir      = "benchmarks"
		subrepoDir         = "subrepo"
		subrepoBaselineDir = "subrepo-baseline"
	)

	workDir, err := st.bc.WorkDir(st.ctx)
	if err != nil {
		err = fmt.Errorf("error discovering workdir for helper %s: %v", st.bc.IPPort(), err)
		return nil, err
	}
	goroot := st.conf.FilePathJoin(workDir, "go")
	baselineGoroot := st.conf.FilePathJoin(workDir, baselineDir)
	gopath := st.conf.FilePathJoin(workDir, "gopath")

	// Install baseline toolchain in addition to the experiment toolchain.
	toolchainBaselineCommit, remoteErr, err := st.installBaselineToolchain(goroot, baselineDir)
	if remoteErr != nil || err != nil {
		return remoteErr, err
	}

	// Install x/benchmarks.
	benchmarksCommit, remoteErr, err := st.fetchBenchmarksSource(benchmarksDir)
	if remoteErr != nil || err != nil {
		return remoteErr, err
	}

	// If testing a repo other than Go, install the subrepo and its baseline.
	var subrepoBaselineCommit string
	if repo != "go" {
		subrepoBaselineCommit, remoteErr, err = st.fetchSubrepoAndBaseline(subrepoDir, subrepoBaselineDir)
		if remoteErr != nil || err != nil {
			return remoteErr, err
		}
	}

	// Run golang.org/x/benchmarks/cmd/bench to perform benchmarks.
	sp := st.CreateSpan("running_benchmark_tests", st.SubName)
	defer func() { sp.Done(err) }()

	env := append(st.conf.Env(),
		"BENCH_BASELINE_GOROOT="+baselineGoroot,
		"BENCH_BRANCH="+st.RevBranch,
		"BENCH_REPOSITORY="+repo,
		"GOROOT="+goroot,
		"GOPATH="+gopath, // For module cache storage
	)
	env = append(env, st.modulesEnv()...)
	if repo != "go" {
		env = append(env, "BENCH_SUBREPO_PATH="+st.conf.FilePathJoin(workDir, subrepoDir))
		env = append(env, "BENCH_SUBREPO_BASELINE_PATH="+st.conf.FilePathJoin(workDir, subrepoBaselineDir))
	}
	rErr, err := st.bc.Exec(st.ctx, "./go/bin/go", buildlet.ExecOpts{
		Debug:    true, // make buildlet print extra debug in output for failures
		Output:   st,
		Dir:      benchmarksDir,
		ExtraEnv: env,
		Path:     []string{st.conf.FilePathJoin("$WORKDIR", "go", "bin"), "$PATH"},
		Args:     []string{"run", "golang.org/x/benchmarks/cmd/bench"},
	})
	if err != nil || rErr != nil {
		return rErr, err
	}

	// Upload benchmark results on success.
	if err := st.uploadBenchResults(toolchainBaselineCommit, subrepoBaselineCommit, benchmarksCommit); err != nil {
		return nil, err
	}
	return nil, nil
}

func (st *buildStatus) uploadBenchResults(toolchainBaselineCommit, subrepoBaselineCommit, benchmarksCommit string) (err error) {
	sp := st.CreateSpan("upload_bench_results")
	defer func() { sp.Done(err) }()

	s := pool.NewGCEConfiguration().BuildEnv().PerfDataURL
	if s == "" {
		log.Printf("No perfdata URL, skipping benchmark upload")
		return nil
	}
	client := &perfstorage.Client{BaseURL: s, HTTPClient: pool.NewGCEConfiguration().OAuthHTTPClient()}
	u := client.NewUpload(st.ctx)
	w, err := u.CreateFile("results")
	if err != nil {
		u.Abort()
		return fmt.Errorf("error creating perfdata file: %w", err)
	}

	// Prepend some useful metadata.
	var b strings.Builder
	if subrepoBaselineCommit != "" {
		// Subrepos compare two subrepo commits.
		fmt.Fprintf(&b, "experiment-commit: %s\n", st.SubRev)
		fmt.Fprintf(&b, "experiment-commit-time: %s\n", st.SubRevCommitTime.In(time.UTC).Format(time.RFC3339Nano))
		fmt.Fprintf(&b, "baseline-commit: %s\n", subrepoBaselineCommit)
		// Subrepo benchmarks typically don't care about the toolchain
		// version, but we should still provide the data as toolchain
		// version changes may cause a performance discontinuity.
		fmt.Fprintf(&b, "toolchain-commit: %s\n", toolchainBaselineCommit)
	} else {
		// Go repo compares two main repo commits.
		fmt.Fprintf(&b, "experiment-commit: %s\n", st.Rev)
		fmt.Fprintf(&b, "experiment-commit-time: %s\n", st.RevCommitTime.In(time.UTC).Format(time.RFC3339Nano))
		fmt.Fprintf(&b, "baseline-commit: %s\n", toolchainBaselineCommit)
	}
	fmt.Fprintf(&b, "benchmarks-commit: %s\n", benchmarksCommit)
	fmt.Fprintf(&b, "post-submit: %t\n", st.trySet == nil)
	if _, err := w.Write([]byte(b.String())); err != nil {
		u.Abort()
		return fmt.Errorf("error writing perfdata metadata with contents %q: %w", b.String(), err)
	}

	// TODO(prattmic): Full log output may contain non-benchmark output
	// that can be erroneously parsed as benchfmt.
	if _, err := w.Write([]byte(st.logs())); err != nil {
		u.Abort()
		return fmt.Errorf("error writing perfdata file with contents %q: %w", st.logs(), err)
	}
	status, err := u.Commit()
	if err != nil {
		return fmt.Errorf("error committing perfdata file: %w", err)
	}
	st.LogEventTime("bench_upload", status.UploadID)
	return nil
}

func (st *buildStatus) installBaselineToolchain(goroot, baselineDir string) (baselineCommit string, remoteErr, err error) {
	sp := st.CreateSpan("install_baseline")
	defer func() { sp.Done(err) }()

	commit, err := st.toolchainBaselineCommit()
	if err != nil {
		return "", nil, fmt.Errorf("error finding baseline commit: %w", err)
	}
	fmt.Fprintf(st, "Baseline toolchain %s\n", commit)

	if st.useSnapshotFor(commit) {
		if err := st.writeGoSnapshotTo(commit, baselineDir); err != nil {
			return "", nil, fmt.Errorf("error writing baseline snapshot: %w", err)
		}
		return commit, nil, nil
	}

	if err := st.writeGoSourceTo(st.bc, commit, baselineDir); err != nil {
		return "", nil, fmt.Errorf("error writing baseline source: %w", err)
	}

	br := st.BuilderRev
	br.Rev = commit

	builder := buildgo.GoBuilder{
		Logger:     st,
		BuilderRev: br,
		Conf:       st.conf,
		Goroot:     baselineDir,
		// Use the primary GOROOT as GOROOT_BOOTSTRAP. The
		// typical bootstrap toolchain may not be available if
		// the primary toolchain was installed from a snapshot.
		GorootBootstrap: goroot,
	}
	remoteErr, err = builder.RunMake(st.ctx, st.bc, st)
	if err != nil {
		return "", nil, err
	}
	if remoteErr != nil {
		return "", remoteErr, nil
	}
	return commit, nil, nil
}

func (st *buildStatus) fetchBenchmarksSource(benchmarksDir string) (rev string, remoteErr, err error) {
	if st.SubName == "benchmarks" {
		rev = st.SubRev
	} else {
		rev, err = getRepoHead("benchmarks")
		if err != nil {
			return "", nil, fmt.Errorf("error finding x/benchmarks HEAD: %w", err)
		}
	}

	sp := st.CreateSpan("fetching_benchmarks")
	defer func() { sp.Done(err) }()

	tgz, err := sourcecache.GetSourceTgz(st, "benchmarks", rev)
	if errors.As(err, new(sourcecache.TooBigError)) {
		// Source being too big is a non-retryable error.
		return "", err, nil
	} else if err != nil {
		return "", nil, err
	}

	err = st.bc.PutTar(st.ctx, tgz, benchmarksDir)
	if err != nil {
		return "", nil, err
	}

	return rev, nil, nil
}

func (st *buildStatus) fetchSubrepoAndBaseline(repoDir, baselineDir string) (baselineRev string, remoteErr, err error) {
	st.LogEventTime("fetching_subrepo", st.SubName)

	tgz, err := sourcecache.GetSourceTgz(st, st.SubName, st.SubRev)
	if errors.As(err, new(sourcecache.TooBigError)) {
		// Source being too big is a non-retryable error.
		return "", err, nil
	} else if err != nil {
		return "", nil, err
	}

	err = st.bc.PutTar(st.ctx, tgz, repoDir)
	if err != nil {
		return "", nil, err
	}

	baselineRev, err = st.subrepoBaselineCommit()
	if err != nil {
		return "", nil, err
	}

	fmt.Fprintf(st, "Baseline subrepo %s\n", baselineRev)

	tgz, err = sourcecache.GetSourceTgz(st, st.SubName, baselineRev)
	if errors.As(err, new(sourcecache.TooBigError)) {
		// Source being too big is a non-retryable error.
		return "", err, nil
	} else if err != nil {
		return "", nil, err
	}

	err = st.bc.PutTar(st.ctx, tgz, baselineDir)
	if err != nil {
		return "", nil, err
	}

	return baselineRev, nil, nil
}

var errBuildletsGone = errors.New("runTests: dist test failed: all buildlets had network errors or timeouts, yet tests remain")

// runTests runs tests for the main Go repo.
//
// After runTests completes, the caller must assume that st.bc might be invalid
// (It's possible that only one of the helper buildlets survived).
func (st *buildStatus) runTests(helpers <-chan buildlet.Client) (remoteErr, err error) {
	testNames, remoteErr, err := st.distTestList()
	if remoteErr != nil {
		return fmt.Errorf("distTestList remote: %v", remoteErr), nil
	}
	if err != nil {
		return nil, fmt.Errorf("distTestList exec: %v", err)
	}
	testStats := getTestStats(st)

	set, err := st.newTestSet(testStats, testNames)
	if err != nil {
		return nil, err
	}
	st.LogEventTime("starting_tests", fmt.Sprintf("%d tests", len(set.items)))
	startTime := time.Now()

	workDir, err := st.bc.WorkDir(st.ctx)
	if err != nil {
		return nil, fmt.Errorf("error discovering workdir for main buildlet, %s: %v", st.bc.Name(), err)
	}

	mainBuildletGoroot := st.conf.FilePathJoin(workDir, "go")
	mainBuildletGopath := st.conf.FilePathJoin(workDir, "gopath")

	// We use our original buildlet to run the tests in order, to
	// make the streaming somewhat smooth and not incredibly
	// lumpy.  The rest of the buildlets run the largest tests
	// first (critical path scheduling).
	// The buildletActivity WaitGroup is used to track when all
	// the buildlets are dead or done.
	var buildletActivity sync.WaitGroup
	buildletActivity.Add(2) // one per goroutine below (main + helper launcher goroutine)
	go func() {
		defer buildletActivity.Done() // for the per-goroutine Add(2) above
		for !st.bc.IsBroken() {
			tis, ok := set.testsToRunInOrder()
			if !ok {
				select {
				case <-st.ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				continue
			}
			st.runTestsOnBuildlet(st.bc, tis, mainBuildletGoroot, mainBuildletGopath)
		}
		st.LogEventTime("main_buildlet_broken", st.bc.Name())
	}()
	go func() {
		defer buildletActivity.Done() // for the per-goroutine Add(2) above
		for helper := range helpers {
			buildletActivity.Add(1)
			go func(bc buildlet.Client) {
				defer buildletActivity.Done() // for the per-helper Add(1) above
				defer st.LogEventTime("closed_helper", bc.Name())
				defer bc.Close()
				if devPause {
					defer time.Sleep(5 * time.Minute)
					defer st.LogEventTime("DEV_HELPER_SLEEP", bc.Name())
				}
				st.LogEventTime("got_empty_test_helper", bc.String())
				if err := bc.PutTarFromURL(st.ctx, st.SnapshotURL(pool.NewGCEConfiguration().BuildEnv()), "go"); err != nil {
					log.Printf("failed to extract snapshot for helper %s: %v", bc.Name(), err)
					return
				}
				workDir, err := bc.WorkDir(st.ctx)
				if err != nil {
					log.Printf("error discovering workdir for helper %s: %v", bc.Name(), err)
					return
				}
				st.LogEventTime("test_helper_set_up", bc.Name())
				goroot := st.conf.FilePathJoin(workDir, "go")
				gopath := st.conf.FilePathJoin(workDir, "gopath")
				for !bc.IsBroken() {
					tis, ok := set.testsToRunBiggestFirst()
					if !ok {
						st.LogEventTime("no_new_tests_remain", bc.Name())
						return
					}
					st.runTestsOnBuildlet(bc, tis, goroot, gopath)
				}
				st.LogEventTime("test_helper_is_broken", bc.Name())
			}(helper)
		}
	}()

	// Convert a sync.WaitGroup into a channel.
	// Aside: https://groups.google.com/forum/#!topic/golang-dev/7fjGWuImu5k
	buildletsGone := make(chan struct{})
	go func() {
		buildletActivity.Wait()
		close(buildletsGone)
	}()

	var lastMetadata string
	var lastHeader string
	var serialDuration time.Duration
	for _, ti := range set.items {
	AwaitDone:
		for {
			timer := time.NewTimer(30 * time.Second)
			select {
			case <-ti.done: // wait for success
				timer.Stop()
				break AwaitDone
			case <-timer.C:
				st.LogEventTime("still_waiting_on_test", ti.name.Old)
			case <-buildletsGone:
				set.cancelAll()
				return nil, errBuildletsGone
			}
		}

		serialDuration += ti.execDuration
		if len(ti.output) > 0 {
			metadata, header, out := parseOutputAndHeader(ti.output)
			printHeader := false
			if metadata != lastMetadata {
				lastMetadata = metadata
				fmt.Fprintf(st, "\n%s\n", metadata)
				// Always include the test header after
				// metadata changes. This is a readability
				// optimization that ensures that tests are
				// always immediately preceded by their test
				// banner, even if it is duplicate banner
				// because the test metadata changed.
				printHeader = true
			}
			if header != lastHeader {
				lastHeader = header
				printHeader = true
			}
			if printHeader {
				fmt.Fprintf(st, "\n%s\n", header)
			}
			if pool.NewGCEConfiguration().InStaging() {
				out = bytes.TrimSuffix(out, nl)
				st.Write(out)
				fmt.Fprintf(st, " (shard %s; par=%d)\n", ti.shardIPPort, ti.groupSize)
			} else {
				st.Write(out)
			}
		}

		if ti.remoteErr != nil {
			set.cancelAll()
			return fmt.Errorf("dist test failed: %s: %v", ti.name, ti.remoteErr), nil
		}
	}
	elapsed := time.Since(startTime)
	var msg string
	if st.conf.NumTestHelpers(st.isTry()) > 0 {
		msg = fmt.Sprintf("took %v; aggregate %v; saved %v", elapsed, serialDuration, serialDuration-elapsed)
	} else {
		msg = fmt.Sprintf("took %v", elapsed)
	}
	st.LogEventTime("tests_complete", msg)
	fmt.Fprintf(st, "\nAll tests passed.\n")
	return nil, nil
}

const (
	banner       = "XXXBANNERXXX:" // flag passed to dist
	bannerPrefix = "\n" + banner   // with the newline added by dist

	metadataBannerPrefix = bannerPrefix + "Test execution environment."

	outputBanner = "##### " // banner to display in output.
)

var (
	bannerPrefixBytes         = []byte(bannerPrefix)
	metadataBannerPrefixBytes = []byte(metadataBannerPrefix)
)

// parseOutputAndHeader parses b and returns the test (optional) environment
// metaadata, display header (e.g., "##### Testing packages.") and the
// following output.
//
// metadata is the optional execution environment metadata block. e.g.,
//
// ##### Test execution environment.
// # GOARCH: amd64
// # CPU: Intel(R) Xeon(R) W-2135 CPU @ 3.70GHz
func parseOutputAndHeader(b []byte) (metadata, header string, out []byte) {
	if !bytes.HasPrefix(b, bannerPrefixBytes) {
		return "", "", b
	}

	if bytes.HasPrefix(b, metadataBannerPrefixBytes) {
		// Header includes everything up to and including the next
		// banner.
		rem := b[len(metadataBannerPrefixBytes):]
		i := bytes.Index(rem, bannerPrefixBytes)
		if i == -1 {
			// Metadata block without a following block doesn't
			// make sense. Bail.
			return "", "", b
		}
		bi := i + len(metadataBannerPrefixBytes)
		// Metadata portion of header, skipping initial and trailing newlines.
		metadata = strings.Trim(string(b[:bi]), "\n")
		metadata = strings.Replace(metadata, banner, outputBanner, 1)
		b = b[bi+1:] // skip newline at start of next banner.
	} else {
		b = b[1:] // skip newline
	}

	// Find end of primary test banner.
	nl := bytes.IndexByte(b, '\n')
	if nl == -1 {
		// No newline, everything is header.
		header = string(b)
		b = nil
	} else {
		header = string(b[:nl])
		b = b[nl+1:]
	}

	// Replace internal marker banner with the human-friendly version.
	header = strings.Replace(header, banner, outputBanner, 1)
	return metadata, header, b
}

// maxTestExecError is the number of test execution failures at which
// we give up and stop trying and instead permanently fail the test.
// Note that this is not related to whether the test failed remotely,
// but whether we were unable to start or complete watching it run.
// (A communication error)
const maxTestExecErrors = 3

// runTestsOnBuildlet runs tis on bc, using the optional goroot & gopath environment variables.
func (st *buildStatus) runTestsOnBuildlet(bc buildlet.Client, tis []*testItem, goroot, gopath string) {
	names, rawNames := make([]string, len(tis)), make([]string, len(tis))
	for i, ti := range tis {
		names[i], rawNames[i] = ti.name.Old, ti.name.Raw
		if i > 0 && (!strings.HasPrefix(ti.name.Old, "go_test:") || !strings.HasPrefix(names[0], "go_test:")) {
			panic("only go_test:* tests may be merged")
		}
	}
	var spanName string
	var detail string
	if len(names) == 1 {
		spanName = "run_test:" + names[0]
		detail = bc.Name()
	} else {
		spanName = "run_tests_multi"
		detail = fmt.Sprintf("%s: %v", bc.Name(), names)
	}
	sp := st.CreateSpan(spanName, detail)

	args := []string{"tool", "dist", "test", "--no-rebuild", "--banner=" + banner}
	if st.conf.IsRace() {
		args = append(args, "--race")
	}
	if st.conf.CompileOnly {
		args = append(args, "--compile-only")
	}
	if st.useKeepGoingFlag() {
		args = append(args, "-k")
	}
	args = append(args, rawNames...)
	var buf bytes.Buffer
	t0 := time.Now()
	timeout := st.conf.DistTestsExecTimeout(names)

	ctx, cancel := context.WithTimeout(st.ctx, timeout)
	defer cancel()

	env := append(st.conf.Env(),
		"GOROOT="+goroot,
		"GOPATH="+gopath,
	)
	env = append(env, st.modulesEnv()...)

	remoteErr, err := bc.Exec(ctx, "./go/bin/go", buildlet.ExecOpts{
		// We set Dir to "." instead of the default ("go/bin") so when the dist tests
		// try to run os/exec.Command("go", "test", ...), the LookPath of "go" doesn't
		// return "./go.exe" (which exists in the current directory: "go/bin") and then
		// fail when dist tries to run the binary in dir "$GOROOT/src", since
		// "$GOROOT/src" + "./go.exe" doesn't exist. Perhaps LookPath should return
		// an absolute path.
		Dir:      ".",
		Output:   &buf, // see "maybe stream lines" TODO below
		ExtraEnv: env,
		Path:     []string{st.conf.FilePathJoin("$WORKDIR", "go", "bin"), "$PATH"},
		Args:     args,
	})
	execDuration := time.Since(t0)
	sp.Done(err)
	if err != nil {
		bc.MarkBroken() // prevents reuse
		for _, ti := range tis {
			ti.numFail++
			st.logf("Execution error running %s on %s: %v (numFails = %d)", ti.name, bc, err, ti.numFail)
			if err == buildlet.ErrTimeout {
				ti.failf("Test %q ran over %v limit (%v); saw output:\n%s", ti.name, timeout, execDuration, buf.Bytes())
			} else if ti.numFail >= maxTestExecErrors {
				ti.failf("Failed to schedule %q test after %d tries.\n", ti.name, maxTestExecErrors)
			} else {
				ti.retry()
			}
		}
		return
	}

	out := buf.Bytes()
	out = bytes.Replace(out, []byte("\nALL TESTS PASSED (some were excluded)\n"), nil, 1)
	out = bytes.Replace(out, []byte("\nALL TESTS PASSED\n"), nil, 1)

	for _, ti := range tis {
		ti.output = out
		ti.remoteErr = remoteErr
		ti.execDuration = execDuration
		ti.groupSize = len(tis)
		ti.shardIPPort = bc.IPPort()
		close(ti.done)

		// After the first one, make the rest succeed with no output.
		// TODO: maybe stream lines (set Output to a line-reading
		// Writer instead of &buf). for now we just wait for them in
		// ~10 second batches.  Doesn't look as smooth on the output,
		// though.
		out = nil
		remoteErr = nil
		execDuration = 0
	}
}

func (st *buildStatus) CreateSpan(event string, optText ...string) spanlog.Span {
	return schedule.CreateSpan(st, event, optText...)
}

func (st *buildStatus) LogEventTime(event string, optText ...string) {
	if len(optText) > 1 {
		panic("usage")
	}
	if pool.NewGCEConfiguration().InStaging() {
		st.logf("%s %v", event, optText)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	var text string
	if len(optText) > 0 {
		text = optText[0]
	}
	st.events = append(st.events, eventAndTime{
		t:    time.Now(),
		evt:  event,
		text: text,
	})
}

func (st *buildStatus) hasEvent(event string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, e := range st.events {
		if e.evt == event {
			return true
		}
	}
	return false
}

// HTMLStatusLine returns the HTML to show within the <pre> block on
// the main page's list of active builds.
func (st *buildStatus) HTMLStatusLine() template.HTML      { return st.htmlStatus(singleLine) }
func (st *buildStatus) HTMLStatusTruncated() template.HTML { return st.htmlStatus(truncated) }
func (st *buildStatus) HTMLStatus() template.HTML          { return st.htmlStatus(full) }

func strSliceTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

type buildStatusDetail int

const (
	singleLine buildStatusDetail = iota
	truncated
	full
)

func (st *buildStatus) htmlStatus(detail buildStatusDetail) template.HTML {
	if st == nil {
		return "[nil]"
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	urlPrefix := "https://go-review.googlesource.com/#/q/"

	if st.Rev == "" {
		log.Printf("warning: st.Rev is empty")
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "<a href='https://go.dev/wiki/DashboardBuilders'>%s</a> rev <a href='%s%s'>%s</a>",
		st.Name, urlPrefix, st.Rev, strSliceTo(st.Rev, 8))
	if st.IsSubrepo() {
		if st.SubRev == "" {
			log.Printf("warning: st.SubRev is empty on subrepo")
		}
		fmt.Fprintf(&buf, " (sub-repo %s rev <a href='%s%s'>%s</a>)",
			st.SubName, urlPrefix, st.SubRev, strSliceTo(st.SubRev, 8))
	}
	if ts := st.trySet; ts != nil {
		if ts.ChangeID == "" {
			log.Printf("warning: ts.ChangeID is empty")
		}
		fmt.Fprintf(&buf, " (<a href='/try?commit=%v'>trybot set</a> for <a href='https://go-review.googlesource.com/#/q/%s'>%s</a>)",
			strSliceTo(ts.Commit, 8),
			ts.ChangeTriple(), strSliceTo(ts.ChangeID, 8))
	}

	var state string
	if st.canceled {
		state = "canceled"
	} else if st.done.IsZero() {
		if st.HasBuildlet() {
			state = "running"
		} else {
			state = "waiting_for_machine"
		}
	} else if st.succeeded {
		state = "succeeded"
	} else {
		state = "<font color='#700000'>failed</font>"
	}
	if detail > singleLine && st.bc != nil {
		fmt.Fprintf(&buf, "; <a href='%s'>%s</a>; %s", html.EscapeString(st.logsURLLocked()), state, html.EscapeString(st.bc.String()))
	} else {
		fmt.Fprintf(&buf, "; <a href='%s'>%s</a>", html.EscapeString(st.logsURLLocked()), state)
	}

	t := st.done
	if t.IsZero() {
		t = st.startTime
	}
	fmt.Fprintf(&buf, ", %v ago", time.Since(t).Round(time.Second))
	if detail > singleLine {
		buf.WriteByte('\n')
		lastLines := 0
		if detail == truncated {
			lastLines = 3
		}
		st.writeEventsLocked(&buf, true, lastLines)
	}
	return template.HTML(buf.String())
}

func (st *buildStatus) logsURLLocked() string {
	if st.logURL != "" {
		return st.logURL
	}
	var urlPrefix string
	if pool.NewGCEConfiguration().BuildEnv() == buildenv.Production {
		urlPrefix = "https://farmer.golang.org"
	} else {
		urlPrefix = "http://" + pool.NewGCEConfiguration().BuildEnv().StaticIP
	}
	if *mode == "dev" {
		urlPrefix = "https://localhost:8119"
	}
	u := fmt.Sprintf("%v/temporarylogs?name=%s&rev=%s&st=%p", urlPrefix, st.Name, st.Rev, st)
	if st.IsSubrepo() {
		u += fmt.Sprintf("&subName=%v&subRev=%v", st.SubName, st.SubRev)
	}
	return u
}

// st.mu must be held.
// If numLines is greater than zero, it's the number of final lines to truncate to.
func (st *buildStatus) writeEventsLocked(w io.Writer, htmlMode bool, numLines int) {
	startAt := 0
	if numLines > 0 {
		startAt = len(st.events) - numLines
		if startAt > 0 {
			io.WriteString(w, "...\n")
		} else {
			startAt = 0
		}
	}

	for i := startAt; i < len(st.events); i++ {
		evt := st.events[i]
		e := evt.evt
		text := evt.text
		if htmlMode {
			if e == "running_exec" {
				e = fmt.Sprintf("<a href='%s'>%s</a>", html.EscapeString(st.logsURLLocked()), e)
			}
			e = "<b>" + e + "</b>"
			text = "<i>" + html.EscapeString(text) + "</i>"
		}
		fmt.Fprintf(w, "  %v %s %s\n", evt.t.Format(time.RFC3339), e, text)
	}
	if st.isRunningLocked() && len(st.events) > 0 {
		lastEvt := st.events[len(st.events)-1]
		fmt.Fprintf(w, " %7s (now)\n", fmt.Sprintf("+%0.1fs", time.Since(lastEvt.t).Seconds()))
	}
}

func (st *buildStatus) logs() string {
	return st.output.String()
}

func (st *buildStatus) Write(p []byte) (n int, err error) {
	return st.output.Write(p)
}

// repeatedCommunicationError takes a buildlet execution error (a
// network/communication error, as opposed to a remote execution that
// ran and had a non-zero exit status and we heard about) and
// conditionally promotes it to a terminal error. If this returns a
// non-nil value, the execErr should be considered terminal with the
// returned error.
func (st *buildStatus) repeatedCommunicationError(execErr error) error {
	if execErr == nil {
		return nil
	}
	// For now, only do this for plan9, which is flaky (Issue 31261),
	// but not for plan9-arm (Issue 52677)
	if strings.HasPrefix(st.Name, "plan9-") && st.Name != "plan9-arm" && execErr == errBuildletsGone {
		// TODO: give it two tries at least later (store state
		// somewhere; global map?). But for now we're going to
		// only give it one try.
		return fmt.Errorf("network error promoted to terminal error: %v", execErr)
	}
	return nil
}

// commitTime returns the greater of Rev and SubRev's commit times.
func (st *buildStatus) commitTime() time.Time {
	if st.RevCommitTime.Before(st.SubRevCommitTime) {
		return st.SubRevCommitTime
	}
	return st.RevCommitTime
}

// branch returns branch for either Rev, or SubRev if it exists.
func (st *buildStatus) branch() string {
	if st.SubRev != "" {
		return st.SubRevBranch
	}
	return st.RevBranch
}
