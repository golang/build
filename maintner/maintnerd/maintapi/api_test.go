// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintapi

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

func TestGetRef(t *testing.T) {
	c := getGoData(t)
	s := apiService{c: c}
	req := &apipb.GetRefRequest{
		GerritServer:  "go.googlesource.com",
		GerritProject: "go",
		Ref:           "refs/heads/master",
	}
	res, err := s.GetRef(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Value) != 40 {
		t.Errorf("go master ref = %q; want length 40 string", res.Value)
	}

	// Bogus ref
	req.Ref = "NOT EXIST REF"
	res, err = s.GetRef(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Value) != 0 {
		t.Errorf("go bogus ref = %q; want empty string", res.Value)
	}

	// Bogus project
	req.GerritProject = "NOT EXIST PROJ"
	_, err = s.GetRef(context.Background(), req)
	if got, want := fmt.Sprint(err), "unknown gerrit project"; got != want {
		t.Errorf("error for bogus project = %q; want %q", got, want)
	}
}

var hitGerrit = flag.Bool("hit_gerrit", false, "query production Gerrit in TestFindTryWork")

func TestFindTryWork(t *testing.T) {
	if !*hitGerrit {
		t.Skip("skipping without flag -hit_gerrit")
	}
	c := getGoData(t)
	s := apiService{c: c}
	req := &apipb.GoFindTryWorkRequest{}
	t0 := time.Now()
	res, err := s.GoFindTryWork(context.Background(), req)
	d0 := time.Since(t0)
	if err != nil {
		t.Fatal(err)
	}

	// Just for interactive debugging. This is using live data.
	// The stable tests are in TestTryWorkItem and TestTryBotStatus.
	t.Logf("Current:\n%v", prototext.Format(res))

	t1 := time.Now()
	res2, err := s.GoFindTryWork(context.Background(), req)
	d1 := time.Since(t1)
	t.Logf("Latency: %v, then %v", d0, d1)
	t.Logf("Cached: equal=%v, err=%v", proto.Equal(res, res2), err)
}

func TestTryBotStatus(t *testing.T) {
	c := getGoData(t)
	tests := []struct {
		proj      string
		clnum     int32
		msgCutoff int
		wantTry   bool
		wantDone  bool
	}{
		{"go", 51430, 1, true, false},
		{"go", 51430, 2, true, false},
		{"go", 51430, 3, true, true},

		{"build", 48968, 5, true, false},  // adding trybot (coordinator ignores for "build" repo)
		{"build", 48968, 6, false, false}, // removing it
	}
	for _, tt := range tests {
		cl := c.Gerrit().Project("go.googlesource.com", tt.proj).CL(tt.clnum)
		if cl == nil {
			t.Errorf("CL %d in %s not found", tt.clnum, tt.proj)
			continue
		}
		old := *cl // save before mutations
		cl.Version = cl.Messages[tt.msgCutoff-1].Version
		cl.Messages = cl.Messages[:tt.msgCutoff]
		gotTry, gotDone := tryBotStatus(cl, false /* not staging */)
		if gotTry != tt.wantTry || gotDone != tt.wantDone {
			t.Errorf("tryBotStatus(%q, %d) after %d messages = try/done %v, %v; want %v, %v",
				tt.proj, tt.clnum, tt.msgCutoff, gotTry, gotDone, tt.wantTry, tt.wantDone)
			for _, msg := range cl.Messages {
				t.Logf("  msg ver=%d, text=%q", msg.Version, msg.Message)
			}
		}
		*cl = old // restore
	}
}

