// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command issuelock locks Github issues.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: issuelock [<issue>]\n")
	flag.PrintDefaults()
	os.Exit(1)
}

func main() {
	flag.Usage = usage
	flag.Parse()

	tokenFile := filepath.Join(os.Getenv("HOME"), "keys", "github-gobot")
	slurp, err := ioutil.ReadFile(tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	f := strings.SplitN(strings.TrimSpace(string(slurp)), ":", 2)
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		log.Fatalf("Expected token file %s to be of form <username>:<token>", tokenFile)
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: f[1]})
	tc := oauth2.NewClient(context.Background(), ts)
	client := github.NewClient(tc)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if flag.NArg() == 1 {
		issueNum, err := strconv.Atoi(flag.Arg(0))
		if err != nil {
			usage()
		}
		if err := freeze(ctx, client, issueNum); err != nil {
			log.Fatal(err)
		}
		return
	}
	if flag.NArg() > 1 {
		usage()
	}

	tooOld := time.Now().Add(-365 * 24 * time.Hour).Format("2006-01-02")
	log.Printf("Freezing closed issues before %v", tooOld)
	for {
		result, response, err := client.Search.Issues(ctx, "repo:golang/go is:closed -label:FrozenDueToAge updated:<="+tooOld, &github.SearchOptions{
			Sort:  "created",
			Order: "asc",
			ListOptions: github.ListOptions{
				PerPage: 500,
			},
		})
		if err != nil {
			log.Fatal(err)
		}

		if *result.Total == 0 {
			return
		}
		log.Printf("Matches: %d, Res: %#v", *result.Total, response)
		for _, is := range result.Issues {
			num := *is.Number
			log.Printf("Freezing issue: %d", *is.Number)
			if err := freeze(ctx, client, num); err != nil {
				log.Fatal(err)
			}
			time.Sleep(500 * time.Millisecond) // be nice to github
		}
	}
}

func freeze(ctx context.Context, client *github.Client, issueNum int) error {
	_, err := client.Issues.Lock(ctx, "golang", "go", issueNum)
	if err != nil {
		return err
	}
	_, _, err = client.Issues.AddLabelsToIssue(ctx, "golang", "go", issueNum, []string{"FrozenDueToAge"})
	return err
}
