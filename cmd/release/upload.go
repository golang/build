// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
)

const (
	uploadURL     = "https://golang.org/dl/upload"
	projectID     = "999119582588"
	storageBucket = "golang"
)

var publicACL = []storage.ACLRule{
	{Entity: storage.AllUsers, Role: storage.RoleReader},
	// If you don't give the owners access, the web UI seems to
	// have a bug and doesn't have access to see that it's public,
	// so won't render the "Shared Publicly" link. So we do that,
	// even though it's dumb and unnecessary otherwise:
	{Entity: storage.ACLEntity("project-owners-" + projectID), Role: storage.RoleOwner},
}

// File represents a file on the golang.org downloads page.
// It should be kept in sync with the download code in x/tools/godoc/dl.
type File struct {
	Filename       string `json:"filename"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	Version        string `json:"version"`
	Checksum       string `json:"-"` // SHA1; deprecated
	ChecksumSHA256 string `json:"sha256"`
	Size           int64  `json:"size"`
	Kind           string `json:"kind"` // "archive", "installer", "source"
}

// fileRe matches the files created by the release tool, such as:
//   go1.5beta2.src.tar.gz
//   go1.5.1.linux-386.tar.gz
//   go1.5.windows-amd64.msi
var fileRe = regexp.MustCompile(`^(go[a-z0-9-.]+)\.(src|([a-z0-9]+)-([a-z0-9]+)(?:-([a-z0-9.]+))?)\.(tar\.gz|zip|pkg|msi)(.asc)?$`)

func upload(files []string) error {
	ctx := context.Background()
	c, err := storage.NewClient(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	files, err = expandFiles(ctx, c, files)
	if err != nil {
		return err
	}

	files = chooseBestFiles(files)

	var sitePayloads []*File
	var uploaded []string
	for _, name := range files {
		base := filepath.Base(name)
		log.Printf("Uploading %v to GCS ...", base)
		m := fileRe.FindStringSubmatch(base)
		if m == nil {
			return fmt.Errorf("unrecognized file: %q", base)
		}

		checksum, size, err := uploadArtifact(ctx, c, name)
		if err != nil {
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
			Size:           size,
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
	if *uploadKick != "" {
		args := strings.Fields(*uploadKick)
		log.Printf("Running %v...", args)
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout = os.Stderr // Don't print to stdout.
		cmd.Stderr = os.Stderr
		// Don't wait for the command to finish.
		if err := cmd.Start(); err != nil {
			log.Printf("Couldn't start edge cache update command: %v", err)
		}
	}

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
				url := "https://dl.google.com/go/" + fname
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
	wr.ACL = publicACL
	if _, err := wr.Write(body); err != nil {
		return err
	}
	return wr.Close()
}

// expandFiles expands any "/..." paths in GCS URIs to include files in its subtree.
func expandFiles(ctx context.Context, storageClient *storage.Client, files []string) ([]string, error) {
	var expanded []string
	for _, f := range files {
		if !(strings.HasPrefix(f, "gs://") && strings.HasSuffix(f, "/...")) {
			expanded = append(expanded, f)
			continue
		}
		bucket, path := gcsParts(f)

		iter := storageClient.Bucket(bucket).Objects(ctx, &storage.Query{
			Prefix: strings.TrimSuffix(path, "..."), // Retain trailing "/" (if present).
		})
		for {
			attrs, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return nil, err
			}
			if filepath.Ext(attrs.Name) == ".sha256" {
				// Ignore sha256 files.
				continue
			}
			expanded = append(expanded, fmt.Sprintf("gs://%s/%s", attrs.Bucket, attrs.Name))
		}
	}
	return expanded, nil
}

// gcsParts splits a GCS URI (e.g., "gs://bucket/path/to/object") into its bucket and path parts:
// ("bucket", "path/to/object")
//
// It assumes its input a well-formed GCS URI.
func gcsParts(uri string) (bucket, path string) {
	parts := strings.SplitN(strings.TrimPrefix(uri, "gs://"), "/", 2)
	return parts[0], parts[1]
}

func chooseBestFiles(files []string) []string {
	// map from basename to filepath/GCS URI.
	best := make(map[string]string)
	for _, f := range files {
		base := filepath.Base(f)
		if _, ok := best[base]; !ok {
			best[base] = f
			continue
		}

		// Overwrite existing only if the new entry is signed.
		if strings.HasPrefix(f, "gs://") && strings.Contains(f, "/signed/") {
			best[base] = f
		}
	}

	var out []string
	for _, path := range best {
		out = append(out, path)
	}
	sort.Strings(out) // for prettier printing.
	return out
}

func uploadArtifact(ctx context.Context, storageClient *storage.Client, path string) (checksum string, size int64, err error) {
	if strings.HasPrefix(path, "gs://") {
		return uploadArtifactGCS(ctx, storageClient, path)
	}
	return uploadArtifactLocal(ctx, storageClient, path)
}

func uploadArtifactGCS(ctx context.Context, storageClient *storage.Client, path string) (checksum string, size int64, err error) {
	bucket, path := gcsParts(path)
	base := filepath.Base(path)
	src := storageClient.Bucket(bucket).Object(path)
	dst := storageClient.Bucket(storageBucket).Object(base)

	r, err := storageClient.Bucket(bucket).Object(path + ".sha256").NewReader(ctx)
	if err != nil {
		return "", -1, fmt.Errorf("could not get sha256: %v", err)
	}
	checksumBytes, err := ioutil.ReadAll(r)
	if err != nil {
		return "", -1, fmt.Errorf("could not get sha256: %v", err)
	}
	copier := dst.CopierFrom(src)
	copier.ACL = publicACL
	attrs, err := copier.Run(ctx)
	if err != nil {
		return "", -1, err
	}
	return string(checksumBytes), attrs.Size, nil
}

func uploadArtifactLocal(ctx context.Context, storageClient *storage.Client, path string) (checksum string, size int64, err error) {
	base := filepath.Base(path)

	fileBytes, err := ioutil.ReadFile(path)
	if err != nil {
		return "", -1, fmt.Errorf("ioutil.ReadFile: %v", err)
	}
	// Upload file to Google Cloud Storage.
	if err := putObject(ctx, storageClient, base, fileBytes); err != nil {
		return "", -1, err
	}
	checksum = fmt.Sprintf("%x", sha256.Sum256(fileBytes))
	return checksum, int64(len(fileBytes)), nil
}
