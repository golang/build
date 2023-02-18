// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sign

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/relui/protos"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var _ Service = (*SigningServer)(nil)

// SigningServer is a GRPC signing server used to send signing requests and
// signing status requests to a client.
type SigningServer struct {
	protos.UnimplementedReleaseServiceServer

	// requests is a channel of outgoing signing requests to be sent to
	// any of the connected signing clients.
	requests chan *protos.SigningRequest

	// callback is a map of the message ID to callback association used to
	// respond to previously created requests to a channel.
	callbackMu sync.Mutex
	callback   map[string]func(*signResponse) // Key is message ID.
}

// NewServer creates a GRPC signing server used to send signing requests and
// signing status requests to a client.
func NewServer() *SigningServer {
	return &SigningServer{
		requests: make(chan *protos.SigningRequest),
		callback: make(map[string]func(*signResponse)),
	}
}

// UpdateSigningStatus uses a bidirectional streaming connection to send signing requests to the client
// and receive status updates on signing requests. There is no specific order which the requests or responses
// need to occur in. The connection returns an error once the context is canceled or an error is encountered.
func (rs *SigningServer) UpdateSigningStatus(stream protos.ReleaseService_UpdateSigningStatusServer) error {
	iap, err := access.IAPFromContext(stream.Context())
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}

	t := time.Now()
	log.Printf("SigningServer: a client connected (iap = %+v)\n", iap)
	g, ctx := errgroup.WithContext(stream.Context())
	g.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case req := <-rs.requests:
				err := stream.Send(req)
				if err != nil {
					rs.callAndDeregister(req.GetMessageId(), &signResponse{err: err})
					return err
				}
			}
		}
	})
	g.Go(func() error {
		for {
			resp, err := stream.Recv()
			if err != nil {
				return err
			}
			rs.callAndDeregister(resp.GetMessageId(), &signResponse{status: resp})
		}
	})
	err = g.Wait()
	log.Printf("SigningServer: a client disconnected after %v (err = %v)\n", time.Since(t), err)
	return err
}

// do sends a signing request and returns the corresponding signing response.
// It blocks until a response is received or the context times out or is canceled.
func (rs *SigningServer) do(ctx context.Context, req *protos.SigningRequest) (resp *protos.SigningStatus, err error) {
	t := time.Now()
	defer func() {
		if err == nil {
			log.Printf("SigningServer: successfully round-tripped message=%q in %v:\n  req = %v\n  resp = %v\n", req.GetMessageId(), time.Since(t), req, resp)
		} else {
			log.Printf("SigningServer: communication error %v for message=%q after %v:\n  req = %v\n", err, req.GetMessageId(), time.Since(t), req)
		}
	}()

	// Register where to send the response for this message ID.
	respCh := make(chan *signResponse, 1) // Room for one response.
	rs.register(req.GetMessageId(), func(r *signResponse) { respCh <- r })

	// Send the request.
	select {
	case rs.requests <- req:
	case <-ctx.Done():
		rs.deregister(req.GetMessageId())
		return nil, ctx.Err()
	}

	// Wait for the response.
	select {
	case resp := <-respCh:
		return resp.status, resp.err
	case <-ctx.Done():
		rs.deregister(req.GetMessageId())
		return nil, ctx.Err()
	}
}

// SignArtifact implements Service.
func (rs *SigningServer) SignArtifact(ctx context.Context, bt BuildType, objectURI []string) (jobID string, _ error) {
	resp, err := rs.do(ctx, &protos.SigningRequest{
		MessageId: uuid.NewString(),
		RequestOneof: &protos.SigningRequest_Sign{Sign: &protos.SignArtifactRequest{
			BuildType: bt.proto(),
			GcsUri:    objectURI,
		}},
	})
	if err != nil {
		return "", err
	}
	switch t := resp.StatusOneof.(type) {
	case *protos.SigningStatus_Started:
		return t.Started.JobId, nil
	case *protos.SigningStatus_Failed:
		return "", fmt.Errorf("failed to start %v signing on %q: %s", bt, objectURI, t.Failed.GetDescription())
	default:
		return "", fmt.Errorf("unexpected response type %T for a sign request", t)
	}
}

// ArtifactSigningStatus implements Service.
func (rs *SigningServer) ArtifactSigningStatus(ctx context.Context, jobID string) (_ Status, desc string, objectURI []string, _ error) {
	resp, err := rs.do(ctx, &protos.SigningRequest{
		MessageId: uuid.NewString(),
		RequestOneof: &protos.SigningRequest_Status{Status: &protos.SignArtifactStatusRequest{
			JobId: jobID,
		}},
	})
	if err != nil {
		return StatusUnknown, "", nil, err
	}
	switch t := resp.StatusOneof.(type) {
	case *protos.SigningStatus_Completed:
		return StatusCompleted, "", t.Completed.GetGcsUri(), nil
	case *protos.SigningStatus_Failed:
		return StatusFailed, t.Failed.GetDescription(), nil, nil
	case *protos.SigningStatus_NotFound:
		return StatusNotFound, fmt.Sprintf("signing job %q not found", jobID), nil, nil
	case *protos.SigningStatus_Running:
		return StatusRunning, t.Running.GetDescription(), nil, nil
	default:
		return 0, "", nil, fmt.Errorf("unexpected response type %T for a status request", t)
	}
}

// CancelSigning implements Service.
func (rs *SigningServer) CancelSigning(ctx context.Context, jobID string) error {
	_, err := rs.do(ctx, &protos.SigningRequest{
		MessageId: uuid.NewString(),
		RequestOneof: &protos.SigningRequest_Cancel{Cancel: &protos.SignArtifactCancelRequest{
			JobId: jobID,
		}},
	})
	return err
}

// signResponse contains the response and error from a signing request.
type signResponse struct {
	status *protos.SigningStatus
	err    error
}

// register creates a message ID to channel association.
func (s *SigningServer) register(messageID string, f func(*signResponse)) {
	s.callbackMu.Lock()
	s.callback[messageID] = f
	s.callbackMu.Unlock()
}

// deregister removes the channel to message ID association.
func (s *SigningServer) deregister(messageID string) {
	s.callbackMu.Lock()
	delete(s.callback, messageID)
	s.callbackMu.Unlock()
}

// callAndDeregister calls the callback associated with the message ID.
// If no callback is registered for the message ID, the response is dropped.
// The callback registration is always removed if it exists.
func (s *SigningServer) callAndDeregister(messageID string, resp *signResponse) {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()

	respFunc, ok := s.callback[messageID]
	if !ok {
		// drop the message
		log.Printf("SigningServer: caller not found for message=%q", messageID)
		return
	}
	delete(s.callback, messageID)
	respFunc(resp)
}