func TestTryWorkItem(t *testing.T) {
	c := getGoData(t)
	goProj := gerritProject{
		refs: []refHash{
			{"refs/heads/master", gitHash("9995c6b50aa55c1cc1236d1d688929df512dad53")},
			{"refs/heads/release-branch.go1.16", gitHash("e67a58b7cb2b228e04477dfdb1aacd8348e63534")},
			{"refs/heads/release-branch.go1.15", gitHash("72ccabc99449b2cb5bb1438eb90244d55f7b02f5")},
		},
	}
	develVersion := apipb.MajorMinor{
		Major: 1, Minor: 17,
	}
	supportedReleases := []*apipb.GoRelease{
		{
			Major: 1, Minor: 16, Patch: 3,
			TagName:      "go1.16.3",
			TagCommit:    "9baddd3f21230c55f0ad2a10f5f20579dcf0a0bb",
			BranchName:   "release-branch.go1.16",
			BranchCommit: "e67a58b7cb2b228e04477dfdb1aacd8348e63534",
		},
		{
			Major: 1, Minor: 15, Patch: 11,
			TagName:      "go1.15.11",
			TagCommit:    "8c163e85267d146274f68854fe02b4a495586584",
			BranchName:   "release-branch.go1.15",
			BranchCommit: "72ccabc99449b2cb5bb1438eb90244d55f7b02f5",
		},
	}
	tests := []struct {
		proj     string
		clnum    int32
		ci       *gerrit.ChangeInfo
		comments map[string][]gerrit.CommentInfo
		want     *apipb.GerritTryWorkItem
	}{
		// Same Change-Id, different branch:
		{"go", 51430, &gerrit.ChangeInfo{}, nil, &apipb.GerritTryWorkItem{
			Project:     "go",
			Branch:      "master",
			ChangeId:    "I0bcae339624e7d61037d9ea0885b7bd07491bbb6",
			Commit:      "45a4609c0ae214e448612e0bc0846e2f2682f1b2",
			AuthorEmail: "bradfitz@golang.org",
			GoVersion:   []*apipb.MajorMinor{{Major: 1, Minor: 17}},
		}},
		{"go", 51450, &gerrit.ChangeInfo{}, nil, &apipb.GerritTryWorkItem{
			Project:     "go",
			Branch:      "release-branch.go1.9",
			ChangeId:    "I0bcae339624e7d61037d9ea0885b7bd07491bbb6",
			Commit:      "7320506bc58d3a55eff2c67b2ec65cfa94f7b0a7",
			AuthorEmail: "bradfitz@golang.org",
			GoVersion:   []*apipb.MajorMinor{{Major: 1, Minor: 9}},
		}},
		// Different project: Tested on tip and two supported releases.
		{"build", 51432, &gerrit.ChangeInfo{}, nil, &apipb.GerritTryWorkItem{
			Project:     "build",
			Branch:      "master",
			ChangeId:    "I1f71836da7008e58d3e76e2cc3170e96cd57ddf6",
			Commit:      "9251bc9950baff61d95da0761e2e4bfab61ed210",
			AuthorEmail: "bradfitz@golang.org",
			GoCommit: []string{
				"9995c6b50aa55c1cc1236d1d688929df512dad53",
				"e67a58b7cb2b228e04477dfdb1aacd8348e63534",
				"72ccabc99449b2cb5bb1438eb90244d55f7b02f5",
			},
			GoBranch: []string{"master", "release-branch.go1.16", "release-branch.go1.15"},
			GoVersion: []*apipb.MajorMinor{
				{Major: 1, Minor: 17},
				{Major: 1, Minor: 16},
				{Major: 1, Minor: 15},
			},
		}},

		// Test that a golang.org/x repo TryBot on a branch like
		// "internal-branch.go1.N-suffix" tests with Go 1.N (rather than tip + two supported releases).
		// See issues 28891, 42127, and 36882.
		{"net", 314649, &gerrit.ChangeInfo{}, nil, &apipb.GerritTryWorkItem{
			Project:     "net",
			Branch:      "internal-branch.go1.16-vendor",
			ChangeId:    "I2c54ce3b2acf1c5efdea66db0595b93a3f5ae5f3",
			Commit:      "3f4a416c7d3b3b41375d159f71ff0a801fc0102b",
			AuthorEmail: "katie@golang.org",
			GoCommit:    []string{"e67a58b7cb2b228e04477dfdb1aacd8348e63534"},
			GoBranch:    []string{"release-branch.go1.16"},
			GoVersion:   []*apipb.MajorMinor{{Major: 1, Minor: 16}},
		}},

		// Test that TryBots run on branches of the x/ repositories, other than
		// "master" and "release-branch.go1.N". See issue 37512.
		{"tools", 238259, &gerrit.ChangeInfo{}, nil, &apipb.GerritTryWorkItem{
			Project:     "tools",
			Branch:      "dev.go2go",
			ChangeId:    "I24950593b517af011a636966cb98b9652d2c4134",
			Commit:      "76e917206452e73dc28cbeb58a15ea8f30487263",
			AuthorEmail: "rstambler@golang.org",
			GoCommit:    []string{"9995c6b50aa55c1cc1236d1d688929df512dad53"},
			GoBranch:    []string{"master"},
			GoVersion:   []*apipb.MajorMinor{{Major: 1, Minor: 17}},
		}},

		// Test that x/tools TryBots on gopls release branches are
		// tested on tip and two supported releases. See issue 46156.
		{"tools", 316773, &gerrit.ChangeInfo{}, nil, &apipb.GerritTryWorkItem{
			Project:     "tools",
			Branch:      "gopls-release-branch.0.6",
			ChangeId:    "I32fd2c0d30854e61109ebd16a05d5099f9074fe5",
			Commit:      "0bb7e5c47b1a31f85d4f173edc878a8e049764a5",
			AuthorEmail: "rstambler@golang.org",
			GoCommit: []string{
				"9995c6b50aa55c1cc1236d1d688929df512dad53",
				"e67a58b7cb2b228e04477dfdb1aacd8348e63534",
				"72ccabc99449b2cb5bb1438eb90244d55f7b02f5",
			},
			GoBranch: []string{"master", "release-branch.go1.16", "release-branch.go1.15"},
			GoVersion: []*apipb.MajorMinor{
				{Major: 1, Minor: 17},
				{Major: 1, Minor: 16},
				{Major: 1, Minor: 15},
			},
		}},

		// With comments:
		{
			proj:  "go",
			clnum: 201203,
			ci: &gerrit.ChangeInfo{
				CurrentRevision: "f99d33e72efdea68fce39765bc94479b5ebed0a9",
				Revisions: map[string]gerrit.RevisionInfo{
					"f99d33e72efdea68fce39765bc94479b5ebed0a9": {PatchSetNumber: 88},
				},
				Messages: []gerrit.ChangeMessageInfo{
					{
						Author:         &gerrit.AccountInfo{NumericID: 1234},
						Message:        "Patch Set 1: Run-TryBot+1\n\n(1 comment)",
						Time:           gerrit.TimeStamp(time.Date(2020, 7, 7, 23, 27, 23, 0, time.UTC)),
						RevisionNumber: 1,
					},
					{
						Author:         &gerrit.AccountInfo{NumericID: 5678},
						Message:        "Patch Set 2: Foo-2 Run-TryBot+1\n\n(1 comment)",
						Time:           gerrit.TimeStamp(time.Date(2020, 7, 7, 23, 28, 47, 0, time.UTC)),
						RevisionNumber: 2,
					},
				},
			},
			comments: map[string][]gerrit.CommentInfo{
				"/PATCHSET_LEVEL": {
					{
						PatchSet: 1,
						Message:  "TRY=foo",
						Updated:  gerrit.TimeStamp(time.Date(2020, 7, 7, 23, 27, 23, 0, time.UTC)),
						Author:   &gerrit.AccountInfo{NumericID: 1234},
					},
					{
						PatchSet: 2,
						Message:  "A preceding sentence.\nTRY=bar, baz\nA following sentence.",
						Updated:  gerrit.TimeStamp(time.Date(2020, 7, 7, 23, 28, 47, 0, time.UTC)),
						Author:   &gerrit.AccountInfo{NumericID: 5678},
					},
				},
			},
			want: &apipb.GerritTryWorkItem{
				Project:     "go",
				Branch:      "master",
				ChangeId:    "I358eb7b11768df8c80fb7e805abd4cd01d52bb9b",
				Commit:      "f99d33e72efdea68fce39765bc94479b5ebed0a9",
				AuthorEmail: "bradfitz@golang.org",
				Version:     88,
				GoVersion:   []*apipb.MajorMinor{{Major: 1, Minor: 17}},
				TryMessage: []*apipb.TryVoteMessage{
					{Message: "foo", AuthorId: 1234, Version: 1},
					{Message: "bar, baz", AuthorId: 5678, Version: 2},
				},
			},
		},

		// Test that followup TRY= requests on the same patch set are included. See issue 42084.
		{
			proj:  "go",
			clnum: 324763,
			ci: &gerrit.ChangeInfo{
				CurrentRevision: "dd38fd80c3667f891dbe06bd1d8ed153c2e208da",
				Revisions: map[string]gerrit.RevisionInfo{
					"dd38fd80c3667f891dbe06bd1d8ed153c2e208da": {PatchSetNumber: 1},
				},
				Messages: []gerrit.ChangeMessageInfo{
					{
						Author:         &gerrit.AccountInfo{NumericID: 1234},
						Message:        "Patch Set 1: Run-TryBot+1 Trust+1\n\n(1 comment)",
						Time:           gerrit.TimeStamp(time.Date(2021, 6, 3, 18, 58, 0, 0, time.UTC)),
						RevisionNumber: 1,
					},
					{
						Author:         &gerrit.AccountInfo{NumericID: 1234},
						Message:        "Patch Set 1: Run-TryBot+1\n\n(1 comment)",
						Time:           gerrit.TimeStamp(time.Date(2021, 6, 3, 19, 16, 26, 0, time.UTC)),
						RevisionNumber: 1,
					},
				},
			},
			comments: map[string][]gerrit.CommentInfo{
				"/PATCHSET_LEVEL": {
					{
						PatchSet: 1,
						Message:  "TRY=windows-arm64,windows-amd64",
						Updated:  gerrit.TimeStamp(time.Date(2021, 6, 3, 18, 58, 0, 0, time.UTC)),
						Author:   &gerrit.AccountInfo{NumericID: 1234},
					},
					{
						PatchSet: 1,
						Message:  "TRY=windows-arm64-10",
						Updated:  gerrit.TimeStamp(time.Date(2021, 6, 3, 19, 16, 26, 0, time.UTC)),
						Author:   &gerrit.AccountInfo{NumericID: 1234},
					},
				},
			},
			want: &apipb.GerritTryWorkItem{
				Project:     "go",
				Branch:      "master",
				ChangeId:    "I023d5208374f867552ba68b45011f7990159868f",
				Commit:      "dd38fd80c3667f891dbe06bd1d8ed153c2e208da",
				AuthorEmail: "thanm@google.com",
				Version:     1,
				GoVersion:   []*apipb.MajorMinor{{Major: 1, Minor: 17}},
				TryMessage: []*apipb.TryVoteMessage{
					{Message: "windows-arm64,windows-amd64", AuthorId: 1234, Version: 1},
					{Message: "windows-arm64-10", AuthorId: 1234, Version: 1},
				},
			},
		},

		// Test that TRY= request messages with an older patchset-level comment are included.
		// See https://go-review.googlesource.com/c/go/+/493535/comments/c72580be_773332cb where
		// a Run-TryBot+1 request is posted on PS 2 with a patchset-level comment left on PS 1.
		{
			proj:  "go",
			clnum: 493535,
			ci: &gerrit.ChangeInfo{
				CurrentRevision: "f8aa751e53d7019eb1114da68754c77cc0830163",
				Revisions: map[string]gerrit.RevisionInfo{
					"a2afb09fc37fcff8ff43d895def78274d6ec4d74": {PatchSetNumber: 1},
					"f8aa751e53d7019eb1114da68754c77cc0830163": {PatchSetNumber: 2},
				},
				Messages: []gerrit.ChangeMessageInfo{
					// A message posted a minute after PS 2 was uploaded.
					{
						Author:         &gerrit.AccountInfo{NumericID: 1234},
						Message:        "Patch Set 2: Code-Review+2 Run-TryBot+1\n\n(1 comment)",
						Time:           gerrit.TimeStamp(time.Date(2023, 5, 8, 16, 14, 3, 0, time.UTC)),
						RevisionNumber: 2,
					},
				},
			},
			comments: map[string][]gerrit.CommentInfo{
				"/PATCHSET_LEVEL": {
					// Its patchset-level comment is associated with PS 1.
					{
						PatchSet: 1,
						Message:  "TRY\u003dplan9\n\nThanks!",
						Updated:  gerrit.TimeStamp(time.Date(2023, 5, 8, 16, 14, 3, 0, time.UTC)),
						Author:   &gerrit.AccountInfo{NumericID: 1234},
					},
				},
			},
			want: &apipb.GerritTryWorkItem{
				Project:     "go",
				Branch:      "master",
				ChangeId:    "Ia30f51307cc6d07a7e3ada6bf9d60bf9951982ff",
				Commit:      "f8aa751e53d7019eb1114da68754c77cc0830163",
				AuthorEmail: "millerresearch@gmail.com",
				Version:     2,
				GoVersion:   []*apipb.MajorMinor{{Major: 1, Minor: 17}},
				TryMessage: []*apipb.TryVoteMessage{
					{Message: "plan9", AuthorId: 1234, Version: 2},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(int(tt.clnum)), func(t *testing.T) {
			cl := c.Gerrit().Project("go.googlesource.com", tt.proj).CL(tt.clnum)
			if cl == nil {
				t.Fatalf("CL %d in %s not found", tt.clnum, tt.proj)
			}
			work, err := tryWorkItem(cl, tt.ci, tt.comments, goProj, develVersion, supportedReleases)
			if err != nil {
				t.Fatalf("tryWorkItem(%q, %v, ...): err=%v", tt.proj, tt.clnum, err)
			}
			if len(work.GoVersion) == 0 {
				t.Errorf("tryWorkItem(%q, %v, ...): len(GoVersion) is zero, want at least one", tt.proj, tt.clnum)
			}
			if work.Project != "go" && (len(work.GoCommit) == 0 || len(work.GoBranch) == 0) {
				t.Errorf("tryWorkItem(%q, %v, ...): GoCommit/GoBranch slice is empty for x/ repo, want both non-empty", tt.proj, tt.clnum)
			}
			if len(work.GoBranch) != len(work.GoCommit) {
				t.Errorf("tryWorkItem(%q, %v, ...): bad correlation between GoBranch and GoCommit slices", tt.proj, tt.clnum)
			}
			if ok := len(work.GoVersion) == len(work.GoCommit) || (len(work.GoVersion) == 1 && len(work.GoCommit) == 0); !ok {
				t.Errorf("tryWorkItem(%q, %v, ...): bad correlation between GoVersion and GoCommit slices", tt.proj, tt.clnum)
			}
			if diff := cmp.Diff(tt.want, work, protocmp.Transform()); diff != "" {
				t.Errorf("tryWorkItem(%q, %v, ...) mismatch (-want +got):\n%s", tt.proj, tt.clnum, diff)
			}
		})
	}
}

func TestParseInternalBranchVersion(t *testing.T) {
	tests := []struct {
		name    string
		wantMaj int32
		wantMin int32
		wantOK  bool
	}{
		{"internal-branch.go1.16-vendor", 1, 16, true},
		{"internal-branch.go1.16-", 0, 0, false}, // Empty suffix is rejected.
		{"internal-branch.go1.16", 0, 0, false},  // No suffix is rejected.
		{"not-internal-branch", 0, 0, false},
		{"internal-branch.go1.16.2", 0, 0, false},
		{"internal-branch.go42-suffix", 42, 0, true}, // Be ready in case Go 42 is released after 7.5 million years.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maj, min, ok := parseInternalBranchVersion(tt.name)
			if ok != tt.wantOK || maj != tt.wantMaj || min != tt.wantMin {
				t.Errorf("parseInternalBranchVersion(%q) = Go %v.%v ok=%v; want Go %v.%v ok=%v", tt.name,
					maj, min, ok, tt.wantMaj, tt.wantMin, tt.wantOK)
			}
		})
	}
}

