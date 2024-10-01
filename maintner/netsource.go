// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/maintner/internal/robustio"
	"golang.org/x/build/maintner/maintpb"
	"golang.org/x/build/maintner/reclog"
	"google.golang.org/protobuf/proto"
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

// TailNetworkMutationSource calls fn for all new mutations added to the log on server.
// Events with the End field set to true are not sent, so all events will
// have exactly one of Mutation or Err fields set to a non-zero value.
// It ignores prior events.
// If the server is restarted and its history diverges,
// TailNetworkMutationSource may return duplicate events. This therefore does not
// return a MutationSource, so it can't be accidentally misused for important things.
// TailNetworkMutationSource returns if fn returns an error, if ctx expires,
// or if it runs into a network error.
func TailNetworkMutationSource(ctx context.Context, server string, fn func(MutationStreamEvent) error) error {
	td, err := os.MkdirTemp("", "maintnertail")
	if err != nil {
		return err
	}
	defer robustio.RemoveAll(td)

	ns := NewNetworkMutationSource(server, td).(*netMutSource)
	ns.quiet = true
	getSegs := func(waitSizeNot int64) ([]LogSegmentJSON, error) {
		for {
			segs, err := ns.getServerSegments(ctx, waitSizeNot)
			if err != nil {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				// Sleep a minimum of 5 seconds before trying
				// again. The user's fn function might sleep
				// longer or shorter.
				timer := time.NewTimer(5 * time.Second)
				err := fn(MutationStreamEvent{Err: err})
				if err != nil {
					timer.Stop()
					return nil, err
				}
				<-timer.C
				continue
			}
			return segs, nil
		}
	}

	// See how long the log is at start. Then we'll only fetch
	// things after that.
	segs, err := getSegs(0)
	if err != nil {
		return err
	}
	segSize := sumJSONSegSize(segs)
	lastSeg := segs[len(segs)-1]
	if _, _, err := ns.syncSeg(ctx, lastSeg); err != nil {
		return err
	}

	ticker := time.NewTicker(time.Second) // max re-fetch interval
	defer ticker.Stop()
	for {
		segs, err := getSegs(segSize)
		if err != nil {
			return err
		}
		segSize = sumJSONSegSize(segs)

		for _, seg := range segs {
			if seg.Number < lastSeg.Number {
				continue
			}
			var off int64
			if seg.Number == lastSeg.Number {
				off = lastSeg.Size
			}
			_, newData, err := ns.syncSeg(ctx, seg)
			if err != nil {
				return err
			}
			if err := reclog.ForeachRecord(bytes.NewReader(newData), off, func(off int64, hdr, rec []byte) error {
				m := new(maintpb.Mutation)
				if err := proto.Unmarshal(rec, m); err != nil {
					return err
				}
				return fn(MutationStreamEvent{Mutation: m})
			}); err != nil {
				return err
			}
		}
		lastSeg = segs[len(segs)-1]

		<-ticker.C
	}
}

type netMutSource struct {
	server   string
	base     *url.URL
	cacheDir string

	last  []fileSeg
	quiet bool // disable verbose logging

	// Hooks for testing. If nil, unused:
	testHookGetServerSegments func(context.Context, int64) ([]LogSegmentJSON, error)
	testHookSyncSeg           func(context.Context, LogSegmentJSON) (fileSeg, []byte, error)
	testHookOnSplit           func(sumCommon int64)
	testHookFilePrefixSum224  func(file string, n int64) string
}

