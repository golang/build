// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/gogo/protobuf/proto"
	"golang.org/x/build/maintner/maintpb"
	"golang.org/x/build/maintner/reclog"
)

// NewNetworkMutationSource returns a mutation source from a master server.
// The server argument should be a URL to the JSON logs index.
func NewNetworkMutationSource(server, cacheDir string) MutationSource {
	base, err := url.Parse(server)
	if err != nil {
		panic(fmt.Sprintf("invalid URL: %q", server))
	}
	return &netMutSource{
		server:   server,
		base:     base,
		cacheDir: cacheDir,
	}
}

type netMutSource struct {
	server   string
	base     *url.URL
	cacheDir string
}

func (ns *netMutSource) GetMutations(ctx context.Context) <-chan MutationStreamEvent {
	ch := make(chan MutationStreamEvent, 50)
	go func() {
		err := ns.sendMutations(ctx, ch)
		final := MutationStreamEvent{Err: err}
		if err == nil {
			final.End = true
		}
		select {
		case ch <- final:
		case <-ctx.Done():
		}
	}()
	return ch
}

func (ns *netMutSource) sendMutations(ctx context.Context, ch chan<- MutationStreamEvent) error {
	req, err := http.NewRequest("GET", ns.server, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("%s: %v", ns.server, res.Status)
	}
	var segs []LogSegmentJSON
	if err := json.NewDecoder(res.Body).Decode(&segs); err != nil {
		return fmt.Errorf("decoding %s JSON: %v", ns.server, err)
	}

	// TODO: optimization: if already on GCE, skip sync to disk part and just
	// read from network. fast & free network inside.

	var fileSegs []fileSeg
	for _, seg := range segs {
		fileSeg, err := ns.syncSeg(ctx, seg)
		if err != nil {
			return fmt.Errorf("syncing segment %d: %v", seg.Number, err)
		}
		fileSegs = append(fileSegs, fileSeg)
	}
	return foreachFileSeg(fileSegs, func(seg fileSeg) error {
		f, err := os.Open(seg.file)
		if err != nil {
			return err
		}
		defer f.Close()
		return reclog.ForeachRecord(io.LimitReader(f, seg.size), func(off int64, hdr, rec []byte) error {
			m := new(maintpb.Mutation)
			if err := proto.Unmarshal(rec, m); err != nil {
				return err
			}
			select {
			case ch <- MutationStreamEvent{Mutation: m}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	})
}

func foreachFileSeg(segs []fileSeg, fn func(seg fileSeg) error) error {
	for _, seg := range segs {
		if err := fn(seg); err != nil {
			return err
		}
	}
	return nil
}

type fileSeg struct {
	seg  int
	file string // full path
	size int64
}

func (ns *netMutSource) syncSeg(ctx context.Context, seg LogSegmentJSON) (fileSeg, error) {
	isFinalSeg := !strings.HasPrefix(seg.URL, "https://storage.googleapis.com/")
	relURL, err := url.Parse(seg.URL)
	if err != nil {
		return fileSeg{}, err
	}
	segURL := ns.base.ResolveReference(relURL)

	frozen := filepath.Join(ns.cacheDir, fmt.Sprintf("%04d.%s.mutlog", seg.Number, seg.SHA224))

	// Do we already have it? Files named in their final form with the sha224 are considered
	// complete and immutable.
	if fi, err := os.Stat(frozen); err == nil && fi.Size() == seg.Size {
		return fileSeg{seg.Number, frozen, fi.Size()}, nil
	}

	// See how much data we already have in the partial growing file.
	partial := filepath.Join(ns.cacheDir, fmt.Sprintf("%04d.growing.mutlog", seg.Number))
	have, _ := ioutil.ReadFile(partial)
	if int64(len(have)) == seg.Size {
		got224 := fmt.Sprintf("%x", sha256.Sum224(have))
		if got224 == seg.SHA224 {
			if !isFinalSeg {
				// This was growing for us, but the server started a new growing segment.
				if err := os.Rename(partial, frozen); err != nil {
					return fileSeg{}, err
				}
				return fileSeg{seg.Number, frozen, seg.Size}, nil
			}
			return fileSeg{seg.Number, partial, seg.Size}, nil
		}
	}

	// Otherwise, download.
	req, err := http.NewRequest("GET", segURL.String(), nil)
	if err != nil {
		return fileSeg{}, err
	}
	req = req.WithContext(ctx)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", len(have), seg.Size-1))

	log.Printf("Downloading %d bytes of %s ...", seg.Size-int64(len(have)), segURL)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fileSeg{}, err
	}
	if res.StatusCode != 200 && res.StatusCode != 206 {
		return fileSeg{}, fmt.Errorf("%s: %s", segURL.String(), res.Status)
	}
	slurp, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return fileSeg{}, err
	}

	var newContents []byte
	if int64(len(slurp)) == seg.Size {
		newContents = slurp
	} else if int64(len(have)+len(slurp)) == seg.Size {
		newContents = append(have, slurp...)
	}
	got224 := fmt.Sprintf("%x", sha256.Sum224(newContents))
	if got224 != seg.SHA224 {
		if len(have) == 0 {
			return fileSeg{}, errors.New("corrupt download")
		}
		// Try again
		os.Remove(partial)
		return ns.syncSeg(ctx, seg)
	}
	tf, err := ioutil.TempFile(ns.cacheDir, "tempseg")
	if err != nil {
		return fileSeg{}, err
	}
	if _, err := tf.Write(newContents); err != nil {
		return fileSeg{}, err
	}
	if err := tf.Close(); err != nil {
		return fileSeg{}, err
	}
	finalName := partial
	if !isFinalSeg {
		finalName = frozen
	}
	if err := os.Rename(tf.Name(), finalName); err != nil {
		return fileSeg{}, err
	}
	log.Printf("wrote %v", finalName)
	return fileSeg{seg.Number, finalName, seg.Size}, nil
}

type LogSegmentJSON struct {
	Number int    `json:"number"`
	Size   int64  `json:"size"`
	SHA224 string `json:"sha224"`
	URL    string `json:"url"`
}
