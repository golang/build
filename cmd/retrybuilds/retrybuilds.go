// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The retrybuilds command clears build failures from the build.golang.org dashboard
// to force them to be rebuilt.
//
// Valid usage modes:
//
//   retrybuilds -loghash=f45f0eb8
//   retrybuilds -builder=openbsd-amd64
//   retrybuilds -builder=openbsd-amd64 -hash=6fecb7
//   retrybuilds -redo-flaky
//   retrybuilds -redo-flaky -builder=linux-amd64-clang
//   retrybuilds -substr="failed to find foo"
//   retrybuilds -substr="failed to find foo" -builder=linux-amd64-stretch
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/cmd/coordinator/protos"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var (
	masterKeyFile = flag.String("masterkey", filepath.Join(os.Getenv("HOME"), "keys", "gobuilder-master.key"), "path to Go builder master key. If present, the key argument is not necessary")
	keyFile       = flag.String("key", "", "path to key file")
	builder       = flag.String("builder", "", "builder to wipe a result for. Empty means all.")
	hash          = flag.String("hash", "", "Hash to wipe. If empty, all will be wiped.")
	redoFlaky     = flag.Bool("redo-flaky", false, "Reset all flaky builds. If builder is empty, the master key is required.")
	builderPrefix = flag.String("builder-prefix", "https://build.golang.org", "builder URL prefix")
	logHash       = flag.String("loghash", "", "If non-empty, clear the build that failed with this loghash prefix")
	sendMasterKey = flag.Bool("sendmaster", false, "send the master key in request instead of a builder-specific key; allows overriding actions of revoked keys")
	branch        = flag.String("branch", "master", "branch to find flakes from (for use with -redo-flaky)")
	substr        = flag.String("substr", "", "if non-empty, redoes all build failures whose failure logs contain this substring")
	// TODO(golang.org/issue/34744) - remove after gRPC API for ClearResults is deployed
	grpcHost = flag.String("grpc-host", "", "(EXPERIMENTAL) use gRPC for communicating with the API.")
)

type Failure struct {
	Builder string
	Hash    string
	LogURL  string
}

func main() {
	flag.Parse()
	*builderPrefix = strings.TrimSuffix(*builderPrefix, "/")
	cl := client{}
	if *grpcHost != "" {
		tc := &tls.Config{InsecureSkipVerify: strings.HasPrefix(*grpcHost, "localhost:")}
		cc, err := grpc.DialContext(context.Background(), *grpcHost, grpc.WithTransportCredentials(credentials.NewTLS(tc)))
		if err != nil {
			log.Fatalf("grpc.DialContext(_, %q, _) = %v, wanted no error", *grpcHost, err)
		}
		cl.coordinator = protos.NewCoordinatorClient(cc)
	}
	if *logHash != "" {
		substr := "/log/" + *logHash
		for _, f := range failures() {
			if strings.Contains(f.LogURL, substr) {
				cl.wipe(f.Builder, f.Hash)
			}
		}
		return
	}
	if *substr != "" {
		foreachFailure(func(f Failure, failLog string) {
			if strings.Contains(failLog, *substr) {
				log.Printf("Restarting %+v", f)
				cl.wipe(f.Builder, f.Hash)
			}
		})
		return
	}
	if *redoFlaky {
		foreachFailure(func(f Failure, failLog string) {
			if isFlaky(failLog) {
				log.Printf("Restarting flaky %+v", f)
				cl.wipe(f.Builder, f.Hash)
			}
		})
		return
	}
	if *builder == "" {
		log.Fatalf("Missing -builder, -redo-flaky, -substr, or -loghash flag.")
	}
	if *hash == "" {
		for _, f := range failures() {
			if f.Builder != *builder {
				continue
			}
			cl.wipe(f.Builder, f.Hash)
		}
		return
	}
	cl.wipe(*builder, fullHash(*hash))
}

func foreachFailure(fn func(f Failure, failLog string)) {
	gate := make(chan bool, 50)
	var wg sync.WaitGroup
	for _, f := range failures() {
		f := f
		if *builder != "" && f.Builder != *builder {
			continue
		}
		gate <- true
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-gate }()
			res, err := http.Get(f.LogURL)
			if err != nil {
				log.Fatalf("Error fetching %s: %v", f.LogURL, err)
			}
			failLog, err := ioutil.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				log.Fatalf("Error reading %s: %v", f.LogURL, err)
			}
			fn(f, string(failLog))
		}()
	}
	wg.Wait()
}

var flakePhrases = []string{
	"No space left on device",
	"no space left on device", // solaris case apparently
	"fatal error: error in backend: IO failure on output stream",
	"Boffset: unknown state 0",
	"Bseek: unknown state 0",
	"error exporting repository: exit status",
	"remote error: User Is Over Quota",
	"fatal: remote did not send all necessary objects",
	"Failed to schedule \"", // e.g. Failed to schedule "go_test:archive/tar" test after 3 tries.
	"lookup _xmpp-server._tcp.google.com. on 8.8.8.8:53: dial udp 8.8.8.8:53: i/o timeout",
	"lookup _xmpp-server._tcp.google.com on",
	"lookup gmail.com. on 8.8.8.8:53: dial udp 8.8.8.8:53: i/o timeout",
	"lookup gmail.com on 8.8.8.8:53",
	"lookup www.mit.edu on ",
	"undefined: runtime.SetMutexProfileFraction", // ppc64 builders had not-quite-go1.8 bootstrap
	"make.bat: The parameter is incorrect",
	"killed",
	"memory",
	"allocate",
	"Killed",
	"Error running API checker: exit status 1",
	"/compile: exit status 1",
	"cmd/link: exit status 1",
}

