// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/tarutil"
	"golang.org/x/sync/errgroup"
)

// putTar a .tar.gz
func putTar(args []string) error {
	fs := flag.NewFlagSet("put", flag.ContinueOnError)
	fs.Usage = func() {
		log := usageLogger
		log.Print("puttar usage: gomote puttar [put-opts] [instance] <source>")
		log.Print()
		log.Print("<source> may be one of:")
		log.Print("- A path to a local .tar.gz file.")
		log.Print("- A URL that points at a .tar.gz file.")
		log.Print("- The '-' character to indicate a .tar.gz file passed via stdin.")
		log.Print("- Git hash (min 7 characters) for the Go repository (extract a .tar.gz of the repository at that commit w/o history)")
		log.Print()
		log.Print("Instance name is optional if a group is specified.")
		fs.PrintDefaults()
		os.Exit(1)
	}
	var dir string
	fs.StringVar(&dir, "dir", "", "relative directory from buildlet's work dir to extra tarball into")

	fs.Parse(args)

	// Parse arguments.
	var putSet []string
	var src string
	switch fs.NArg() {
	case 1:
		// Must be just the source, so we need an active group.
		if activeGroup == nil {
			log.Print("no active group found; need an active group with only 1 argument")
			fs.Usage()
		}
		for _, inst := range activeGroup.Instances {
			putSet = append(putSet, inst)
		}
		src = fs.Arg(0)
	case 2:
		// Instance and source is specified.
		putSet = []string{fs.Arg(0)}
		src = fs.Arg(1)
	case 0:
		log.Print("error: not enough arguments")
		fs.Usage()
	default:
		log.Print("error: too many arguments")
		fs.Usage()
	}

	// Interpret source.
	var putTarFn func(ctx context.Context, inst string) error
	if src == "-" {
		// We might have multiple readers, so slurp up STDIN
		// and store it, then hand out bytes.Readers to everyone.
		var buf bytes.Buffer
		_, err := io.Copy(&buf, os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		sharedTarBuf := buf.Bytes()
		putTarFn = func(ctx context.Context, inst string) error {
			return doPutTar(ctx, inst, dir, bytes.NewReader(sharedTarBuf))
		}
	} else {
		u, err := url.Parse(src)
		if err != nil {
			// The URL parser should technically accept any of these, so the fact that
			// we failed means its *very* malformed.
			return fmt.Errorf("malformed source: not a path, a URL, -, or a git hash")
		}
		if u.Scheme != "" || u.Host != "" {
			// Probably a real URL.
			putTarFn = func(ctx context.Context, inst string) error {
				return doPutTarURL(ctx, inst, dir, u.String())
			}
		} else {
			// Probably a path. Check if it exists.
			_, err := os.Stat(src)
			if os.IsNotExist(err) {
				// It must be a git hash. Check if this actually matches a git hash.
				if len(src) < 7 || len(src) > 40 || regexp.MustCompile("[^a-f0-9]").MatchString(src) {
					return fmt.Errorf("malformed source: not a path, a URL, -, or a git hash")
				}
				putTarFn = func(ctx context.Context, inst string) error {
					return doPutTarGoRev(ctx, inst, dir, src)
				}
			} else if err != nil {
				return fmt.Errorf("failed to stat %q: %w", src, err)
			} else {
				// It's a path.
				putTarFn = func(ctx context.Context, inst string) error {
					f, err := os.Open(src)
					if err != nil {
						return fmt.Errorf("opening %q: %w", src, err)
					}
					defer f.Close()
					return doPutTar(ctx, inst, dir, f)
				}
			}
		}
	}
	eg, ctx := errgroup.WithContext(context.Background())
	for _, inst := range putSet {
		eg.Go(func() error {
			return putTarFn(ctx, inst)
		})
	}
	return eg.Wait()
}

func doPutTarURL(ctx context.Context, name, dir, tarURL string) error {
	client := gomoteServerClient(ctx)
	_, err := client.WriteTGZFromURL(ctx, &protos.WriteTGZFromURLRequest{
		GomoteId:  name,
		Directory: dir,
		Url:       tarURL,
	})
	if err != nil {
		return fmt.Errorf("unable to write tar to instance: %w", err)
	}
	return nil
}

func doPutTarGoRev(ctx context.Context, name, dir, rev string) error {
	tarURL := "https://go.googlesource.com/go/+archive/" + rev + ".tar.gz"
	if err := doPutTarURL(ctx, name, dir, tarURL); err != nil {
		return err
	}

	// Put a VERSION file there too, to avoid git usage.
	version := strings.NewReader("devel " + rev)
	var vtar tarutil.FileList
	vtar.AddRegular(&tar.Header{
		Name: "VERSION",
		Mode: 0644,
		Size: int64(version.Len()),
	}, int64(version.Len()), version)
	tgz := vtar.TarGz()
	defer tgz.Close()

	client := gomoteServerClient(ctx)
	resp, err := client.UploadFile(ctx, &protos.UploadFileRequest{})
	if err != nil {
		return fmt.Errorf("unable to request credentials for a file upload: %w", err)
	}
	if err := uploadToGCS(ctx, resp.GetFields(), tgz, resp.GetObjectName(), resp.GetUrl()); err != nil {
		return fmt.Errorf("unable to upload version file to GCS: %w", err)
	}
	if _, err = client.WriteTGZFromURL(ctx, &protos.WriteTGZFromURLRequest{
		GomoteId:  name,
		Directory: dir,
		Url:       fmt.Sprintf("%s%s", resp.GetUrl(), resp.GetObjectName()),
	}); err != nil {
		return fmt.Errorf("unable to write tar to instance: %w", err)
	}
	return nil
}

func doPutTar(ctx context.Context, name, dir string, tgz io.Reader) error {
	client := gomoteServerClient(ctx)
	resp, err := client.UploadFile(ctx, &protos.UploadFileRequest{})
	if err != nil {
		return fmt.Errorf("unable to request credentials for a file upload: %w", err)
	}
	if err := uploadToGCS(ctx, resp.GetFields(), tgz, resp.GetObjectName(), resp.GetUrl()); err != nil {
		return fmt.Errorf("unable to upload file to GCS: %w", err)
	}
	if _, err := client.WriteTGZFromURL(ctx, &protos.WriteTGZFromURLRequest{
		GomoteId:  name,
		Directory: dir,
		Url:       fmt.Sprintf("%s%s", resp.GetUrl(), resp.GetObjectName()),
	}); err != nil {
		return fmt.Errorf("unable to write tar to instance: %w", err)
	}
	return nil
}

// putBootstrap places the bootstrap version of go in the workdir
func putBootstrap(args []string) error {
	fs := flag.NewFlagSet("putbootstrap", flag.ContinueOnError)
	fs.Usage = func() {
		log.Print("putbootstrap usage: gomote putbootstrap [instance]")
		fmt.Fprintln(os.Stderr)
		log.Print("Instance name is optional if a group is specified.")
		fs.PrintDefaults()
		os.Exit(1)
	}
	fs.Parse(args)

	var putSet []string
	switch fs.NArg() {
	case 0:
		if activeGroup == nil {
			log.Print("no active group found; need an active group with only 1 argument")
			fs.Usage()
		}
		for _, inst := range activeGroup.Instances {
			putSet = append(putSet, inst)
		}
	case 1:
		putSet = []string{fs.Arg(0)}
	default:
		log.Print("error: too many arguments")
		fs.Usage()
	}

	eg, ctx := errgroup.WithContext(context.Background())
	for _, inst := range putSet {
		eg.Go(func() error {
			// TODO(66635) remove once gomotes can no longer be created via the coordinator.
			if luciDisabled() {
				client := gomoteServerClient(ctx)
				resp, err := client.AddBootstrap(ctx, &protos.AddBootstrapRequest{
					GomoteId: inst,
				})
				if err != nil {
					return fmt.Errorf("unable to add bootstrap version of Go to instance: %w", err)
				}
				if resp.GetBootstrapGoUrl() == "" {
					fmt.Printf("No GoBootstrapURL defined for %q; ignoring. (may be baked into image)\n", inst)
				}
			}
			return nil
		})
	}
	return eg.Wait()
}

// put single file
func put(args []string) error {
	fs := flag.NewFlagSet("put", flag.ContinueOnError)
	fs.Usage = func() {
		log.Print("put usage: gomote put [put-opts] [instance] <source or '-' for stdin> [destination]")
		fmt.Fprintln(os.Stderr)
		log.Print("Instance name is optional if a group is specified.")
		fs.PrintDefaults()
		os.Exit(1)
	}
	modeStr := fs.String("mode", "", "Unix file mode (octal); default to source file mode")
	fs.Parse(args)

	if fs.NArg() == 0 {
		fs.Usage()
	}

	ctx := context.Background()
	var putSet []string
	var src, dst string
	if err := doPing(ctx, fs.Arg(0)); instanceDoesNotExist(err) {
		// When there's no active group, this is just an error.
		if activeGroup == nil {
			return fmt.Errorf("instance %q: %w", fs.Arg(0), err)
		}
		// When there is an active group, this just means that we're going
		// to use the group instead and assume the rest is a command.
		for _, inst := range activeGroup.Instances {
			putSet = append(putSet, inst)
		}
		src = fs.Arg(0)
		if fs.NArg() == 2 {
			dst = fs.Arg(1)
		} else if fs.NArg() != 1 {
			log.Print("error: too many arguments")
			fs.Usage()
		}
	} else if err == nil {
		putSet = append(putSet, fs.Arg(0))
		if fs.NArg() == 1 {
			log.Print("error: missing source")
			fs.Usage()
		}
		src = fs.Arg(1)
		if fs.NArg() == 3 {
			dst = fs.Arg(2)
		} else if fs.NArg() != 2 {
			log.Print("error: too many arguments")
			fs.Usage()
		}
	} else {
		return fmt.Errorf("checking instance %q: %w", fs.Arg(0), err)
	}
	if dst == "" {
		if src == "-" {
			return errors.New("must specify destination file name when source is standard input")
		}
		dst = filepath.Base(src)
	}

	var mode os.FileMode = 0666
	if *modeStr != "" {
		modeInt, err := strconv.ParseInt(*modeStr, 8, 64)
		if err != nil {
			return err
		}
		mode = os.FileMode(modeInt)
		if !mode.IsRegular() {
			return fmt.Errorf("bad mode: %v", mode)
		}
	}

	var putFileFn func(context.Context, string) error
	if src == "-" {
		var buf bytes.Buffer
		_, err := io.Copy(&buf, os.Stdin)
		if err != nil {
			return fmt.Errorf("reading from stdin: %w", err)
		}
		sharedFileBuf := buf.Bytes()
		putFileFn = func(ctx context.Context, inst string) error {
			return doPutFile(ctx, inst, bytes.NewReader(sharedFileBuf), dst, mode)
		}
	} else {
		putFileFn = func(ctx context.Context, inst string) error {
			f, err := os.Open(src)
			if err != nil {
				return err
			}
			defer f.Close()

			if *modeStr == "" {
				fi, err := f.Stat()
				if err != nil {
					return err
				}
				mode = fi.Mode()
			}
			return doPutFile(ctx, inst, f, dst, mode)
		}
	}

	eg, ctx := errgroup.WithContext(ctx)
	for _, inst := range putSet {
		eg.Go(func() error {
			return putFileFn(ctx, inst)
		})
	}
	return eg.Wait()
}

func doPutFile(ctx context.Context, inst string, r io.Reader, dst string, mode os.FileMode) error {
	client := gomoteServerClient(ctx)
	resp, err := client.UploadFile(ctx, &protos.UploadFileRequest{})
	if err != nil {
		return fmt.Errorf("unable to request credentials for a file upload: %w", err)
	}
	err = uploadToGCS(ctx, resp.GetFields(), r, dst, resp.GetUrl())
	if err != nil {
		return fmt.Errorf("unable to upload file to GCS: %w", err)
	}
	_, err = client.WriteFileFromURL(ctx, &protos.WriteFileFromURLRequest{
		GomoteId: inst,
		Url:      fmt.Sprintf("%s%s", resp.GetUrl(), resp.GetObjectName()),
		Filename: dst,
		Mode:     uint32(mode),
	})
	if err != nil {
		return fmt.Errorf("unable to write the file from URL: %w", err)
	}
	return nil
}

func uploadToGCS(ctx context.Context, fields map[string]string, file io.Reader, filename, url string) error {
	buf := new(bytes.Buffer)
	mw := multipart.NewWriter(buf)

	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return fmt.Errorf("unable to write field: %w", err)
		}
	}
	_, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return fmt.Errorf("unable to create form file: %w", err)
	}
	// Write our own boundary to avoid buffering entire file into the multipart Writer
	bound := fmt.Sprintf("\r\n--%s--\r\n", mw.Boundary())
	req, err := http.NewRequestWithContext(ctx, "POST", url, io.NopCloser(io.MultiReader(buf, file, strings.NewReader(bound))))
	if err != nil {
		return fmt.Errorf("unable to create request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("http post failed: status code=%d", res.StatusCode)
	}
	return nil
}
