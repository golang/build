// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package revdial implements a Dialer and Listener which work together
// to turn an accepted connection (for instance, a Hijacked HTTP request) into
// a Dialer which can then create net.Conns connecting back to the original
// dialer, which then gets a net.Listener accepting those conns.
//
// This is basically a very minimal SOCKS5 client & server.
//
// The motivation is that sometimes you want to run a server on a
// machine deep inside a NAT. Rather than connecting to the machine
// directly (which you can't, because of the NAT), you have the
// sequestered machine connect out to a public machine. Both sides
// then use revdial and the public machine can become a client for the
// NATed machine.
//
// Deprecated: this package should not be used and actually can no
// longer be used: half of its code has been deleted as it's no longer
// in use. We kept the half still needed by cmd/coordinator for old
// buildlet clients, but the new buildlet no longer uses this.
// Instead, callers should use the revdial/v2 version that is based on
// a different design without the flow control issues.
package revdial

/*
Protocol:

7-byte frame header:

uint8: frame type
   0 new conn   (server to peer only)
   1 close conn (either way)
   2 write      (either way)
uint32: conn id  (coordinator chooses, no ack from peer)
uint16: length of rest of data (for all frame types)

TODO(bradfitz): health checking PING packet type? since we can't use
TCP keep-alives at this layer. I guess we can just assume our caller
set up TCP keep-alives or similar. But it's actually tedious/hard to
do.

*/

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// The Dialer can create new connections.
type Dialer struct {
	rw     *bufio.ReadWriter
	closer io.Closer

	mu     sync.Mutex // guards following, and writes to rw
	err    error      // non-nil when closed or peer dies
	closed bool
	conns  map[uint32]*conn
	nextID uint32
	donec  chan struct{}
}

// NewDialer returns the side of the connection which will initiate
// new connections. This will typically be the side which did the
// HTTP Hijack. The io.Closer is what gets closed by the Close
// or by any errors. It will typically be the hijacked Conn.
func NewDialer(rw *bufio.ReadWriter, c io.Closer) *Dialer {
	d := &Dialer{
		rw:     rw,
		closer: c,
		conns:  map[uint32]*conn{},
		nextID: 1, // just for debugging, not seeing zeros
		donec:  make(chan struct{}),
	}
	go func() {
		err := readFrames(rw.Reader, d)
		if err == nil {
			err = errors.New("revdial: Dialer.readFrames terminated with success")
		}
		d.closeWithError(err)
	}()
	return d
}

// Done returns a channel which is closed when d is either closed or closed
// by the peer.
func (d *Dialer) Done() <-chan struct{} { return d.donec }

var errDialerClosed = errors.New("revdial: Dialer closed")

// Close closes the Dialer and all still-open connections from it.
func (d *Dialer) Close() error {
	return d.closeWithError(errDialerClosed)
}

func (d *Dialer) closeWithError(err error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.err = err
	for _, c := range d.conns {
		// TODO(bradfitz): propagate err to peers. For now they'll just fail with
		// EOF, which works but isn't as nice as it could be.
		c.peerClose()
	}
	closeErr := d.closer.Close()
	close(d.donec)

	if err == errDialerClosed || err == nil {
		return closeErr
	}
	return closeErr
}

func (d *Dialer) conn(id uint32) (*conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.conns[id]
	if !ok {
		return nil, fmt.Errorf("revdial.Dialer saw reference to unknown conn %v", id)
	}
	return c, nil
}

var (
	errRole = errors.New("revdial: invalid frame type received for role")
)

func (d *Dialer) onFrame(f frame) error {
	switch f.command {
	case frameNewConn:
		return errRole
	case frameCloseConn:
		c, err := d.conn(f.connID)
		if err != nil {
			// Oh well.
			return nil
		}
		c.peerClose()
		return nil
	case frameWrite:
		c, err := d.conn(f.connID)
		if err != nil {
			// Ignore writes on bogus conn IDs; assume it
			// just recently closed.
			return nil
		}
		if _, err := c.peerWrite(f.payload); err != nil {
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if closed {
				// Conn is now closed. Assume error
				// was "io: read/write on closed pipe"
				// and it was just data in-flight
				// while this side closed. So, don't abort
				// the frame-reading loop.
				return nil
			}
			return err
		}
		return nil
	default:
		// Ignore unknown frame types.
	}
	return nil
}