func (ns *netMutSource) GetMutations(ctx context.Context) <-chan MutationStreamEvent {
	ch := make(chan MutationStreamEvent, 50)
	go func() {
		err := ns.fetchAndSendMutations(ctx, ch)
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

// isNoInternetError reports whether the provided error is because there's no
// network connectivity.
func isNoInternetError(err error) bool {
	if err == nil {
		return false
	}
	switch err := err.(type) {
	case fetchError:
		return isNoInternetError(err.Err)
	case *url.Error:
		return isNoInternetError(err.Err)
	case *net.OpError:
		return isNoInternetError(err.Err)
	case *net.DNSError:
		// Trashy:
		return err.Err == "no such host"
	default:
		log.Printf("Unknown error type %T: %#v", err, err)
		return false
	}
}

func (ns *netMutSource) locallyCachedSegments() (segs []fileSeg, err error) {
	defer func() {
		if err != nil {
			log.Printf("No network connection and failed to use local cache: %v", err)
		} else {
			log.Printf("No network connection; using %d locally cached segments.", len(segs))
		}
	}()
	des, err := os.ReadDir(ns.cacheDir)
	if err != nil {
		return nil, err
	}
	fiMap := map[string]fs.FileInfo{}
	segHex := map[int]string{}
	segGrowing := map[int]bool{}
	for _, de := range des {
		name := de.Name()
		if !strings.HasSuffix(name, ".mutlog") {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			return nil, err
		}
		fiMap[name] = fi

		if len(name) == len("0000.6897fab4d3afcda332424b2a2a1a4469021074282bc7be5606aaa221.mutlog") {
			num, err := strconv.Atoi(name[:4])
			if err != nil {
				continue
			}
			segHex[num] = strings.TrimSuffix(name[5:], ".mutlog")
		} else if strings.HasSuffix(name, ".growing.mutlog") {
			num, err := strconv.Atoi(name[:4])
			if err != nil {
				continue
			}
			segGrowing[num] = true
		}
	}
	for num := 0; ; num++ {
		if hex, ok := segHex[num]; ok {
			name := fmt.Sprintf("%04d.%s.mutlog", num, hex)
			segs = append(segs, fileSeg{
				seg:    num,
				file:   filepath.Join(ns.cacheDir, name),
				size:   fiMap[name].Size(),
				sha224: hex,
			})
			continue
		}
		if segGrowing[num] {
			name := fmt.Sprintf("%04d.growing.mutlog", num)
			slurp, err := robustio.ReadFile(filepath.Join(ns.cacheDir, name))
			if err != nil {
				return nil, err
			}
			segs = append(segs, fileSeg{
				seg:    num,
				file:   filepath.Join(ns.cacheDir, name),
				size:   int64(len(slurp)),
				sha224: fmt.Sprintf("%x", sha256.Sum224(slurp)),
			})
		}
		return segs, nil
	}
}

// getServerSegments fetches the JSON logs handler (ns.server, usually
// https://maintner.golang.org/logs) and returns the parsed JSON.
// It sends the "waitsizenot" URL parameter, which specifies that the
// request should long-poll waiting for the server to have a sum of
// log segment sizes different than the value specified. As a result,
// it blocks until the server has new data to send or ctx expires.
//
// getServerSegments returns an error that matches fetchError with
// PossiblyRetryable set to true when it has signal that repeating
// the same call after some time may succeed.
func (ns *netMutSource) getServerSegments(ctx context.Context, waitSizeNot int64) ([]LogSegmentJSON, error) {
	if fn := ns.testHookGetServerSegments; fn != nil {
		return fn(ctx, waitSizeNot)
	}
	logsURL := fmt.Sprintf("%s?waitsizenot=%d", ns.server, waitSizeNot)
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", logsURL, nil)
		if err != nil {
			return nil, err
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fetchError{Err: err, PossiblyRetryable: true}
		}
		// When we're doing a long poll and the server replies
		// with a 304 response, that means the server is just
		// heart-beating us and trying to get a response back
		// within its various deadlines. But we should just
		// try again.
		if res.StatusCode == http.StatusNotModified {
			res.Body.Close()
			continue
		}
		defer res.Body.Close()
		if res.StatusCode/100 == 5 {
			// Consider a 5xx server response to possibly succeed later.
			return nil, fetchError{Err: fmt.Errorf("%s: %v", ns.server, res.Status), PossiblyRetryable: true}
		} else if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %v", ns.server, res.Status)
		}
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, fetchError{Err: err, PossiblyRetryable: true}
		}
		var segs []LogSegmentJSON
		err = json.Unmarshal(b, &segs)
		if err != nil {
			return nil, fmt.Errorf("unmarshaling %s JSON: %v", ns.server, err)
		}
		return segs, nil
	}
}

