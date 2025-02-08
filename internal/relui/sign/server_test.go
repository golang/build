// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sign

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/relui/protos"
	"golang.org/x/net/nettest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestUpdateSigningStatus(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client, server := setupSigningTest(t, ctx)
	var wg1, wg2 sync.WaitGroup
	wg1.Add(1)
	wg2.Add(3)
	clientContext, cancel := context.WithCancel(ctx)
	go fakeSigningServerClient(t, clientContext, client, &wg1)
	go signRequestSeries(t, ctx, "request-1", server, BuildGPG, &wg2)
	go signRequestSeries(t, ctx, "request-2", server, BuildMacOS, &wg2)
	go signRequestSeries(t, ctx, "request-3", server, BuildWindows, &wg2)
	wg2.Wait()
	cancel()
	wg1.Wait()
}

func TestUpdateSigningStatusError(t *testing.T) {
	t.Run("unauthenticated client", func(t *testing.T) {
		ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
		client, _ := setupSigningTest(t, ctx)
		stream, err := client.UpdateSigningStatus(context.Background())
		if err != nil {
			t.Fatalf("client.UpdateSigningStatus() = %v, %s; want no error", stream, err)
		}
		wantCode := codes.Unauthenticated
		if _, err = stream.Recv(); status.Code(err) != wantCode {
			t.Fatalf("stream.Recv() = %s, want %s", status.Code(err), wantCode)
		}
	})
	t.Run("non-existent signing request", func(t *testing.T) {
		ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
		client, server := setupSigningTest(t, ctx)
		var wg sync.WaitGroup
		wg.Add(1)
		clientContext, cancel := context.WithCancel(ctx)
		go fakeSigningServerClient(t, clientContext, client, &wg)
		defer func() {
			cancel()
			wg.Wait()
		}()
		if status, _, _, err := server.ArtifactSigningStatus(ctx, "not-exist"); err != nil {
			t.Fatalf("ArtifactSigningStatus(%q) failed: %v", "not-exist", err)
		} else if status != StatusNotFound {
			t.Fatalf("ArtifactSigningStatus(%q) returned status %v, want StatusNotFound", "not-exist", status)
		}
	})
}

func signRequestSeries(t *testing.T, ctx context.Context, id string, server *SigningServer, buildT BuildType, wg *sync.WaitGroup) {
	defer wg.Done()
	const uri = "gs://foo/bar.tar.gz"
	jobID, err := server.SignArtifact(ctx, buildT, []string{uri})
	if err != nil {
		t.Errorf("SignArtifact(%q, %v, %q) = %s; want no error", id, buildT, uri, err)
		return
	}
	_, _, _, err = server.ArtifactSigningStatus(ctx, jobID)
	if err != nil {
		t.Errorf("ArtifactSigningStatus(%q) = %s; want no error", id, err)
		return
	}
	_, _, _, err = server.ArtifactSigningStatus(ctx, jobID)
	if err != nil {
		t.Errorf("ArtifactSigningStatus(%q) = %s; want no error", id, err)
	}
}

// fakeSigningServerClient is a simple implementation of how we expect the client side to perform.
// A signing request initiates a signing job. The first status update should return a status of
// running. The second status request will return a status of Completed.
func fakeSigningServerClient(t *testing.T, ctx context.Context, client protos.ReleaseServiceClient, wg *sync.WaitGroup) {
	defer wg.Done()
	stream, err := client.UpdateSigningStatus(ctx)
	if err != nil {
		t.Errorf("client.UpdateSigningStatus() = %v, %s; want no error", stream, err)
		return
	}
	requests := make(chan *protos.SigningRequest, 10)
	go func() {
		defer close(requests)
		for {
			in, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil && status.Code(err) == codes.Canceled {
				return
			}
			if err != nil {
				t.Logf("client.stream.Recv() = nil, %s; want no error", err)
				return
			}
			requests <- in
		}
	}()
	jobs := make(map[string]int) // jobID -> request count
	for request := range requests {
		resp := &protos.SigningStatus{
			MessageId:   request.GetMessageId(),
			StatusOneof: nil,
		}
		switch r := request.RequestOneof.(type) {
		case *protos.SigningRequest_Sign:
			newJob := uuid.NewString() // Create a new pretend job with an ID.
			jobs[newJob] = 1
			resp.StatusOneof = &protos.SigningStatus_Started{
				Started: &protos.StatusStarted{
					JobId: newJob,
				},
			}
		case *protos.SigningRequest_Status:
			id := r.Status.JobId
			c, ok := jobs[id]
			if !ok {
				resp.StatusOneof = &protos.SigningStatus_NotFound{}
			} else if c == 1 {
				jobs[id] = 2
				resp.StatusOneof = &protos.SigningStatus_Running{}
			} else if c == 2 {
				delete(jobs, id)
				resp.StatusOneof = &protos.SigningStatus_Completed{
					Completed: &protos.StatusCompleted{
						GcsUri: []string{"gs://private-bucket/some-file.tar.gz"},
					},
				}
			}
		}
		if err := stream.Send(resp); err != nil {
			t.Errorf("client.stream.Send(%v) = %s, want no error", resp, err)
			return
		}
	}
	stream.CloseSend()
}

func setupSigningTest(t *testing.T, ctx context.Context) (protos.ReleaseServiceClient, *SigningServer) {
	lis, err := nettest.NewLocalListener("tcp")
	if err != nil {
		t.Fatalf(`nettest.NewLocalListener("tcp") = %s; want no error`, err)
	}
	sopts := access.FakeIAPAuthInterceptorOptions()
	s := grpc.NewServer(sopts...)
	ss := NewServer()
	protos.RegisterReleaseServiceServer(s, ss)
	go s.Serve(lis)
	// create GRPC client
	copts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	conn, err := grpc.DialContext(ctx, lis.Addr().String(), copts...)
	if err != nil {
		lis.Close()
		t.Fatalf("grpc.Dial(%s, %+v) = nil, %s; want no error", lis.Addr().String(), copts, err)
	}
	sc := protos.NewReleaseServiceClient(conn)
	t.Cleanup(func() {
		conn.Close()
		s.Stop()
		lis.Close()
	})
	return sc, ss
}

func fakeAuthContext(ctx context.Context) context.Context {
	return access.ContextWithIAP(ctx, access.IAPFields{
		Email: "accounts.google.com:relui-user@google.com",
		ID:    "accounts.google.com:randomuuidstuff",
	})
}

func fakeIAP() access.IAPFields {
	return fakeIAPWithUser("example", "randomuuidstuff")
}

func fakeIAPWithUser(user string, id string) access.IAPFields {
	return access.IAPFields{
		Email: fmt.Sprintf("accounts.google.com:%s@gmail.com", user),
		ID:    fmt.Sprintf("accounts.google.com:%s", id),
	}
}
