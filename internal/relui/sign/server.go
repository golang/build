// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sign

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"

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
	callback *responseCallback
	requests chan *protos.SigningRequest
}

// NewServer creates a GRPC signing server used to send signing requests and
// signing status requests to a client.
func NewServer() *SigningServer {
	return &SigningServer{
		requests: make(chan *protos.SigningRequest, 1),
		callback: &responseCallback{
			registry: make(map[string]func(*signResponse)),
		},
	}
}

// UpdateSigningStatus uses a bidirectional streaming connection to send signing requests to the client and
// and receive status updates on signing requests. There is no specific order which the requests or responses
// need to occur in. The connection returns an error once the context is canceled or an error is encountered.
func (rs *SigningServer) UpdateSigningStatus(stream protos.ReleaseService_UpdateSigningStatusServer) error {
	_, err := access.IAPFromContext(stream.Context())
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	g, ctx := errgroup.WithContext(stream.Context())
	g.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return nil
			case request := <-rs.requests:
				if err := stream.Send(request); err != nil {
					rs.callback.callAndDeregister(&signResponse{
						err:       err,
						messageID: request.GetMessageId(),
					})
					return status.Errorf(codes.Internal, "sending request failed")
				}
			}
		}
	})
	g.Go(func() error {
		for {
			in, err := stream.Recv()
			if err == io.EOF {
				return nil
			} else if err != nil {
				return err
			}
			rs.callback.callAndDeregister(&signResponse{
				messageID: in.GetMessageId(),
				status:    in,
			})
		}
	})
	if err := g.Wait(); err == nil {
		log.Printf("SigningServer: UpdateSigningStatus=%s", err)
		return err
	}
	return nil
}

// send is called when sending a request to the client calling the server. This will return either the response or a
// timeout error when the context has been canceled.
func (rs *SigningServer) send(ctx context.Context, req *protos.SigningRequest) (*protos.SigningStatus, error) {
	respCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var resp *signResponse
	rs.callback.register(req.GetMessageId(), func(r *signResponse) {
		resp = r
		cancel()
	})

	// send message
	select {
	case rs.requests <- req:
		log.Printf("SigningServer: sent signing request with message=%q", req.GetMessageId())
	case <-ctx.Done():
		rs.callback.deregister(req.GetMessageId())
		return nil, ctx.Err()
	}
	<-respCtx.Done()
	return resp.status, resp.err
}

// SignArtifact implements Service.
func (rs *SigningServer) SignArtifact(ctx context.Context, bt BuildType, objectURI []string) (jobID string, _ error) {
	resp, err := rs.send(ctx, &protos.SigningRequest{
		MessageId: uuid.NewString(),
		RequestOneof: &protos.SigningRequest_Sign{
			Sign: &protos.SignArtifactRequest{
				BuildType: bt.proto(),
				GcsUri:    objectURI,
			},
		},
	})
	if err != nil {
		return "", err
	}
	switch t := resp.StatusOneof.(type) {
	case *protos.SigningStatus_Started:
		return t.Started.JobId, nil
	default:
		return "", fmt.Errorf("unexpected response type %T for a sign request", t)
	}
}

// ArtifactSigningStatus implements Service.
func (rs *SigningServer) ArtifactSigningStatus(ctx context.Context, jobID string) (status Status, objectURI []string, err error) {
	resp, err := rs.send(ctx, &protos.SigningRequest{
		MessageId: uuid.NewString(),
		RequestOneof: &protos.SigningRequest_Status{
			Status: &protos.SignArtifactStatusRequest{JobId: jobID},
		},
	})
	if err != nil {
		return StatusUnknown, nil, err
	}
	switch t := resp.StatusOneof.(type) {
	case *protos.SigningStatus_Completed:
		status = StatusCompleted
		objectURI = t.Completed.GetGcsUri()
	case *protos.SigningStatus_Failed:
		status = StatusFailed
	case *protos.SigningStatus_NotFound:
		status = StatusNotFound
		err = fmt.Errorf("signing request not found for message=%q", resp.GetMessageId())
	case *protos.SigningStatus_Running:
		status = StatusRunning
	default:
		return 0, nil, fmt.Errorf("unexpected response type %T for a status request", t)
	}
	return status, objectURI, err
}

// CancelSigning implements Service.
func (rs *SigningServer) CancelSigning(ctx context.Context, jobID string) error {
	_, err := rs.send(ctx, &protos.SigningRequest{
		MessageId: uuid.NewString(),
		RequestOneof: &protos.SigningRequest_Cancel{
			Cancel: &protos.SignArtifactCancelRequest{
				JobId: jobID,
			},
		},
	})
	return err
}

// signResponse contains the response and error from a signing request.
type signResponse struct {
	err       error
	messageID string
	status    *protos.SigningStatus
}

// responseCallback manages the message ID to callback association used to
// respond to previously created requests to a channel.
type responseCallback struct {
	mu sync.Mutex
	// registry is a map of message_id -> callback
	registry map[string]func(*signResponse)
}

// register creates a message ID to channel association.
func (c *responseCallback) register(messageID string, f func(*signResponse)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.registry[messageID] = f
}

// deregister removes the channel to message ID association.
func (c *responseCallback) deregister(messageID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.registry, messageID)
}

// callAndDeregister calls the callback associated with the message ID. If no
// callback is registered for the message ID, the response is dropped. The callback
// registration is always removed if it exists.
func (c *responseCallback) callAndDeregister(resp *signResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	respFunc, ok := c.registry[resp.status.GetMessageId()]
	if ok {
		delete(c.registry, resp.status.GetMessageId())
	}
	if !ok {
		// drop the message
		log.Printf("SigningServer: caller not found for message=%q", resp.status.GetMessageId())
		return
	}
	respFunc(resp)
}