// Dial creates a new connection back to the Listener.
func (d *Dialer) Dial() (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, errors.New("revdial: Dial on closed client")
	}
	var id uint32
	for {
		id = d.nextID
		d.nextID++ // wrapping is okay; we check for free ones, assuming sparse
		if _, inUse := d.conns[id]; inUse {
			continue
		}
		break
	}
	c := &conn{
		id:        id,
		wmu:       &d.mu,
		w:         d.rw.Writer,
		unregConn: d.unregConn,
	}
	c.cond = sync.NewCond(&c.mu)
	d.conns[id] = c
	err := writeFrame(c, frame{
		command: frameNewConn,
		connID:  id,
	})
	return c, err
}

// c.wmu must be held.
func writeFrame(c *conn, f frame) error {
	if len(f.payload) > 0xffff {
		return errors.New("revdial: frame too long")
	}
	w := c.w
	hdr := [7]byte{
		byte(f.command),
		byte(f.connID >> 24),
		byte(f.connID >> 16),
		byte(f.connID >> 8),
		byte(f.connID),
		byte(len(f.payload) >> 8),
		byte(len(f.payload)),
	}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.Write(f.payload); err != nil {
		return err
	}
	return w.Flush()
}

type conn struct {
	id uint32

	wmu       *sync.Mutex // held while writing & calling unreg
	w         *bufio.Writer
	unregConn func(id uint32) // called with wmu held

	mu        sync.Mutex
	cond      *sync.Cond
	buf       []byte // unread data
	eof       bool   // remote side closed
	closed    bool   // our side closed (with Close)
	rdeadline time.Time
	wdeadline time.Time
	rtimer    *time.Timer
	wtimer    *time.Timer
}

func (c *conn) LocalAddr() net.Addr  { return fakeAddr{} }
func (c *conn) RemoteAddr() net.Addr { return fakeAddr{} }

func (c *conn) SetDeadline(t time.Time) error {
	rerr := c.SetReadDeadline(t)
	werr := c.SetWriteDeadline(t)
	if rerr != nil {
		return rerr
	}
	return werr
}

func (c *conn) SetWriteDeadline(t time.Time) error {
	defer c.cond.Signal()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("SetWriteDeadline called on closed connection")
	}
	c.stopWriteTimerLocked()
	c.wdeadline = t
	now := time.Now()
	if t.After(now) {
		c.wtimer = time.AfterFunc(t.Sub(now), c.cond.Broadcast)
	}
	return nil
}

func (c *conn) SetReadDeadline(t time.Time) error {
	defer c.cond.Signal()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("SetReadDeadline called on closed connection")
	}
	c.stopReadTimerLocked()
	c.rdeadline = t
	now := time.Now()
	if t.After(now) {
		c.rtimer = time.AfterFunc(t.Sub(now), c.cond.Broadcast)
	}
	return nil
}

func (c *conn) stopReadTimerLocked() {
	if c.rtimer != nil {
		c.rtimer.Stop()
		c.rtimer = nil
	}
}

func (c *conn) stopWriteTimerLocked() {
	if c.wtimer != nil {
		c.wtimer.Stop()
		c.wtimer = nil
	}
}

func (c *conn) Close() error {
	defer c.cond.Broadcast()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.stopReadTimerLocked()
	c.stopWriteTimerLocked()
	c.closed = true
	c.mu.Unlock()

	c.wmu.Lock()
	c.unregConn(c.id)
	defer c.wmu.Unlock()
	return writeFrame(c, frame{
		command: frameCloseConn,
		connID:  c.id,
	})
}

func (d *Dialer) unregConn(id uint32) {
	delete(d.conns, id)
}

func (c *conn) peerWrite(p []byte) (n int, err error) {
	defer c.cond.Signal()
	c.mu.Lock()
	defer c.mu.Unlock()
	// TODO(bradfitz): bound this, like http2's buffer/pipe code
	c.buf = append(c.buf, p...)
	return len(p), nil
}