// getNewSegments fetches new mutations from the network mutation source.
// It tries to absorb the expected network bumps by trying multiple times,
// and returns an error only when it considers the problem to be terminal.
//
// If there's no internet connectivity from the start, it returns locally
// cached segments that might be available from before. Otherwise it waits
// for internet connectivity to come back and keeps going when it does.
func (ns *netMutSource) getNewSegments(ctx context.Context) ([]fileSeg, error) {
	sumLast := sumSegSize(ns.last)

	// First, fetch JSON metadata for the segments from the server.
	var serverSegs []LogSegmentJSON
	for try := 1; ; {
		segs, err := ns.getServerSegments(ctx, sumLast)
		if isNoInternetError(err) {
			if sumLast == 0 {
				return ns.locallyCachedSegments()
			}
			log.Printf("No internet; blocking.")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(15 * time.Second):
				try = 1
				continue
			}
		} else if fe := (fetchError{}); errors.As(err, &fe) && fe.PossiblyRetryable {
			// Fetching the JSON logs handler happens over an unreliable network connection,
			// and will fail at some point. Prefer to try again over reporting a terminal error.
			const maxTries = 5
			if try == maxTries {
				// At this point, promote it to a terminal error.
				return nil, fmt.Errorf("after %d attempts, fetching server segments still failed: %v", maxTries, err)
			}
			someDelay := time.Duration(try*try) * time.Second
			log.Printf("fetching server segments did not succeed on attempt %d, will try again in %v: %v", try, someDelay, err)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(someDelay):
				try++
				continue
			}
		} else if err != nil {
			return nil, err
		}
		serverSegs = segs
		break
	}
	// TODO: optimization: if already on GCE, skip sync to disk part and just
	// read from network. fast & free network inside.

	// Second, fetch the new segments or their fragments
	// that we don't yet have locally.
	var fileSegs []fileSeg
	for _, seg := range serverSegs {
		for try := 1; ; {
			fileSeg, _, err := ns.syncSeg(ctx, seg)
			if isNoInternetError(err) {
				log.Printf("No internet; blocking.")
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(15 * time.Second):
					try = 1
					continue
				}
			} else if fe := (fetchError{}); errors.As(err, &fe) && fe.PossiblyRetryable {
				// Syncing a segment fetches a good deal of data over a network connection,
				// and will fail at some point. Be very willing to try again at this layer,
				// since it's much more efficient than having GetMutations return an error
				// and possibly cause a higher level retry to redo significantly more work.
				const maxTries = 10
				if try == maxTries {
					// At this point, promote it to a terminal error.
					return nil, fmt.Errorf("after %d attempts, syncing segment %d still failed: %v", maxTries, seg.Number, err)
				}
				someDelay := time.Duration(try*try) * time.Second
				log.Printf("syncing segment %d did not succeed on attempt %d, will try again in %v: %v", seg.Number, try, someDelay, err)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(someDelay):
					try++
					continue
				}
			} else if err != nil {
				return nil, err
			}
			fileSegs = append(fileSegs, fileSeg)
			break
		}
	}

	// Verify consistency of newly fetched data,
	// and check there is in fact something new.
	sumCommon := ns.sumCommonPrefixSize(fileSegs, ns.last)
	if sumCommon != sumLast {
		if fn := ns.testHookOnSplit; fn != nil {
			fn(sumCommon)
		}
		// Our history diverged from the source.
		return nil, ErrSplit
	} else if sumCur := sumSegSize(fileSegs); sumCommon == sumCur {
		// Nothing new. This shouldn't happen since the maintnerd server is required to handle
		// the "?waitsizenot=NNN" long polling parameter, so it's a problem if we get here.
		return nil, fmt.Errorf("maintner.netsource: maintnerd server returned unchanged log segments")
	}
	ns.last = fileSegs

	newSegs := trimLeadingSegBytes(fileSegs, sumCommon)
	return newSegs, nil
}

func trimLeadingSegBytes(in []fileSeg, trim int64) []fileSeg {
	// First trim off whole segments, sharing the same underlying memory.
	for len(in) > 0 && trim >= in[0].size {
		trim -= in[0].size
		in = in[1:]
	}
	if len(in) == 0 {
		return nil
	}
	// Now copy, since we'll be modifying the first element.
	out := append([]fileSeg(nil), in...)
	out[0].skip = trim
	return out
}