var (
	corpusMu    sync.Mutex
	corpusCache *maintner.Corpus
)

func getGoData(tb testing.TB) *maintner.Corpus {
	if testing.Short() {
		tb.Skip("skipping test requiring large download in short mode")
	}
	corpusMu.Lock()
	defer corpusMu.Unlock()
	if corpusCache != nil {
		return corpusCache
	}
	var err error
	corpusCache, err = godata.Get(context.Background())
	if err != nil {
		tb.Fatalf("getting corpus: %v", err)
	}
	return corpusCache
}

func TestSupportedGoReleases(t *testing.T) {
	tests := []struct {
		goProj nonChangeRefLister
		want   []*apipb.GoRelease
	}{
		// A sample of real data from maintner.
		{
			goProj: gerritProject{
				refs: []refHash{
					{"HEAD", gitHash("5168fcf63f5001b38f9ac64ce5c5e3c2d397363d")},
					{"refs/heads/dev.boringcrypto", gitHash("13bf5b80e8d8841a2a3c9b0d5dec65a0c8636253")},
					{"refs/heads/dev.boringcrypto.go1.10", gitHash("2e2a04a605b6c3fc6e733810bdcd0200d8ed25a8")},
					{"refs/heads/dev.boringcrypto.go1.11", gitHash("685dc1638240af70c86a146b0ddb86d51d64f269")},
					{"refs/heads/dev.typealias", gitHash("8a5ef1501dee0715093e87cdc1c9b6becb81c882")},
					{"refs/heads/master", gitHash("5168fcf63f5001b38f9ac64ce5c5e3c2d397363d")},
					{"refs/heads/release-branch.go1", gitHash("08b97d4061dd75ceec1d44e4335183cd791c9306")},
					{"refs/heads/release-branch.go1.1", gitHash("1d6d8fca241bb611af51e265c1b5a2e9ae904702")},
					{"refs/heads/release-branch.go1.10", gitHash("e97b7d68f107ff60152f5bd5701e0286f221ee93")},
					{"refs/heads/release-branch.go1.11", gitHash("97781d2ed116d2cd9cb870d0b84fc0ec598c9abc")},
					{"refs/heads/release-branch.go1.10-security", gitHash("25ca8f49c3fc4a68daff7a23ab613e3453be5cda")},
					{"refs/heads/release-branch.go1.11-security", gitHash("90c896448691b5edb0ab11110f37234f63cd28ed")},
					{"refs/heads/release-branch.go1.2", gitHash("43d00b0942c1c6f43993ac71e1eea48e62e22b8d")},
					{"refs/heads/release-branch.r59", gitHash("5d9765785dff74784bbdad43f7847b6825509032")},
					{"refs/heads/release-branch.r60", gitHash("394b383a1ee0ac3fec5e453a7dbe590d3ce6d6b0")},
					{"refs/notes/review", gitHash("c46ab9dacb2ac618d86f1c1f719bc2de46010e86")},
					{"refs/tags/1.10beta1.mailed", gitHash("2df74db61620771e4f878c9e1db7aeecc00808ba")},
					{"refs/tags/andybons/blog.mailed", gitHash("707a89416af909a3af6c26df93995bc17bf9ce81")},
					{"refs/tags/go1", gitHash("6174b5e21e73714c63061e66efdbe180e1c5491d")},
					{"refs/tags/go1.0.1", gitHash("2fffba7fe19690e038314d17a117d6b87979c89f")},
					{"refs/tags/go1.0.2", gitHash("cb6c6570b73a1c4d19cad94570ed277f7dae55ac")},
					{"refs/tags/go1.0.3", gitHash("30be9b4313622c2077539e68826194cb1028c691")},
					{"refs/tags/go1.1", gitHash("205f850ceacfc39d1e9d76a9569416284594ce8c")},
					{"refs/tags/go1.10", gitHash("bf86aec25972f3a100c3aa58a6abcbcc35bdea49")},
					{"refs/tags/go1.10.1", gitHash("ac7c0ee26dda18076d5f6c151d8f920b43340ae3")},
					{"refs/tags/go1.10.2", gitHash("71bdbf431b79dff61944f22c25c7e085ccfc25d5")},
					{"refs/tags/go1.10.3", gitHash("fe8a0d12b14108cbe2408b417afcaab722b0727c")},
					{"refs/tags/go1.10.4", gitHash("2191fce26a7fd1cd5b4975e7bd44ab44b1d9dd78")},
					{"refs/tags/go1.10beta1", gitHash("9ce6b5c2ed5d3d5251b9a6a0c548d5fb2c8567e8")},
					{"refs/tags/go1.10beta2", gitHash("594668a5a96267a46282ce3007a584ec07adf705")},
					{"refs/tags/go1.10rc1", gitHash("5348aed83e39bd1d450d92d7f627e994c2db6ebf")},
					{"refs/tags/go1.10rc2", gitHash("20e228f2fdb44350c858de941dff4aea9f3127b8")},
					{"refs/tags/go1.11", gitHash("41e62b8c49d21659b48a95216e3062032285250f")},
					{"refs/tags/go1.11.1", gitHash("26957168c4c0cdcc7ca4f0b19d0eb19474d224ac")},
					{"refs/tags/go1.11beta1", gitHash("a12c1f26e4cc602dae62ec065a237172a5b8f926")},
					{"refs/tags/go1.11beta2", gitHash("c814ac44c0571f844718f07aa52afa47e37fb1ed")},
					{"refs/tags/go1.11beta3", gitHash("1b870077c896379c066b41657d3c9062097a6943")},
					{"refs/tags/go1.11rc1", gitHash("807e7f2420c683384dc9c6db498808ba1b7aab17")},
					{"refs/tags/go1.11rc2", gitHash("02c0c32960f65d0b9c66ec840c612f5f9623dc51")},
					{"refs/tags/go1.9.7", gitHash("7df09b4a03f9e53334672674ba7983d5e7128646")},
					{"refs/tags/go1.9beta1", gitHash("952ecbe0a27aadd184ca3e2c342beb464d6b1653")},
					{"refs/tags/go1.9beta2", gitHash("eab99a8d548f8ba864647ab171a44f0a5376a6b3")},
					{"refs/tags/go1.9rc1", gitHash("65c6c88a9442b91d8b2fd0230337b1fda4bb6cdf")},
					{"refs/tags/go1.9rc2", gitHash("048c9cfaacb6fe7ac342b0acd8ca8322b6c49508")},
					{"refs/tags/release.r59", gitHash("5d9765785dff74784bbdad43f7847b6825509032")},
					{"refs/tags/release.r60", gitHash("5464bfebe723752dfc09a6dd6b361b8e79db5995")},
					{"refs/tags/release.r60.1", gitHash("4af7136fcf874e212d66c72178a68db969918b25")},
					{"refs/tags/weekly", gitHash("3895b5051df256b442d0b0af50debfffd8d75164")},
					{"refs/tags/weekly.2009-11-10", gitHash("78c47c36b2984058c1bec0bd72e0b127b24fcd44")},
					{"refs/tags/weekly.2009-11-10.1", gitHash("c57054f7b49539ca4ed6533267c1c20c39aaaaa5")},
				},
			},
			want: []*apipb.GoRelease{
				{
					Major: 1, Minor: 11, Patch: 1,
					TagName:      "go1.11.1",
					TagCommit:    "26957168c4c0cdcc7ca4f0b19d0eb19474d224ac",
					BranchName:   "release-branch.go1.11",
					BranchCommit: "97781d2ed116d2cd9cb870d0b84fc0ec598c9abc",
				},
				{
					Major: 1, Minor: 10, Patch: 4,
					TagName:      "go1.10.4",
					TagCommit:    "2191fce26a7fd1cd5b4975e7bd44ab44b1d9dd78",
					BranchName:   "release-branch.go1.10",
					BranchCommit: "e97b7d68f107ff60152f5bd5701e0286f221ee93",
				},
			},
		},

		// Detect and handle a new major version.
		{
			goProj: gerritProject{
				refs: []refHash{
					{"refs/tags/go1.5", gitHash("9b82ca331d1fa30e3428e7914ba780ae7f75a702")},
					{"refs/tags/go1.42.1", gitHash("23982c09ae5ac811d1dd0099e1626596ade61000")},
					{"refs/tags/go1", gitHash("5c503fde0aa534d3259533802052f936c95fa782")},
					{"refs/tags/go2", gitHash("43126518de2eb0dadc0917a593f08637318986bf")},
					{"refs/tags/go1.11.111", gitHash("c59f000d9bb66592ff84a942014afd1a7be4c953")}, // The onesiest release ever!
					{"refs/heads/release-branch.go1", gitHash("b0f2d801c19fc8798ecf67e50364a44dba606fcd")},
					{"refs/heads/release-branch.go1.5", gitHash("a6ae58c93408bcc17758d397eed0ace973de8481")},
					{"refs/heads/release-branch.go1.11", gitHash("f4f148ef7962271ff8ffcebf13400ded535e9957")},
					{"refs/heads/release-branch.go1.42", gitHash("362986e7a4b5edc911ed55324c37106c40abe3fb")},
					{"refs/heads/release-branch.go2", gitHash("cfbe0f14bcbf1e773f8dd9a968c80cf0b9238c59")},
					{"refs/heads/release-branch.go1.2", gitHash("6523e1eb33ef792df04e08462ed332b95311261e")},

					// It doesn't count as a release if there's no corresponding release-branch.go1.43 release branch.
					{"refs/tags/go1.43", gitHash("3aa7f7065ecf717b1dd6512bb7a9f40625fc8cb5")},
				},
			},
			want: []*apipb.GoRelease{
				{
					Major: 2, Minor: 0, Patch: 0,
					TagName:      "go2",
					TagCommit:    "43126518de2eb0dadc0917a593f08637318986bf",
					BranchName:   "release-branch.go2",
					BranchCommit: "cfbe0f14bcbf1e773f8dd9a968c80cf0b9238c59",
				},
				{
					Major: 1, Minor: 42, Patch: 1,
					TagName:      "go1.42.1",
					TagCommit:    "23982c09ae5ac811d1dd0099e1626596ade61000",
					BranchName:   "release-branch.go1.42",
					BranchCommit: "362986e7a4b5edc911ed55324c37106c40abe3fb",
				},
			},
		},
	}
	for i, tt := range tests {
		got, err := supportedGoReleases(tt.goProj)
		if err != nil {
			t.Fatalf("%d: supportedGoReleases: %v", i, err)
		}
		if diff := cmp.Diff(got, tt.want, protocmp.Transform()); diff != "" {
			t.Errorf("%d: supportedGoReleases: (-got +want)\n%s", i, diff)
		}
	}
}

