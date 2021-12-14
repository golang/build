// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin
// +build linux darwin

package gomote

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/coordinator/schedule"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type scheduler interface {
	State() (st schedule.SchedulerState)
	WaiterState(waiter *schedule.SchedItem) (ws types.BuildletWaitStatus)
	GetBuildlet(ctx context.Context, si *schedule.SchedItem) (buildlet.Client, error)
}

// Server is a gomote server implementation.
type Server struct {
	// embed the unimplemented server.
	protos.UnimplementedGomoteServiceServer

	buildlets *remote.SessionPool
	scheduler scheduler
}

// New creates a gomote server.
func New(rsp *remote.SessionPool, sched *schedule.Scheduler) *Server {
	return &Server{
		buildlets: rsp,
		scheduler: sched,
	}
}

// Authenticate will allow the caller to verify that they are properly authenticated and authorized to interact with the
// Service.
func (s *Server) Authenticate(ctx context.Context, req *protos.AuthenticateRequest) (*protos.AuthenticateResponse, error) {
	_, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("Authenticate access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	return &protos.AuthenticateResponse{}, nil
}

// CreateInstance will create a gomote instance for the authenticated user.
func (s *Server) CreateInstance(req *protos.CreateInstanceRequest, stream protos.GomoteService_CreateInstanceServer) error {
	ctx, cancel := context.WithTimeout(stream.Context(), 5*time.Minute)
	defer cancel()

	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("CreateInstance access.IAPFromContext(ctx) = nil, %s", err)
		return status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	if req.GetBuilderType() == "" {
		return status.Errorf(codes.InvalidArgument, "invalid builder type")
	}
	bconf, ok := dashboard.Builders[req.GetBuilderType()]
	if !ok {
		return status.Errorf(codes.InvalidArgument, "unknown builder type")
	}
	if bconf.IsRestricted() && !isPrivilegedUser(creds.Email) {
		return status.Errorf(codes.PermissionDenied, "user is unable to create gomote of that builder type")
	}
	si := &schedule.SchedItem{
		HostType: bconf.HostType,
		IsGomote: true,
	}
	type result struct {
		buildletClient buildlet.Client
		err            error
	}
	rc := make(chan result, 1)
	go func() {
		bc, err := s.scheduler.GetBuildlet(ctx, si)
		rc <- result{bc, err}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return status.Errorf(codes.DeadlineExceeded, "timed out waiting for gomote instance to be created")
		case <-ticker.C:
			st := s.scheduler.WaiterState(si)
			err := stream.Send(&protos.CreateInstanceResponse{
				Status:       protos.CreateInstanceResponse_WAITING,
				WaitersAhead: int64(st.Ahead),
			})
			if err != nil {
				return status.Errorf(codes.Internal, "unable to stream result: %s", err)
			}
		case r := <-rc:
			if r.err != nil {
				log.Printf("error creating gomote buildlet: %v", err)

				return status.Errorf(codes.Unknown, "gomote creation failed: %s", err)
			}
			userName, err := emailToUser(creds.Email)
			if err != nil {
				status.Errorf(codes.Internal, "invalid user email format")
			}
			gomoteID := s.buildlets.AddSession(creds.ID, userName, req.GetBuilderType(), bconf.HostType, r.buildletClient)
			log.Printf("created buildlet %v for %v (%s)", gomoteID, userName, r.buildletClient.String())
			session, err := s.buildlets.Session(gomoteID)
			if err != nil {
				return status.Errorf(codes.Internal, "unable to query for gomote timeout") // this should never happen
			}
			err = stream.Send(&protos.CreateInstanceResponse{
				Instance: &protos.Instance{
					GomoteId:    gomoteID,
					BuilderType: req.GetBuilderType(),
					HostType:    bconf.HostType,
					Expires:     session.Expires.Unix(),
				},
				Status:       protos.CreateInstanceResponse_COMPLETE,
				WaitersAhead: 0,
			})
			if err != nil {
				return status.Errorf(codes.Internal, "unable to stream result: %s", err)
			}
			return nil
		}
	}
}

// ListInstances will list the gomote instances owned by the requester. The requester must be authenticated.
func (s *Server) ListInstances(ctx context.Context, req *protos.ListInstancesRequest) (*protos.ListInstancesResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("ListInstances access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	var instances []*protos.Instance
	for _, s := range s.buildlets.List() {
		if s.OwnerID != creds.ID {
			continue
		}
		instances = append(instances, &protos.Instance{
			GomoteId:    s.ID,
			BuilderType: s.BuilderType,
			HostType:    s.HostType,
			Expires:     s.Expires.Unix(),
		})
	}
	return &protos.ListInstancesResponse{
		Instances: instances,
	}, nil
}

// isPrivilagedUser returns true if the user is using a Google account.
// The user has to be a part of the appropriate IAM group.
func isPrivilegedUser(email string) bool {
	if strings.HasSuffix(email, "@google.com") {
		return true
	}
	return false
}

// iapEmailRE matches the email string returned by Identity Aware Proxy for sessions where
// the authority is Google.
var iapEmailRE = regexp.MustCompile(`^accounts\.google\.com:.+@.+\..+$`)

// emailToUser returns the displayed user for the IAP email string passed in.
// For example, "accounts.google.com:example@gmail.com" -> "example"
func emailToUser(email string) (string, error) {
	if match := iapEmailRE.MatchString(email); !match {
		return "", errors.New("invalid email format")
	}
	return email[strings.Index(email, ":")+1 : strings.LastIndex(email, "@")], nil
}
