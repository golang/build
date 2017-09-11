// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/option"
)

const (
	uploadURL     = "https://golang.org/dl/upload"
	projectID     = "999119582588"
	storageBucket = "golang"
)

// File represents a file on the golang.org downloads page.
// It should be kept in sync with the download code in x/tools/godoc/dl.
type File struct {
	Filename       string
	OS             string
	Arch           string
	Version        string
	ChecksumSHA256 string
	Size           int64
	Kind           string // "archive", "installer", "source"
}

// fileRe matches the files created by the release tool, such as:
//   go1.5beta2.src.tar.gz
//   go1.5.1.linux-386.tar.gz
//   go1.5.windows-amd64.msi
var fileRe = regexp.MustCompile(`^(go[a-z0-9-.]+)\.(src|([a-z0-9]+)-([a-z0-9]+)(?:-([a-z0-9.]+))?)\.(tar\.gz|zip|pkg|msi)(.asc)?$`)

func upload(files []string) error {
	ctx := context.Background()
	c, err := storageClient(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	var sitePayloads []*File
	var uploaded []string
	for _, name := range files {
		fileBytes, err := ioutil.ReadFile(name)
		if err != nil {
			return fmt.Errorf("ioutil.ReadFile(%q): %v", name, err)
		}
		base := filepath.Base(name)
		log.Printf("Uploading %v to GCS ...", base)
		m := fileRe.FindStringSubmatch(base)
		if m == nil {
			return fmt.Errorf("unrecognized file: %q", base)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(fileBytes))

		// Upload file to Google Cloud Storage.
		if err := putObject(ctx, c, base, fileBytes); err != nil {
			return fmt.Errorf("uploading %q: %v", name, err)
		}
		uploaded = append(uploaded, base)

		if strings.HasSuffix(base, ".asc") {
			// Don't add asc files to the download page, just upload it.
			continue
		}

		// Upload file.sha256.
		fname := base + ".sha256"
		if err := putObject(ctx, c, fname, []byte(checksum)); err != nil {
			return fmt.Errorf("uploading %q: %v", base+".sha256", err)
		}
		uploaded = append(uploaded, fname)

		var kind string
		switch {
		case m[2] == "src":
			kind = "source"
		case strings.HasSuffix(base, ".tar.gz"), strings.HasSuffix(base, ".zip"):
			kind = "archive"
		case strings.HasSuffix(base, ".msi"), strings.HasSuffix(base, ".pkg"):
			kind = "installer"
		}
		f := &File{
			Filename:       base,
			Version:        m[1],
			OS:             m[3],
			Arch:           m[4],
			ChecksumSHA256: checksum,
			Size:           int64(len(fileBytes)),
			Kind:           kind,
		}
		sitePayloads = append(sitePayloads, f)
	}

	log.Println("Waiting for edge cache ...")
	if err := waitForEdgeCache(uploaded); err != nil {
		return fmt.Errorf("waitForEdgeCache(%+v): %v", uploaded, err)
	}

	log.Println("Uploading payloads to golang.org ...")
	for _, f := range sitePayloads {
		if err := updateSite(f); err != nil {
			return fmt.Errorf("updateSite(%+v): %v", f, err)
		}
	}
	return nil
}

func waitForEdgeCache(uploaded []string) error {
	var g errgroup.Group
	for _, u := range uploaded {
		fname := u
		g.Go(func() error {
			// Add some jitter so that dozens of requests are not hitting the
			// endpoint at once.
			time.Sleep(time.Duration(rand.Intn(1000)) * time.Millisecond)
			t := time.Tick(5 * time.Second)
			var retries int
			for {
				url := "https://redirector.gvt1.com/edgedl/go/" + fname
				resp, err := http.Head(url)
				if err != nil {
					if retries < 3 {
						retries++
						<-t
						continue
					}
					return fmt.Errorf("http.Head(%q): %v", url, err)
				}
				retries = 0
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					log.Printf("%s is ready to go!", url)
					break
				}
				<-t
			}
			return nil
		})
	}
	return g.Wait()
}

func updateSite(f *File) error {
	// Post file details to golang.org.
	req, err := json.Marshal(f)
	if err != nil {
		return err
	}
	v := url.Values{"user": {*user}, "key": []string{userToken()}}
	u := fmt.Sprintf("%s?%s", uploadURL, v.Encode())
	resp, err := http.Post(u, "application/json", bytes.NewReader(req))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("upload failed: %v\n%s", resp.Status, b)
	}
	return nil
}

func putObject(ctx context.Context, c *storage.Client, name string, body []byte) error {
	wr := c.Bucket(storageBucket).Object(name).NewWriter(ctx)
	wr.ACL = []storage.ACLRule{
		{Entity: storage.AllUsers, Role: storage.RoleReader},
		// If you don't give the owners access, the web UI seems to
		// have a bug and doesn't have access to see that it's public,
		// so won't render the "Shared Publicly" link. So we do that,
		// even though it's dumb and unnecessary otherwise:
		{Entity: storage.ACLEntity("project-owners-" + projectID), Role: storage.RoleOwner},
	}
	if _, err := wr.Write(body); err != nil {
		return err
	}
	return wr.Close()
}

func storageClient(ctx context.Context) (*storage.Client, error) {
	file := filepath.Join(os.Getenv("HOME"), "keys", "golang-org.service.json")
	blob, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}
	config, err := google.JWTConfigFromJSON(blob, storage.ScopeReadWrite)
	if err != nil {
		return nil, err
	}
	return storage.NewClient(ctx, option.WithTokenSource(config.TokenSource(ctx)))
}