func TestGetDashboard(t *testing.T) {
	c := getGoData(t)
	s := apiService{c: c}

	type check func(t *testing.T, res *apipb.DashboardResponse, resErr error)
	var noError check = func(t *testing.T, res *apipb.DashboardResponse, resErr error) {
		t.Helper()
		if resErr != nil {
			t.Fatalf("GetDashboard: %v", resErr)
		}
	}
	var commitsTruncated check = func(t *testing.T, res *apipb.DashboardResponse, _ error) {
		t.Helper()
		if !res.CommitsTruncated {
			t.Errorf("CommitsTruncated = false; want true")
		}
		if len(res.Commits) == 0 {
			t.Errorf("no commits; expected some commits when expecting CommitsTruncated")
		}

	}
	hasBranch := func(branch string) check {
		return func(t *testing.T, res *apipb.DashboardResponse, _ error) {
			ok := false
			for _, b := range res.Branches {
				if b == branch {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("didn't find expected branch %q; got branches: %q", branch, res.Branches)
			}
		}
	}
	hasRepoHead := func(proj string) check {
		return func(t *testing.T, res *apipb.DashboardResponse, _ error) {
			ok := false
			var got []string
			for _, rh := range res.RepoHeads {
				if rh.GerritProject == proj {
					ok = true
				}
				got = append(got, rh.GerritProject)
			}
			if !ok {
				t.Errorf("didn't find expected repo head %q; got: %q", proj, got)
			}
		}
	}
	var hasThreeReleases check = func(t *testing.T, res *apipb.DashboardResponse, _ error) {
		t.Helper()
		var got []string
		var gotMaster int
		var gotReleaseBranch int
		var uniq = map[string]bool{}
		for _, r := range res.Releases {
			got = append(got, r.BranchName)
			uniq[r.BranchName] = true
			if r.BranchName == "master" {
				gotMaster++
			}
			if strings.HasPrefix(r.BranchName, "release-branch.go") {
				gotReleaseBranch++
			}
		}
		if len(uniq) != 3 {
			t.Errorf("expected 3 Go releases, got: %q", got)
		}
		if gotMaster != 1 {
			t.Errorf("expected 1 Go release to be master, got: %q", got)
		}
		if gotReleaseBranch != 2 {
			t.Errorf("expected 2 Go releases to be release branches, got: %q", got)
		}
	}
	wantRPCError := func(code codes.Code) check {
		return func(t *testing.T, _ *apipb.DashboardResponse, err error) {
			if grpc.Code(err) != code {
				t.Errorf("expected RPC code %v; got %v (err %v)", code, grpc.Code(err), err)
			}
		}
	}
	basicChecks := []check{
		noError,
		commitsTruncated,
		hasBranch("master"),
		hasBranch("release-branch.go1.4"),
		hasBranch("release-branch.go1.13"),
		hasRepoHead("net"),
		hasRepoHead("sys"),
		hasThreeReleases,
	}

	tests := []struct {
		name   string
		req    *apipb.DashboardRequest
		checks []check
	}{
		// Verify that the default view (with no options) works.
		{
			name:   "zero_value",
			req:    &apipb.DashboardRequest{},
			checks: basicChecks,
		},
		// Or with explicit values:
		{
			name: "zero_value_effectively",
			req: &apipb.DashboardRequest{
				Repo:   "go",
				Branch: "master",
			},
			checks: basicChecks,
		},
		// Max commits:
		{
			name: "max_commits",
			req:  &apipb.DashboardRequest{MaxCommits: 1},
			checks: []check{
				noError,
				commitsTruncated,
				func(t *testing.T, res *apipb.DashboardResponse, _ error) {
					if got, want := len(res.Commits), 1; got != want {
						t.Errorf("got %v commits; want %v", got, want)
					}
				},
			},
		},
		// Verify that branch=mixed doesn't return an error at least.
		{
			name: "mixed",
			req:  &apipb.DashboardRequest{Branch: "mixed"},
			checks: []check{
				noError,
				commitsTruncated,
				hasRepoHead("sys"),
				hasThreeReleases,
			},
		},
		// Verify non-Go repos:
		{
			name: "non_go_repo",
			req:  &apipb.DashboardRequest{Repo: "golang.org/x/net"},
			checks: []check{
				noError,
				commitsTruncated,
				func(t *testing.T, res *apipb.DashboardResponse, _ error) {
					for _, c := range res.Commits {
						if c.GoCommitAtTime == "" {
							t.Errorf("response contains commit without GoCommitAtTime")
						}
						if c.GoCommitLatest == "" {
							t.Errorf("response contains commit without GoCommitLatest")
						}
						if t.Failed() {
							return
						}
					}
				},
			},
		},

		// Validate rejection of bad requests:
		{
			name:   "bad-repo",
			req:    &apipb.DashboardRequest{Repo: "NOT_EXIST"},
			checks: []check{wantRPCError(codes.NotFound)},
		},
		{
			name:   "bad-branch",
			req:    &apipb.DashboardRequest{Branch: "NOT_EXIST"},
			checks: []check{wantRPCError(codes.NotFound)},
		},
		{
			name:   "mixed-with-pagination",
			req:    &apipb.DashboardRequest{Branch: "mixed", Page: 5},
			checks: []check{wantRPCError(codes.InvalidArgument)},
		},
		{
			name:   "negative-page",
			req:    &apipb.DashboardRequest{Page: -1},
			checks: []check{wantRPCError(codes.InvalidArgument)},
		},
		{
			name:   "too-big-page",
			req:    &apipb.DashboardRequest{Page: 1e6},
			checks: []check{wantRPCError(codes.InvalidArgument)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := s.GetDashboard(context.Background(), tt.req)
			for _, c := range tt.checks {
				c(t, res, err)
			}
		})
	}
}

type gerritProject struct {
	refs []refHash
}

func (gp gerritProject) Ref(ref string) maintner.GitHash {
	for _, r := range gp.refs {
		if r.Ref == ref {
			return r.Hash
		}
	}
	return ""
}

func (gp gerritProject) ForeachNonChangeRef(fn func(ref string, hash maintner.GitHash) error) error {
	for _, r := range gp.refs {
		err := fn(r.Ref, r.Hash)
		if err != nil {
			return err
		}
	}
	return nil
}

type refHash struct {
	Ref  string
	Hash maintner.GitHash
}

func gitHash(hexa string) maintner.GitHash {
	if len(hexa) != 40 {
		panic(fmt.Errorf("bogus git hash %q", hexa))
	}
	binary, err := hex.DecodeString(hexa)
	if err != nil {
		panic(fmt.Errorf("bogus git hash %q: %v", hexa, err))
	}
	return maintner.GitHash(binary)
}
