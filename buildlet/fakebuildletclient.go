// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"

	"golang.org/x/oauth2"
)

// Client is an interface that represent the methods exposed by client. The
// fake buildlet client should be used instead of client when testing things that
// use the client interface.
type Client interface {
	AddCloseFunc(fn func())
	Close() error
	ConnectSSH(user, authorizedPubKey string) (net.Conn, error)
	DestroyVM(ts oauth2.TokenSource, proj, zone, instance string) error
	Exec(ctx context.Context, cmd string, opts ExecOpts) (remoteErr, execErr error)
	GCEInstanceName() string
	GetTar(ctx context.Context, dir string) (io.ReadCloser, error)
	IPPort() string
	IsBroken() bool
	ListDir(ctx context.Context, dir string, opts ListDirOpts, fn func(DirEntry)) error
	MarkBroken()
	Name() string
	ProxyRoundTripper() http.RoundTripper
	ProxyTCP(port int) (io.ReadWriteCloser, error)
	Put(ctx context.Context, r io.Reader, path string, mode os.FileMode) error
	PutTar(ctx context.Context, r io.Reader, dir string) error
	PutTarFromURL(ctx context.Context, tarURL, dir string) error
	RemoteName() string
	RemoveAll(ctx context.Context, paths ...string) error
	SetDescription(v string)
	SetDialer(dialer func(context.Context) (net.Conn, error))
	SetGCEInstanceName(v string)
	SetHTTPClient(httpClient *http.Client)
	SetName(name string)
	SetOnHeartbeatFailure(fn func())
	SetReleaseMode(v bool)
	Status(ctx context.Context) (Status, error)
	String() string
	URL() string
	WorkDir(ctx context.Context) (string, error)
}

var errUnimplemented = errors.New("unimplemented function")

var _ Client = (*FakeClient)(nil)

// FakeClient is a fake buildlet client used for testing. Not all functions are implemented.
type FakeClient struct {
	name         string
	instanceName string
	closeFuncs   []func()
}

// AddCloseFunc adds optional extra code to run on close for the fake buildlet.
func (fc *FakeClient) AddCloseFunc(fn func()) {
	fc.closeFuncs = append(fc.closeFuncs, fn)
}

// Close is a fake client closer.
func (fc *FakeClient) Close() error {
	for _, f := range fc.closeFuncs {
		f()
	}
	return nil
}

// ConnectSSH connects to a fake SSH server.
func (fc *FakeClient) ConnectSSH(user, authorizedPubKey string) (net.Conn, error) {
	return nil, errUnimplemented
}

// DestroyVM destroys a fake VM.
func (fc *FakeClient) DestroyVM(ts oauth2.TokenSource, proj, zone, instance string) error {
	return errUnimplemented
}

// Exec fakes the execution.
func (fc *FakeClient) Exec(ctx context.Context, cmd string, opts ExecOpts) (remoteErr, execErr error) {
	return nil, errUnimplemented
}

// GCEInstanceName gives the fake instance name.
func (fc *FakeClient) GCEInstanceName() string { return fc.instanceName }

// GetTar gives a vake tar zipped directory.
func (fc *FakeClient) GetTar(ctx context.Context, dir string) (io.ReadCloser, error) {
	return nil, errUnimplemented
}

// IPPort provides a fake ip and port pair.
func (fc *FakeClient) IPPort() string { return "" }

// IsBroken returns a fake broken response.
func (fc *FakeClient) IsBroken() bool { return false }

// ListDir lists a directory on a fake buildlet.
func (fc *FakeClient) ListDir(ctx context.Context, dir string, opts ListDirOpts, fn func(DirEntry)) error {
	return errUnimplemented
}

// MarkBroken marks the fake client as broken.
func (fc *FakeClient) MarkBroken() {}

// Name is the name of the fake client.
func (fc *FakeClient) Name() string { return fc.name }

// ProxyRoundTripper provides a fake proxy.
func (fc *FakeClient) ProxyRoundTripper() http.RoundTripper { return nil }

// ProxyTCP provides a fake proxy.
func (fc *FakeClient) ProxyTCP(port int) (io.ReadWriteCloser, error) { return nil, errUnimplemented }

// Put places a file on a fake buildlet.
func (fc *FakeClient) Put(ctx context.Context, r io.Reader, path string, mode os.FileMode) error {
	return errUnimplemented
}

// PutTar fakes putting  a tar zipped file on a buildldet.
func (fc *FakeClient) PutTar(ctx context.Context, r io.Reader, dir string) error {
	return errUnimplemented
}

// PutTarFromURL fakes putting a tar zipped file on a builelt.
func (fc *FakeClient) PutTarFromURL(ctx context.Context, tarURL, dir string) error {
	return nil
}

// RemoteName gives the remote name of the fake buildlet.
func (fc *FakeClient) RemoteName() string { return "" }

// SetDescription sets the description on a fake client.
func (fc *FakeClient) SetDescription(v string) {}

// SetDialer sets the function that creates a new connection to the fake buildlet.
func (fc *FakeClient) SetDialer(dialer func(context.Context) (net.Conn, error)) {}

// SetGCEInstanceName sets the GCE instance name on a fake client.
func (fc *FakeClient) SetGCEInstanceName(v string) {
	fc.instanceName = v
}

// SetHTTPClient sets the HTTP client on a fake client.
func (fc *FakeClient) SetHTTPClient(httpClient *http.Client) {}

// SetName sets the name on a fake client.
func (fc *FakeClient) SetName(name string) {
	fc.name = name
}

// SetOnHeartbeatFailure sets a function to be called when heartbeats against this fake buildlet fail.
func (fc *FakeClient) SetOnHeartbeatFailure(fn func()) {}

// SetReleaseMode sets the release mode on a fake client.
func (fc *FakeClient) SetReleaseMode(v bool) {}

// Status provides a status on the fake client.
func (fc *FakeClient) Status(ctx context.Context) (Status, error) { return Status{}, errUnimplemented }

// String provides a fake string representation of the client.
func (fc *FakeClient) String() string { return "" }

// URL is the URL for a fake buildlet.
func (fc *FakeClient) URL() string { return "" }

// WorkDir is the working directory for the fake buildlet.
func (fc *FakeClient) WorkDir(ctx context.Context) (string, error) { return "", errUnimplemented }

// RemoveAll deletes the provided paths, relative to the work directory for a fake buildlet.
func (fc *FakeClient) RemoveAll(ctx context.Context, paths ...string) error { return errUnimplemented }
