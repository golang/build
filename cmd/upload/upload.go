// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The upload command writes a file to Google Cloud Storage. It's used
// exclusively by the Makefiles in the Go project repos. Think of it
// as a very light version of gsutil or gcloud, but with some
// Go-specific configuration knowledge baked in.
package main

import (
	"archive/tar"
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
	"regexp"
	"strings"

	"cloud.google.com/go/storage"
)

var (
	public    = flag.Bool("public", false, "object should be world-readable")
	cacheable = flag.Bool("cacheable", true, "object should be cacheable")
	file      = flag.String("file", "", "read object from `file` ('-' for stdin)")
	verbose   = flag.Bool("verbose", false, "verbose logging")
	project   = flag.String("project", "", "GCE Project. If blank, it's automatically inferred from the bucket name for the common Go buckets.")
	doGzip    = flag.Bool("gzip", false, "gzip the stored contents (not the upload's Content-Encoding); this forces the Content-Type to be application/octet-stream. To prevent misuse, the object name must also end in '.gz'")
	extraEnv  = flag.String("env", "", "comma-separated list of addition KEY=val environment pairs to include in build environment when building a target to upload")
)

// to match uploads to e.g. https://storage.googleapis.com/golang/go1.4-bootstrap-20170531.tar.gz.
var go14BootstrapRx = regexp.MustCompile(`^go1\.4-bootstrap-20\d{6}\.tar\.gz$`)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: upload [flags] <bucket/object>

If <bucket/object> is of the form "golang/go1.4-bootstrap-20yymmdd.tar.gz",
then the current release-branch.go1.4 is uploaded from Gerrit, with each
tar entry filename beginning with the prefix "go/".

`)
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

	// Special support for auto-tarring up Go 1.4 tarballs from the 1.4 release branch.
	is14Src := bucket == "golang" && go14BootstrapRx.MatchString(object)
	if is14Src {
		if *file != "-" {
			log.Fatalf("invalid use of -file with Go 1.4 tarball %v", object)
		}
		*doGzip = true
		*public = true
		*cacheable = true
	}

	if *doGzip && !strings.HasSuffix(object, ".gz") {
		log.Fatalf("-gzip flag requires object ending in .gz")
	}

	proj := *project
	if proj == "" {
		proj, _ = bucketProject[bucket]
		if proj == "" {
			log.Fatalf("bucket %q doesn't have an associated project in upload.go", bucket)
		}
	}

	ctx := context.Background()
	storageClient, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatalf("storage.NewClient: %v", err)
	}

	if is14Src {
		_, err := storageClient.Bucket(bucket).Object(object).Attrs(context.Background())
		if err != storage.ErrObjectNotExist {
			if err == nil {
				log.Fatalf("object %v already exists; refusing to overwrite.", object)
			}
			log.Fatalf("error checking for %v: %v", object, err)
		}
	} else if alreadyUploaded(storageClient, bucket, object) {
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
	case is14Src:
		content = generate14Tarfile()
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

var bucketProject = map[string]string{
	"dev-gccgo-builder-data": "gccgo-dashboard-dev",
	"dev-go-builder-data":    "go-dashboard-dev",
	"gccgo-builder-data":     "gccgo-dashboard-builders",
	"go-builder-data":        "symbolic-datum-552",
	"go-build-log":           "symbolic-datum-552",
	"http2-demo-server-tls":  "symbolic-datum-552",
	"winstrap":               "999119582588",
	"gobuilder":              "999119582588", // deprecated
	"golang":                 "999119582588",
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

// generate14Tarfile downloads the release-branch.go1.4 release branch
// tarball and returns it uncompressed, with the "go/" prefix before
// each tar header's filename.
func generate14Tarfile() io.Reader {
	const tarURL = "https://go.googlesource.com/go/+archive/release-branch.go1.4.tar.gz"
	res, err := http.Get(tarURL)
	if err != nil {
		log.Fatal(err)
	}
	if res.StatusCode != 200 {
		log.Fatalf("%v: %v", tarURL, res.Status)
	}
	if got, want := res.Header.Get("Content-Type"), "application/x-gzip"; got != want {
		log.Fatalf("%v: response Content-Type = %q; expected %q", tarURL, got, want)
	}

	var out bytes.Buffer // output tar (not gzipped)

	tw := tar.NewWriter(&out)

	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		log.Fatal(err)
	}
	tr := tar.NewReader(zr)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeSymlink, tar.TypeDir:
			// Accept these.
		default:
			continue
		}
		hdr.Name = "go/" + hdr.Name
		if err := tw.WriteHeader(hdr); err != nil {
			log.Fatalf("WriteHeader: %v", err)
		}
		if _, err := io.Copy(tw, tr); err != nil {
			log.Fatalf("tar copying %v: %v", hdr.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		log.Fatal(err)
	}
	return &out
}
