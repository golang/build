// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package gitauth writes gitcookies files so git will authenticate
// to Gerrit as gopherbot for quota purposes.
package gitauth

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/build/internal/secret"
)

func Init() error {
	cookieFile := filepath.Join(homeDir(), ".gitcookies")
	if err := exec.Command("git", "config", "--global", "http.cookiefile", cookieFile).Run(); err != nil {
		return fmt.Errorf("running git config to set cookiefile: %v", err)
	}
	if !metadata.OnGCE() {
		// Do nothing for now.
		return nil
	}

	sc := mustCreateSecretClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slurp, err := sc.Retrieve(ctx, secret.NameGobotPassword)
	if err != nil {
		proj, _ := metadata.ProjectID()
		if proj != "symbolic-datum-552" { // TODO: don't hard-code this; use buildenv package
			log.Printf("gitauth: ignoring %q secret manager lookup on non-prod project: %v", secret.NameGobotPassword, err)
			return nil
		}
		return fmt.Errorf("gitauth: getting %s secret manager: %v", secret.NameGobotPassword, err)
	}
	slurp = strings.TrimSpace(slurp)
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "go.googlesource.com\tFALSE\t/\tTRUE\t2147483647\to\tgit-gobot.gmail.com=%s\n", slurp)
	fmt.Fprintf(&buf, "go-review.googlesource.com\tFALSE\t/\tTRUE\t2147483647\to\tgit-gobot.gmail.com=%s\n", slurp)
	return ioutil.WriteFile(cookieFile, buf.Bytes(), 0644)
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	log.Fatalf("No HOME set in environment.")
	panic("unreachable")
}

func mustCreateSecretClient() *secret.Client {
	client, err := secret.NewClient()
	if err != nil {
		log.Fatalf("unable to create secret client %v", err)
	}
	return client
}