func isFlaky(failLog string) bool {
	if strings.Count(strings.TrimSpace(failLog), "\n") < 2 {
		return true
	}
	if strings.HasPrefix(failLog, "exit status ") {
		return true
	}
	if strings.HasPrefix(failLog, "timed out after ") {
		return true
	}
	if strings.HasPrefix(failLog, "Failed to schedule ") {
		return true
	}
	for _, phrase := range flakePhrases {
		if strings.Contains(failLog, phrase) {
			return true
		}
	}
	numLines := strings.Count(failLog, "\n")
	if numLines < 20 && strings.Contains(failLog, "error: exit status") {
		return true
	}
	// e.g. fatal: destination path 'go.tools.TMP' already exists and is not an empty directory.
	// To be fixed in golang.org/issue/9407
	if strings.Contains(failLog, "fatal: destination path '") &&
		strings.Contains(failLog, "' already exists and is not an empty directory.") {
		return true
	}
	return false
}

func fullHash(h string) string {
	if len(h) == 40 {
		return h
	}
	if h != "" {
		for _, f := range failures() {
			if strings.HasPrefix(f.Hash, h) {
				return f.Hash
			}
		}
	}
	log.Fatalf("invalid hash %q; failed to finds its full hash. Not a recent failure?", h)
	panic("unreachable")
}

type client struct {
	coordinator protos.CoordinatorClient
}

// grpcWipe wipes a git hash failure for the provided builder and hash.
// Only the main Go repo is currently supported.
// TODO(golang.org/issue/34744) - replace HTTP wipe with this after gRPC API for ClearResults is deployed
func (c *client) grpcWipe(builder, hash string) {
	md := metadata.New(map[string]string{"authorization": "builder " + builderKey(builder)})
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(context.Background(), md), time.Minute)
		resp, err := c.coordinator.ClearResults(ctx, &protos.ClearResultsRequest{
			Builder: builder,
			Hash:    hash,
		})
		cancel()
		if err != nil {
			s, _ := status.FromError(err)
			switch s.Code() {
			case codes.Aborted:
				log.Printf("Concurrent datastore transaction wiping %v %v: retrying in 1 second", builder, hash)
				time.Sleep(time.Second)
			case codes.DeadlineExceeded:
				log.Printf("Timeout wiping %v %v: retrying", builder, hash)
			default:
				log.Fatalln(err)
			}
			continue
		}
		log.Printf("cl.ClearResults(%q, %q) = %v: resp: %v", builder, hash, status.Code(err), resp)
		return
	}
}

// wipe wipes the git hash failure for the provided failure.
// Only the main go repo is currently supported.
func (c *client) wipe(builder, hash string) {
	if *grpcHost != "" {
		// TODO(golang.org/issue/34744) - Remove HTTP logic after gRPC API for ClearResults is deployed
		// to the Coordinator.
		c.grpcWipe(builder, hash)
		return
	}
	vals := url.Values{
		"builder": {builder},
		"hash":    {hash},
		"key":     {builderKey(builder)},
	}
	for i := 0; i < 10; i++ {
		res, err := http.PostForm(*builderPrefix+"/clear-results?"+vals.Encode(), nil)
		if err != nil {
			log.Fatal(err)
		}
		body, err := ioutil.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			log.Fatal(err)
		}
		if res.StatusCode != 200 {
			log.Fatalf("Error clearing %v hash %q: %v", builder, hash, res.Status)
		}
		var dashResponse struct {
			Error string
		}
		if err := json.Unmarshal(body, &dashResponse); err != nil {
			log.Fatalf("Bad dashboard response: %v\nBody: %s", err, body)
		}

		switch e := dashResponse.Error; e {
		case "datastore: concurrent transaction":
			log.Printf("Concurrent datastore transaction wiping %v %v: retrying in 1 second", builder, hash)
			time.Sleep(time.Second)
			continue
		default:
			log.Fatalf("Dashboard error: %v", e)
		case "":
			return
		}
	}
	log.Fatalf("Too many datastore transaction issues wiping %v %v", builder, hash)
}

func builderKey(builder string) string {
	if v, ok := builderKeyFromMaster(builder); ok {
		return v
	}
	if *keyFile == "" {
		log.Fatalf("No --key specified for builder %s", builder)
	}
	slurp, err := ioutil.ReadFile(*keyFile)
	if err != nil {
		log.Fatalf("Error reading builder key %s: %v", builder, err)
	}
	return strings.TrimSpace(string(slurp))
}

func builderKeyFromMaster(builder string) (key string, ok bool) {
	if *masterKeyFile == "" {
		return
	}
	slurp, err := ioutil.ReadFile(*masterKeyFile)
	if err != nil {
		return
	}
	if *sendMasterKey {
		return string(slurp), true
	}
	h := hmac.New(md5.New, bytes.TrimSpace(slurp))
	h.Write([]byte(builder))
	return fmt.Sprintf("%x", h.Sum(nil)), true
}

var (
	failMu    sync.Mutex
	failCache []Failure
)

func failures() (ret []Failure) {
	failMu.Lock()
	ret = failCache
	failMu.Unlock()
	if ret != nil {
		return
	}
	ret = []Failure{} // non-nil

	res, err := http.Get(*builderPrefix + "/?mode=failures&branch=" + url.QueryEscape(*branch))
	if err != nil {
		log.Fatal(err)
	}
	slurp, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		log.Fatal(err)
	}
	body := string(slurp)
	for _, line := range strings.Split(body, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 {
			ret = append(ret, Failure{
				Hash:    f[0],
				Builder: f[1],
				LogURL:  f[2],
			})
		}
	}

	failMu.Lock()
	failCache = ret
	failMu.Unlock()
	return ret
}
