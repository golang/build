// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package revdial

import (
	"bufio"
	"bytes"
	"io"
	"io/ioutil"
	"testing"
)

func TestDialer(t *testing.T) {
	pr, pw := io.Pipe()
	var out bytes.Buffer
	d := NewDialer(bufio.NewReadWriter(
		bufio.NewReader(pr),
		bufio.NewWriter(&out),
	), ioutil.NopCloser(nil))

	c, err := d.Dial()
	if err != nil {
		t.Fatal(err)
	}
	if c.(*conn).id != 1 {
		t.Fatalf("first id = %d; want 1", c.(*conn).id)
	}
	c.Close() // to verify incoming write frames don't block

	c, err = d.Dial()
	if err != nil {
		t.Fatal(err)
	}
	if c.(*conn).id != 2 {
		t.Fatalf("second id = %d; want 2", c.(*conn).id)
	}

	if g, w := len(d.conns), 1; g != w {
		t.Errorf("size of conns map after dial+close+dial = %v; want %v", g, w)
	}

	go func() {
		// Write "b" and then "ar", and read it as "bar"
		pw.Write([]byte{byte(frameWrite), 0, 0, 0, 2, 0, 1, 'b'})
		pw.Write([]byte{byte(frameWrite), 0, 0, 0, 1, 0, 1, 'x'}) // verify doesn't block first conn
		pw.Write([]byte{byte(frameWrite), 0, 0, 0, 2, 0, 2, 'a', 'r'})
	}()
	buf := make([]byte, 3)
	if n, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("ReadFul = %v (%q), %v", n, buf[:n], err)
	}
	if string(buf) != "bar" {
		t.Fatalf("read = %q; want bar", buf)
	}
	if _, err := io.WriteString(c, "hello, world"); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	want := "N\x00\x00\x00\x01\x00\x00" +
		"C\x00\x00\x00\x01\x00\x00" +
		"N\x00\x00\x00\x02\x00\x00" +
		"W\x00\x00\x00\x02\x00\fhello, world"
	if got != want {
		t.Errorf("Written on wire differs.\nWrote: %q\n Want: %q", got, want)
	}
}
