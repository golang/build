// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The upload command writes a file to Google Cloud Storage. It's used
// exclusively by the Makefiles in the Go project repos. Think of it
// as a very light version of gsutil or gcloud, but with some
// Go-specific configuration knowledge baked in.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"cloud.google.com/go/storage"
)

var (
	public    = flag.Bool("public", false, "object should be world-readable")
	cacheable = flag.Bool("cacheable", true, "object should be cacheable")
	file      = flag.String("file", "", "read object from `file` ('-' for stdin)")
	verbose   = flag.Bool("verbose", false, "verbose logging")
	project   = flag.String("project", "", "GCP Project. If blank, it's automatically inferred from the bucket name for the common Go buckets.")
	doGzip    = flag.Bool("gzip", false, "gzip the stored contents (not the upload's Content-Encoding); this forces the Content-Type to be application/octet-stream. To prevent misuse, the object name must also end in '.gz'")
	extraEnv  = flag.String("env", "", "comma-separated list of addition KEY=val environment pairs to include in build environment when building a target to upload")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: upload [flags] bucket/object\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}
	args := strings.SplitN(flag.Arg(0), "/", 2)
	if len(args) != 2 {
		flag.Usage()
		os.Exit(1)
	}
	if strings.HasPrefix(*file, "go:") {
		log.Fatalf("-file=go:target syntax is no longer supported")
	}
	bucket, object := args[0], args[1]

	if *doGzip && !strings.HasSuffix(object, ".gz") {
		log.Fatalf("-gzip flag requires object ending in .gz")
	}

	proj := *project
	if proj == "" {
		proj = bucketProject[bucket]
		if proj == "" {
			log.Fatalf("bucket %q doesn't have an associated project in upload.go", bucket)
		}
	}

	ctx := context.Background()
	storageClient, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatalf("storage.NewClient: %v", err)
	}

	if alreadyUploaded(storageClient, bucket, object) {
		if *verbose {
			log.Printf("gs://%s/%s up-to-date", bucket, object)
		}
		return
	}

	w := storageClient.Bucket(bucket).Object(object).NewWriter(ctx)
	// If you don't give the owners access, the web UI seems to
	// have a bug and doesn't have access to see that it's public, so
	// won't render the "Shared Publicly" link. So we do that, even
	// though it's dumb and unnecessary otherwise:
	w.ACL = append(w.ACL, storage.ACLRule{Entity: storage.ACLEntity("project-owners-" + proj), Role: storage.RoleOwner})
	if *public {
		w.ACL = append(w.ACL, storage.ACLRule{Entity: storage.AllUsers, Role: storage.RoleReader})
		if !*cacheable {
			w.CacheControl = "no-cache"
		}
	}
	var content io.Reader
	switch {
	case *file == "-":
		content = os.Stdin
	default:
		content, err = os.Open(*file)
		if err != nil {
			log.Fatal(err)
		}
	}
	if *doGzip {
		var zbuf bytes.Buffer
		zw := gzip.NewWriter(&zbuf)
		if _, err := io.Copy(zw, content); err != nil {
			log.Fatalf("compressing content: %v", err)
		}
		if err := zw.Close(); err != nil {
			log.Fatalf("gzip.Close: %v", err)
		}
		content = &zbuf
	}

	const maxSlurp = 1 << 20
	var buf bytes.Buffer
	n, err := io.CopyN(&buf, content, maxSlurp)
	if err != nil && err != io.EOF {
		log.Fatalf("Error reading file: %v, %v", n, err)
	}

	if *doGzip {
		w.ContentType = "application/octet-stream"
	} else {
		w.ContentType = http.DetectContentType(buf.Bytes())
	}

	_, err = io.Copy(w, io.MultiReader(&buf, content))
	if cerr := w.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		log.Fatalf("Write error: %v", err)
	}
	if *verbose {
		log.Printf("gs://%s/%s uploaded", bucket, object)
	}
}

// bucketProject is a map from bucket name to its Google Cloud Platform project ID.
var bucketProject = map[string]string{
	"dev-go-builder-data": "go-dashboard-dev",
	"go-builder-data":     "symbolic-datum-552",
	"go-build-log":        "symbolic-datum-552",
	"golang":              "999119582588",
}

// alreadyUploaded reports whether *file has already been uploaded and the correct contents
// are on cloud storage already.
func alreadyUploaded(storageClient *storage.Client, bucket, object string) bool {
	if *file == "-" {
		return false // don't know.
	}
	o, err := storageClient.Bucket(bucket).Object(object).Attrs(context.Background())
	if err == storage.ErrObjectNotExist {
		return false
	}
	if err != nil {
		log.Printf("Warning: stat failure: %v", err)
		return false
	}
	m5 := md5.New()
	fi, err := os.Stat(*file)
	if err != nil {
		log.Fatal(err)
	}
	if fi.Size() != o.Size {
		return false
	}
	f, err := os.Open(*file)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	n, err := io.Copy(m5, f)
	if err != nil {
		log.Fatal(err)
	}
	if n != fi.Size() {
		log.Printf("Warning: file size of %v changed", *file)
	}
	return bytes.Equal(m5.Sum(nil), o.MD5)
}