func (c *conn) peerClose() {
	defer c.cond.Broadcast()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eof = true
}

var errDeadline net.Error = deadlineError{}

type deadlineError struct{}

func (deadlineError) Error() string   { return "revdial: Read/Write deadline expired" }
func (deadlineError) Temporary() bool { return false }
func (deadlineError) Timeout() bool   { return true }

func (c *conn) Read(p []byte) (n int, err error) {
	defer c.cond.Signal() // for when writers block
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		n = copy(p, c.buf)
		c.buf = c.buf[:copy(c.buf, c.buf[n:])] // slide down
		if dl := c.rdeadline; !dl.IsZero() {
			if time.Now().After(dl) {
				return n, errDeadline
			}
		}
		if c.closed {
			return n, errors.New("revdial: Read on closed connection")
		}
		if len(c.buf) == 0 && c.eof {
			return n, io.EOF
		}
		if n > 0 || len(p) == 0 {
			return n, nil
		}
		c.cond.Wait()
	}
}

func (c *conn) Write(p []byte) (n int, err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, errors.New("revdial: Write on Closed conn")
	}
	dl := c.wdeadline
	if !dl.IsZero() && time.Now().After(dl) {
		c.mu.Unlock()
		// TODO: better write deadline support. do it per chunk, push it down
		// to underlying net.Conn (which involves changing API to let caller
		// supply a net.Conn)
		return 0, errDeadline
	}
	c.mu.Unlock()

	var timeout <-chan time.Time
	if !dl.IsZero() {
		timer := time.NewTimer(time.Until(dl))
		defer timer.Stop()
		timeout = timer.C
	}
	type result struct {
		n   int
		err error
	}
	res := make(chan result, 1)
	go func() {
		const max = 0xffff // max chunk size
		n := 0
		for len(p) > 0 {
			chunk := p
			if len(chunk) > max {
				chunk = chunk[:max]
			}
			c.wmu.Lock()
			err = writeFrame(c, frame{
				command: frameWrite,
				connID:  c.id,
				payload: chunk,
			})
			c.wmu.Unlock()
			if err != nil {
				res <- result{n, err}
				return
			}
			n += len(chunk)
			p = p[len(chunk):]
		}
		res <- result{n: n}
	}()
	select {
	case v := <-res:
		return v.n, v.err
	case <-timeout:
		println("timeout for " + dl.String())
		return 0, errDeadline
	}
}

type frameType uint8

const (
	frameNewConn   frameType = 'N'
	frameCloseConn frameType = 'C'
	frameWrite     frameType = 'W'
)

type frame struct {
	command frameType
	connID  uint32
	payload []byte // not owned
}

func (f frame) String() string {
	p := f.payload
	if len(p) > 64 {
		p = p[:64]
	}
	return fmt.Sprintf("[frame %q conn %v, %q]", f.command, f.connID, p)
}

// onFramer is the interface for something that can get callbacks on
// new frames being received.
type onFramer interface {
	onFrame(f frame) error
}

const debug = false

func readFrames(br *bufio.Reader, of onFramer) error {
	var hdr [7]byte
	var payload bytes.Buffer
	for {
		_, err := io.ReadFull(br, hdr[:])
		if err != nil {
			return err
		}
		f := frame{
			command: frameType(hdr[0]),
			connID:  binary.BigEndian.Uint32(hdr[1:5]),
		}
		paySize := binary.BigEndian.Uint16(hdr[5:7])
		if debug {
			log.Printf("Read frame header: %+v (len %v)", f, paySize)
		}
		payload.Reset()
		if paySize > 0 {
			if _, err := io.CopyN(&payload, br, int64(paySize)); err != nil {
				return err
			}
			if payload.Len() != int(paySize) {
				panic("invariant")
			}
		}
		f.payload = payload.Bytes()
		if debug {
			log.Printf("Read full frame: %+v (len %v)", f, paySize)
		}
		err = of.onFrame(f)
		if debug {
			log.Printf("onFrame = %v", err)
		}
		if err != nil {
			return err
		}
	}
}

type fakeAddr struct{}

func (fakeAddr) Network() string { return "revdial" }
func (fakeAddr) String() string  { return "revdialconn" }