// filePrefixSum224 returns the lowercase hex SHA-224 of the first n bytes of file.
func (ns *netMutSource) filePrefixSum224(file string, n int64) string {
	if fn := ns.testHookFilePrefixSum224; fn != nil {
		return fn(file, n)
	}
	f, err := os.Open(file)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Print(err)
		}
		return ""
	}
	defer f.Close()
	h := sha256.New224()
	_, err = io.CopyN(h, f, n)
	if err != nil {
		log.Print(err)
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func sumSegSize(segs []fileSeg) (sum int64) {
	for _, seg := range segs {
		sum += seg.size
	}
	return
}

func sumJSONSegSize(segs []LogSegmentJSON) (sum int64) {
	for _, seg := range segs {
		sum += seg.Size
	}
	return
}

// sumCommonPrefixSize computes the size of the longest common prefix of file segments a and b
// that can be found quickly by checking for matching checksums between segment boundaries.
func (ns *netMutSource) sumCommonPrefixSize(a, b []fileSeg) (sum int64) {
	for len(a) > 0 && len(b) > 0 {
		sa, sb := a[0], b[0]
		if sa.sha224 == sb.sha224 {
			// Whole chunk in common.
			sum += sa.size
			a, b = a[1:], b[1:]
			continue
		}
		if sa.size == sb.size {
			// If they're the same size but different
			// sums, it must've forked.
			return
		}
		// See if one chunk is a prefix of the other.
		// Make sa be the smaller one.
		if sb.size < sa.size {
			sa, sb = sb, sa
		}
		// Hash the beginning of the bigger size.
		bPrefixSum := ns.filePrefixSum224(sb.file, sa.size)
		if bPrefixSum == sa.sha224 {
			sum += sa.size
		}
		break
	}
	return
}

// fetchAndSendMutations fetches new mutations from the network mutation source
// and sends them to ch.
func (ns *netMutSource) fetchAndSendMutations(ctx context.Context, ch chan<- MutationStreamEvent) error {
	newSegs, err := ns.getNewSegments(ctx)
	if err != nil {
		return err
	}
	return foreachFileSeg(newSegs, func(seg fileSeg) error {
		f, err := os.Open(seg.file)
		if err != nil {
			return err
		}
		defer f.Close()
		if seg.skip > 0 {
			if _, err := f.Seek(seg.skip, io.SeekStart); err != nil {
				return err
			}
		}
		return reclog.ForeachRecord(io.LimitReader(f, seg.size-seg.skip), seg.skip, func(off int64, hdr, rec []byte) error {
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

// TODO: add a constructor for this? or simplify it. make it Size +
// File + embedded LogSegmentJSON?
type fileSeg struct {
	seg    int
	file   string // full path
	sha224 string
	skip   int64
	size   int64
}

// syncSeg syncs the provided log segment, returning its on-disk metadata.
// The newData result is the new data that was added to the segment in this sync.
//
// syncSeg returns an error that matches fetchError with PossiblyRetryable set
// to true when it has signal that repeating the same call after some time may
// succeed.
func (ns *netMutSource) syncSeg(ctx context.Context, seg LogSegmentJSON) (_ fileSeg, newData []byte, _ error) {
	if fn := ns.testHookSyncSeg; fn != nil {
		return fn(ctx, seg)
	}

	isFinalSeg := !strings.HasPrefix(seg.URL, "https://storage.googleapis.com/")
	relURL, err := url.Parse(seg.URL)
	if err != nil {
		return fileSeg{}, nil, err
	}
	segURL := ns.base.ResolveReference(relURL)

	frozen := filepath.Join(ns.cacheDir, fmt.Sprintf("%04d.%s.mutlog", seg.Number, seg.SHA224))

	// Do we already have it? Files named in their final form with the sha224 are considered
	// complete and immutable.
	if fi, err := os.Stat(frozen); err == nil && fi.Size() == seg.Size {
		return fileSeg{seg: seg.Number, file: frozen, size: fi.Size(), sha224: seg.SHA224}, nil, nil
	}

	// See how much data we already have in the partial growing file.
	partial := filepath.Join(ns.cacheDir, fmt.Sprintf("%04d.growing.mutlog", seg.Number))
	have, _ := robustio.ReadFile(partial)
	if int64(len(have)) == seg.Size {
		got224 := fmt.Sprintf("%x", sha256.Sum224(have))
		if got224 == seg.SHA224 {
			if !isFinalSeg {
				// This was growing for us, but the server started a new growing segment.
				if err := robustio.Rename(partial, frozen); err != nil {
					return fileSeg{}, nil, err
				}
				return fileSeg{seg: seg.Number, file: frozen, sha224: seg.SHA224, size: seg.Size}, nil, nil
			}
			return fileSeg{seg: seg.Number, file: partial, sha224: seg.SHA224, size: seg.Size}, nil, nil
		}
	}

	// Otherwise, download new data.
	if int64(len(have)) < seg.Size {
		req, err := http.NewRequestWithContext(ctx, "GET", segURL.String(), nil)
		if err != nil {
			return fileSeg{}, nil, err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", len(have), seg.Size-1))

		if !ns.quiet {
			log.Printf("Downloading %d bytes of %s ...", seg.Size-int64(len(have)), segURL)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return fileSeg{}, nil, fetchError{Err: err, PossiblyRetryable: true}
		}
		defer res.Body.Close()
		if res.StatusCode/100 == 5 {
			// Consider a 5xx server response to possibly succeed later.
			return fileSeg{}, nil, fetchError{Err: fmt.Errorf("%s: %s", segURL.String(), res.Status), PossiblyRetryable: true}
		} else if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
			return fileSeg{}, nil, fmt.Errorf("%s: %s", segURL.String(), res.Status)
		}
		newData, err = io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return fileSeg{}, nil, fetchError{Err: err, PossiblyRetryable: true}
		}
	}

	// Commit to disk.
	var newContents []byte
	if int64(len(newData)) == seg.Size {
		newContents = newData
	} else if int64(len(have)+len(newData)) == seg.Size {
		newContents = append(have, newData...)
	} else if int64(len(have)) > seg.Size {
		// We have more data than the server; likely because it restarted with uncommitted
		// transactions, and so we're headed towards an ErrSplit. Reuse the longest common
		// prefix as long as its checksum matches.
		newContents = have[:seg.Size]
	}
	got224 := fmt.Sprintf("%x", sha256.Sum224(newContents))
	if got224 != seg.SHA224 {
		if len(have) == 0 {
			return fileSeg{}, nil, errors.New("corrupt download")
		}
		// Try again.
		os.Remove(partial)
		return ns.syncSeg(ctx, seg)
	}
	// TODO: this is a quadratic amount of write I/O as the 16 MB
	// segment grows. Switch to appending to the existing file,
	// then perhaps encoding the desired file size into the
	// filename suffix (instead of just *.growing.mutlog) so
	// concurrent readers know where to stop.
	tf, err := os.CreateTemp(ns.cacheDir, "tempseg")
	if err != nil {
		return fileSeg{}, nil, err
	}
	if _, err := tf.Write(newContents); err != nil {
		return fileSeg{}, nil, err
	}
	if err := tf.Close(); err != nil {
		return fileSeg{}, nil, err
	}
	finalName := partial
	if !isFinalSeg {
		finalName = frozen
	}
	if err := robustio.Rename(tf.Name(), finalName); err != nil {
		return fileSeg{}, nil, err
	}
	if !ns.quiet {
		log.Printf("wrote %v", finalName)
	}
	return fileSeg{seg: seg.Number, file: finalName, size: seg.Size, sha224: seg.SHA224}, newData, nil
}

type LogSegmentJSON struct {
	Number int    `json:"number"`
	Size   int64  `json:"size"`
	SHA224 string `json:"sha224"`
	URL    string `json:"url"`
}

// fetchError records an error during a fetch operation over an unreliable network.
type fetchError struct {
	Err error // Non-nil.

	// PossiblyRetryable indicates whether Err is believed to be possibly caused by a
	// non-terminal network error, such that the caller can expect it may not happen
	// again if it simply tries the same fetch operation again after waiting a bit.
	PossiblyRetryable bool
}

func (e fetchError) Error() string { return e.Err.Error() }
func (e fetchError) Unwrap() error { return e.Err }
