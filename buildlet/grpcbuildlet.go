// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"

	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GRPCCoordinatorClient struct {
	Client protos.GomoteServiceClient
}

func (c *GRPCCoordinatorClient) CreateBuildlet(ctx context.Context, builderType string) (RemoteClient, error) {
	return c.CreateBuildletWithStatus(ctx, builderType, func(types.BuildletWaitStatus) {})
}

func (c *GRPCCoordinatorClient) CreateBuildletWithStatus(ctx context.Context, builderType string, status func(types.BuildletWaitStatus)) (RemoteClient, error) {
	stream, err := c.Client.CreateInstance(ctx, &protos.CreateInstanceRequest{BuilderType: builderType})
	if err != nil {
		return nil, err
	}
	var instance *protos.Instance
	for {
		update, err := stream.Recv()
		switch {
		case err == io.EOF:
			return &grpcBuildlet{
				client:  c.Client,
				id:      instance.GetGomoteId(),
				workDir: instance.GetWorkingDir(),
			}, nil
		case err != nil:
			return nil, err
		case update.GetStatus() != protos.CreateInstanceResponse_COMPLETE:
			status(types.BuildletWaitStatus{
				Ahead: int(update.WaitersAhead),
			})

		case update.GetStatus() == protos.CreateInstanceResponse_COMPLETE:
			instance = update.GetInstance()

		}
	}
}

type grpcBuildlet struct {
	client  protos.GomoteServiceClient
	id      string
	workDir string
}

var _ RemoteClient = (*grpcBuildlet)(nil)

func (b *grpcBuildlet) Close() error {
	_, err := b.client.DestroyInstance(context.TODO(), &protos.DestroyInstanceRequest{
		GomoteId: b.id,
	})
	return err
}

func (b *grpcBuildlet) Exec(ctx context.Context, cmd string, opts ExecOpts) (remoteErr error, execErr error) {
	stream, err := b.client.ExecuteCommand(ctx, &protos.ExecuteCommandRequest{
		GomoteId:          b.id,
		Command:           cmd,
		SystemLevel:       opts.SystemLevel,
		Debug:             opts.Debug,
		AppendEnvironment: opts.ExtraEnv,
		Path:              opts.Path,
		Directory:         opts.Dir,
		Args:              opts.Args,
	})
	if err != nil {
		return nil, err
	}
	if opts.OnStartExec != nil {
		opts.OnStartExec()
	}
	for {
		update, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			// Execution error.
			if status.Code(err) == codes.Aborted {
				return nil, err
			}
			// Unknown, presumed command error.
			return err, nil
		}
		if opts.Output != nil {
			opts.Output.Write(update.Output)
		}
	}
}

func (b *grpcBuildlet) GetTar(ctx context.Context, dir string) (io.ReadCloser, error) {
	resp, err := b.client.ReadTGZToURL(ctx, &protos.ReadTGZToURLRequest{
		GomoteId:  b.id,
		Directory: dir,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resp.GetUrl(), nil)
	if err != nil {
		return nil, fmt.Errorf("server returned invalid URL %q: %v", resp.GetUrl(), err)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error fetching tgz: %v", err)
	}
	if r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status reading tgz: %v", r.Status)
	}
	return r.Body, nil
}

func (b *grpcBuildlet) ListDir(ctx context.Context, dir string, opts ListDirOpts, fn func(DirEntry)) error {
	resp, err := b.client.ListDirectory(ctx, &protos.ListDirectoryRequest{
		GomoteId:  b.id,
		Directory: dir,
		Recursive: opts.Recursive,
		SkipFiles: opts.Skip,
		Digest:    opts.Digest,
	})
	if err != nil {
		return err
	}
	for _, ent := range resp.Entries {
		fn(DirEntry{ent})
	}
	return nil
}

func (b *grpcBuildlet) Put(ctx context.Context, r io.Reader, path string, mode os.FileMode) error {
	url, err := b.upload(ctx, r)
	if err != nil {
		return err
	}
	_, err = b.client.WriteFileFromURL(ctx, &protos.WriteFileFromURLRequest{
		GomoteId: b.id,
		Url:      url,
		Filename: path,
		Mode:     uint32(mode),
	})
	return err
}

func (b *grpcBuildlet) PutTar(ctx context.Context, r io.Reader, dir string) error {
	url, err := b.upload(ctx, r)
	if err != nil {
		return err
	}
	_, err = b.client.WriteTGZFromURL(ctx, &protos.WriteTGZFromURLRequest{
		GomoteId:  b.id,
		Url:       url,
		Directory: dir,
	})
	return err
}

func (b *grpcBuildlet) PutTarFromURL(ctx context.Context, url string, dir string) error {
	_, err := b.client.WriteTGZFromURL(ctx, &protos.WriteTGZFromURLRequest{
		GomoteId:  b.id,
		Url:       url,
		Directory: dir,
	})
	return err
}

func (b *grpcBuildlet) upload(ctx context.Context, r io.Reader) (string, error) {
	resp, err := b.client.UploadFile(ctx, &protos.UploadFileRequest{})
	if err != nil {
		return "", err
	}

	buf := new(bytes.Buffer)
	mw := multipart.NewWriter(buf)
	for k, v := range resp.Fields {
		if err := mw.WriteField(k, v); err != nil {
			return "", fmt.Errorf("unable to write field: %s", err)
		}
	}
	_, err = mw.CreateFormFile("file", resp.ObjectName)
	if err != nil {
		return "", fmt.Errorf("unable to create form file: %s", err)
	}
	// Write our own boundary to avoid buffering entire file into the multipart Writer
	bound := fmt.Sprintf("\r\n--%s--\r\n", mw.Boundary())
	req, err := http.NewRequestWithContext(ctx, "POST", resp.Url, io.NopCloser(io.MultiReader(buf, r, strings.NewReader(bound))))
	if err != nil {
		return "", fmt.Errorf("unable to create request: %s", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request failed: %s", err)
	}
	if res.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("http post failed: status code=%d", res.StatusCode)
	}
	return resp.Url + resp.ObjectName, nil
}

func (b *grpcBuildlet) ProxyTCP(port int) (io.ReadWriteCloser, error) {
	return nil, fmt.Errorf("TCP proxying unimplemented in grpc")
}

func (b *grpcBuildlet) RemoteName() string {
	return b.id
}

func (b *grpcBuildlet) RemoveAll(ctx context.Context, paths ...string) error {
	_, err := b.client.RemoveFiles(ctx, &protos.RemoveFilesRequest{
		GomoteId: b.id,
		Paths:    paths,
	})
	return err
}

func (b *grpcBuildlet) WorkDir(ctx context.Context) (string, error) {
	return b.workDir, nil
}
