// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package livelog

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

func TestBuffer(t *testing.T) {
	var wg sync.WaitGroup
	testConc := func(prefix string, r io.Reader, want string, wantErr error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			testRead(t, prefix, r, want, wantErr)
		}()
	}
	sleep := func() { time.Sleep(time.Millisecond) }

	const w1, w2, w3, w4 = "first", "second", "third", "fourth"

	var buf Buffer

	buf.Write([]byte(w1))
	r1 := buf.Reader()
	testRead(t, "r1, w1", r1, w1, nil)

	testConc("r1, w2", r1, w2, nil)
	sleep()
	buf.Write([]byte(w2))
	r2 := buf.Reader()
	testRead(t, "r2, w1+w2", r2, w1+w2, nil)
	wg.Wait()

	testConc("r1, w3", r1, w3, nil)
	testConc("r2, w3", r2, w3, nil)
	sleep()
	buf.Write([]byte(w3))
	wg.Wait()

	r3 := buf.Reader()
	testRead(t, "r3, w1+w2+w3", r3, w1+w2+w3, nil)

	testConc("r1, w4", r1, w4, nil)
	testConc("r3, eof", r3, "", io.EOF)
	sleep()
	r3.Close()
	buf.Write([]byte(w4))
	testRead(t, "r2, w4", r2, w4, nil)
	wg.Wait()

	testConc("r1 eof", r1, "", io.EOF)
	sleep()
	buf.Close()
	testRead(t, "r2 eof", r2, "", io.EOF)
	wg.Wait()

	r4 := buf.Reader()
	testRead(t, "r4 all", r4, w1+w2+w3+w4, nil)
	testRead(t, "r4 eof", r4, "", io.EOF)
}

func testRead(t *testing.T, prefix string, r io.Reader, want string, wantErr error) {
	b := make([]byte, 1024)
	n, err := r.Read(b)
	if err != wantErr {
		t.Errorf("%s: got err %v, want %v", prefix, err, wantErr)
		return
	}
	ok := true
	if n != len(want) {
		t.Errorf("%s: got n = %v, want %v", prefix, n, len(want))
		ok = false
	}
	if s := string(b[:n]); s != want {
		t.Errorf("%s: read %q, want %q", prefix, s, want)
		ok = false
	}
	if ok {
		t.Logf("%s: ok", prefix)
	}
}

func TestTruncation(t *testing.T) {
	tests := []struct {
		desc   string
		inputs [][]byte
		want   []byte
	}{
		{
			desc: "no truncation",
			inputs: [][]byte{
				bytes.Repeat([]byte{'a'}, maxUserSize),
			},
			want: bytes.Repeat([]byte{'a'}, maxUserSize),
		},
		{
			desc: "one byte overflow",
			inputs: [][]byte{
				bytes.Repeat([]byte{'a'}, maxUserSize),
				[]byte{'b'},
			},
			want: append(bytes.Repeat([]byte{'a'}, maxUserSize), []byte(truncationMessage)...),
		},
		{
			desc: "single overflow",
			inputs: [][]byte{
				bytes.Repeat([]byte{'a'}, 2*MaxBufferSize),
			},
			want: append(bytes.Repeat([]byte{'a'}, maxUserSize), []byte(truncationMessage)...),
		},
		{
			desc: "multiple overflow",
			inputs: [][]byte{
				bytes.Repeat([]byte{'a'}, 2*MaxBufferSize),
				bytes.Repeat([]byte{'a'}, 2*MaxBufferSize),
			},
			want: append(bytes.Repeat([]byte{'a'}, maxUserSize), []byte(truncationMessage)...),
		},
		{
			desc: "partial input overflow",
			inputs: [][]byte{
				bytes.Repeat([]byte{'a'}, maxUserSize-2),
				[]byte{'1', '2', '3'},
			},
			want: append(bytes.Repeat([]byte{'a'}, maxUserSize-2), append([]byte{'1', '2'}, []byte(truncationMessage)...)...),
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			var buf Buffer
			for _, input := range test.inputs {
				buf.Write(input)
			}

			got := buf.Bytes()
			if !bytes.Equal(got, test.want) {
				t.Errorf("buf inputs %v got %v, want %v", test.inputs, got, test.want)
			}
		})
	}
}
