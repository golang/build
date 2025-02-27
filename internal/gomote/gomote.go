// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package gomote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/pool/queue"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/coordinator/schedule"
	"golang.org/x/build/internal/envutil"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/types"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type scheduler interface {
	State() (st schedule.SchedulerState)
	WaiterState(waiter *queue.SchedItem) (ws types.BuildletWaitStatus)
	GetBuildlet(ctx context.Context, si *queue.SchedItem) (buildlet.Client, error)
}

// bucketHandle interface used to enable testing of the storage.bucketHandle.
type bucketHandle interface {
	GenerateSignedPostPolicyV4(object string, opts *storage.PostPolicyV4Options) (*storage.PostPolicyV4, error)
	SignedURL(object string, opts *storage.SignedURLOptions) (string, error)
	Object(name string) *storage.ObjectHandle
}

// Server is a gomote server implementation.
type Server struct {
	// embed the unimplemented server.
	protos.UnimplementedGomoteServiceServer

	bucket                  bucketHandle
	buildlets               *remote.SessionPool
	gceBucketName           string
	scheduler               scheduler
	sshCertificateAuthority ssh.Signer
}

// New creates a gomote server. If the rawCAPriKey is invalid, the program will exit.
func New(rsp *remote.SessionPool, sched *schedule.Scheduler, rawCAPriKey []byte, gomoteGCSBucket string, storageClient *storage.Client) *Server {
	signer, err := ssh.ParsePrivateKey(rawCAPriKey)
	if err != nil {
		log.Fatalf("unable to parse raw certificate authority private key into signer=%s", err)
	}
	return &Server{
		bucket:                  storageClient.Bucket(gomoteGCSBucket),
		buildlets:               rsp,
		gceBucketName:           gomoteGCSBucket,
		scheduler:               sched,
		sshCertificateAuthority: signer,
	}
}

// AddBootstrap adds the bootstrap version of Go to an instance and returns the URL for the bootstrap version. If no
// bootstrap version is defined then the returned version URL will be empty.
func (s *Server) AddBootstrap(ctx context.Context, req *protos.AddBootstrapRequest) (*protos.AddBootstrapResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("AddBootstrap access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	ses, bc, err := s.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	bconf, ok := dashboard.Builders[ses.BuilderType]
	if !ok {
		return nil, status.Errorf(codes.Internal, "unknown builder type")
	}
	url := bconf.GoBootstrapURL(buildenv.Production)
	if url == "" {
		return &protos.AddBootstrapResponse{}, nil
	}
	if err = bc.PutTarFromURL(ctx, url, "go1.4"); err != nil {
		return nil, status.Errorf(codes.Internal, "unable to download bootstrap Go")
	}
	return &protos.AddBootstrapResponse{BootstrapGoUrl: url}, nil
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
	creds, err := access.IAPFromContext(stream.Context())
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
	if ((!bconf.HostConfig().IsHermetic() && bconf.HostConfig().IsGoogle()) || bconf.IsRestricted()) && !isPrivilegedUser(creds.Email) {
		return status.Errorf(codes.PermissionDenied, "user is unable to create gomote of that builder type")
	}
	userName, err := emailToUser(creds.Email)
	if err != nil {
		return status.Errorf(codes.Internal, "invalid user email format")
	}
	si := &queue.SchedItem{
		HostType:  bconf.HostType,
		IsGomote:  true,
		IsRelease: userName == "relui-prod",
		User:      creds.Email,
	}
	type result struct {
		buildletClient buildlet.Client
		err            error
	}
	rc := make(chan result, 1)
	go func() {
		bc, err := s.scheduler.GetBuildlet(stream.Context(), si)
		rc <- result{bc, err}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stream.Context().Done():
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
				log.Printf("error creating gomote buildlet: %v", r.err)

				return status.Errorf(codes.Unknown, "gomote creation failed: %s", r.err)
			}
			gomoteID := s.buildlets.AddSession(creds.ID, userName, req.GetBuilderType(), bconf.HostType, r.buildletClient)
			log.Printf("created buildlet %v for %v (%s)", gomoteID, userName, r.buildletClient.String())
			session, err := s.buildlets.Session(gomoteID)
			if err != nil {
				return status.Errorf(codes.Internal, "unable to query for gomote timeout") // this should never happen
			}
			wd, err := r.buildletClient.WorkDir(stream.Context())
			if err != nil {
				return status.Errorf(codes.Internal, "could not read working dir: %v", err)
			}
			err = stream.Send(&protos.CreateInstanceResponse{
				Instance: &protos.Instance{
					GomoteId:    gomoteID,
					BuilderType: req.GetBuilderType(),
					HostType:    bconf.HostType,
					Expires:     session.Expires.Unix(),
					WorkingDir:  wd,
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

// InstanceAlive will ensure that the gomote instance is still alive and will extend the timeout. The requester must be authenticated.
func (s *Server) InstanceAlive(ctx context.Context, req *protos.InstanceAliveRequest) (*protos.InstanceAliveResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("InstanceAlive access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	if req.GetGomoteId() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid gomote ID")
	}
	_, err = s.session(req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	if err := s.buildlets.RenewTimeout(req.GetGomoteId()); err != nil {
		return nil, status.Errorf(codes.Internal, "unable to renew timeout")
	}
	return &protos.InstanceAliveResponse{}, nil
}

// ListDirectory lists the contents of the directory on a gomote instance.
func (s *Server) ListDirectory(ctx context.Context, req *protos.ListDirectoryRequest) (*protos.ListDirectoryResponse, error) {
	entries, err := s.listDirectory(ctx, req)
	if err != nil {
		return nil, err
	}
	return &protos.ListDirectoryResponse{
		Entries: entries,
	}, nil
}

// ListDirectoryStreaming lists the contents of the directory on a gomote instance.
func (s *Server) ListDirectoryStreaming(req *protos.ListDirectoryRequest, stream grpc.ServerStreamingServer[protos.ListDirectoryResponse]) error {
	entries, err := s.listDirectory(stream.Context(), req)
	if err != nil {
		return err
	}
	// This could use slices.Chunk, once our go.mod is on go1.23 or higher.
	const chunkSize = 100
	for i := 0; i < len(entries); i += chunkSize {
		end := min(chunkSize, len(entries[i:]))
		if err := stream.Send(&protos.ListDirectoryResponse{
			Entries: entries[i : i+end],
		}); err != nil {
			return err
		}
	}
	return nil
}

// listDirectory lists the contents of the directory on a gomote instance.
func (s *Server) listDirectory(ctx context.Context, req *protos.ListDirectoryRequest) ([]string, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	if req.GetGomoteId() == "" || req.GetDirectory() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid arguments")
	}
	_, bc, err := s.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	opt := buildlet.ListDirOpts{
		Recursive: req.GetRecursive(),
		Digest:    req.GetDigest(),
		Skip:      req.GetSkipFiles(),
	}
	var entries []string
	if err = bc.ListDir(context.Background(), req.GetDirectory(), opt, func(bi buildlet.DirEntry) {
		entries = append(entries, bi.String())
	}); err != nil {
		return nil, status.Errorf(codes.Unimplemented, "method ListDirectory not implemented")
	}
	return entries, nil
}

// ListInstances will list the gomote instances owned by the requester. The requester must be authenticated.
func (s *Server) ListInstances(ctx context.Context, req *protos.ListInstancesRequest) (*protos.ListInstancesResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("ListInstances access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	res := &protos.ListInstancesResponse{}
	for _, s := range s.buildlets.List() {
		if s.OwnerID != creds.ID {
			continue
		}
		res.Instances = append(res.Instances, &protos.Instance{
			GomoteId:    s.ID,
			BuilderType: s.BuilderType,
			HostType:    s.HostType,
			Expires:     s.Expires.Unix(),
		})
	}
	return res, nil
}

// DestroyInstance will destroy a gomote instance. It will ensure that the caller is authenticated and is the owner of the instance
// before it destroys the instance.
func (s *Server) DestroyInstance(ctx context.Context, req *protos.DestroyInstanceRequest) (*protos.DestroyInstanceResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("DestroyInstance access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	if req.GetGomoteId() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid gomote ID")
	}
	_, err = s.session(req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	if err := s.buildlets.DestroySession(req.GetGomoteId()); err != nil {
		log.Printf("DestroyInstance remote.DestroySession(%s) = %s", req.GetGomoteId(), err)
		return nil, status.Errorf(codes.Internal, "unable to destroy gomote instance")
	}
	return &protos.DestroyInstanceResponse{}, nil
}

// ExecuteCommand will execute a command on a gomote instance. The output from the command will be streamed back to the caller if the output is set.
func (s *Server) ExecuteCommand(req *protos.ExecuteCommandRequest, stream protos.GomoteService_ExecuteCommandServer) error {
	creds, err := access.IAPFromContext(stream.Context())
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	ses, bc, err := s.sessionAndClient(stream.Context(), req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return err
	}
	builderType := req.GetImitateHostType()
	if builderType == "" {
		builderType = ses.BuilderType
	}
	conf, ok := dashboard.Builders[builderType]
	if !ok {
		return status.Errorf(codes.Internal, "unable to retrieve configuration for instance")
	}
	remoteErr, execErr := bc.Exec(stream.Context(), req.GetCommand(), buildlet.ExecOpts{
		Dir:         req.GetDirectory(),
		SystemLevel: req.GetSystemLevel(),
		Output: &streamWriter{writeFunc: func(p []byte) (int, error) {
			err := stream.Send(&protos.ExecuteCommandResponse{
				Output: p,
			})
			if err != nil {
				return 0, fmt.Errorf("unable to send data=%w", err)
			}
			return len(p), nil
		}},
		Args:     req.GetArgs(),
		ExtraEnv: envutil.Dedup(conf.GOOS(), append(conf.Env(), req.GetAppendEnvironment()...)),
		Debug:    req.GetDebug(),
		Path:     req.GetPath(),
	})
	if execErr != nil {
		// there were system errors preventing the command from being started or seen to completion.
		return status.Errorf(codes.Aborted, "unable to execute command: %s", execErr)
	}
	if remoteErr != nil {
		// the command succeeded remotely
		return status.Errorf(codes.Unknown, "command execution failed: %s", remoteErr)
	}
	return nil
}

// streamWriter implements the io.Writer interface.
type streamWriter struct {
	writeFunc func(p []byte) (int, error)
}

// Write calls the writeFunc function with the same arguments passed to the Write function.
func (sw *streamWriter) Write(p []byte) (int, error) {
	return sw.writeFunc(p)
}

// ReadTGZToURL retrieves a directory from the gomote instance and writes the file to GCS. It returns a signed URL which the caller uses
// to read the file from GCS.
func (s *Server) ReadTGZToURL(ctx context.Context, req *protos.ReadTGZToURLRequest) (*protos.ReadTGZToURLResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	_, bc, err := s.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	tgz, err := bc.GetTar(ctx, req.GetDirectory())
	if err != nil {
		return nil, status.Errorf(codes.Aborted, "unable to retrieve tar from gomote instance: %s", err)
	}
	defer tgz.Close()
	objectName := uuid.NewString()
	objectHandle := s.bucket.Object(objectName)
	// A context for writes is used to ensure we can cancel the context if a
	// problem is encountered while writing to the object store. The API documentation
	// states that the context should be canceled to stop writing without saving the data.
	writeCtx, cancel := context.WithCancel(ctx)
	tgzWriter := objectHandle.NewWriter(writeCtx)
	defer cancel()
	if _, err = io.Copy(tgzWriter, tgz); err != nil {
		return nil, status.Errorf(codes.Aborted, "unable to stream tar.gz: %s", err)
	}
	// when close is called, the object is stored in the bucket.
	if err := tgzWriter.Close(); err != nil {
		return nil, status.Errorf(codes.Aborted, "unable to store object: %s", err)
	}
	url, err := s.signURLForDownload(objectName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to create signed URL for download: %s", err)
	}
	return &protos.ReadTGZToURLResponse{
		Url: url,
	}, nil
}

// RemoveFiles removes files or directories from the gomote instance.
func (s *Server) RemoveFiles(ctx context.Context, req *protos.RemoveFilesRequest) (*protos.RemoveFilesResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("RemoveFiles access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	// TODO(go.dev/issue/48742) consider what additional path validation should be implemented.
	if req.GetGomoteId() == "" || len(req.GetPaths()) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid arguments")
	}
	_, bc, err := s.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	if err := bc.RemoveAll(ctx, req.GetPaths()...); err != nil {
		log.Printf("RemoveFiles buildletClient.RemoveAll(ctx, %q) = %s", req.GetPaths(), err)
		return nil, status.Errorf(codes.Unknown, "unable to remove files")
	}
	return &protos.RemoveFilesResponse{}, nil
}

// SignSSHKey signs the public SSH key with a certificate. The signed public SSH key is intended for use with the gomote service SSH
// server. It will be signed by the certificate authority of the server and will restrict access to the gomote instance that it was
// signed for.
func (s *Server) SignSSHKey(ctx context.Context, req *protos.SignSSHKeyRequest) (*protos.SignSSHKeyResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	session, err := s.session(req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	signedPublicKey, err := remote.SignPublicSSHKey(ctx, s.sshCertificateAuthority, req.GetPublicSshKey(), session.ID, session.OwnerID, 5*time.Minute)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unable to sign ssh key")
	}
	return &protos.SignSSHKeyResponse{
		SignedPublicSshKey: signedPublicKey,
	}, nil
}

// UploadFile creates a URL and a set of HTTP post fields which are used to upload a file to a staging GCS bucket. Uploaded files are made available to the
// gomote instances via a subsequent call to one of the WriteFromURL endpoints.
func (s *Server) UploadFile(ctx context.Context, req *protos.UploadFileRequest) (*protos.UploadFileResponse, error) {
	_, err := access.IAPFromContext(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	objectName := uuid.NewString()
	url, fields, err := s.signURLForUpload(objectName)
	if err != nil {
		log.Printf("unable to create signed URL: %s", err)
		return nil, status.Errorf(codes.Internal, "unable to create signed url")
	}
	return &protos.UploadFileResponse{
		Url:        url,
		Fields:     fields,
		ObjectName: objectName,
	}, nil
}

// signURLForUpload generates a signed URL and a set of http Post fields to be used to upload an object to GCS without authenticating.
func (s *Server) signURLForUpload(object string) (url string, fields map[string]string, err error) {
	if object == "" {
		return "", nil, errors.New("invalid object name")
	}
	pv4, err := s.bucket.GenerateSignedPostPolicyV4(object, &storage.PostPolicyV4Options{
		Expires:  time.Now().Add(10 * time.Minute),
		Insecure: false,
	})
	if err != nil {
		return "", nil, fmt.Errorf("unable to generate signed url: %w", err)
	}
	return pv4.URL, pv4.Fields, nil
}

// signURLForDownload generates a signed URL and fields to be used to upload an object to GCS without authenticating.
func (s *Server) signURLForDownload(object string) (url string, err error) {
	url, err = s.bucket.SignedURL(object, &storage.SignedURLOptions{
		Expires: time.Now().Add(10 * time.Minute),
		Method:  http.MethodGet,
		Scheme:  storage.SigningSchemeV4,
	})
	if err != nil {
		return "", fmt.Errorf("unable to generate signed url: %w", err)
	}
	return url, nil
}

// WriteFileFromURL initiates an HTTP request to the passed in URL and streams the contents of the request to the gomote instance.
func (s *Server) WriteFileFromURL(ctx context.Context, req *protos.WriteFileFromURLRequest) (*protos.WriteFileFromURLResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("WriteTGZFromURL access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	_, bc, err := s.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	var rc io.ReadCloser
	// objects stored in the gomote staging bucket are only accessible when you have been granted explicit permissions. A builder
	// requires a signed URL in order to access objects stored in the gomote staging bucket.
	if onObjectStore(s.gceBucketName, req.GetUrl()) {
		object, err := objectFromURL(s.gceBucketName, req.GetUrl())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid object URL")
		}
		rc, err = s.bucket.Object(object).NewReader(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "unable to create object reader: %s", err)
		}
	} else {
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, req.GetUrl(), nil)
		// TODO(amedee) find sane client defaults, possibly rely on context timeout in request.
		client := &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSHandshakeTimeout: 5 * time.Second,
			},
		}
		resp, err := client.Do(httpRequest)
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "failed to get file from URL: %s", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, status.Errorf(codes.Aborted, "unable to get file from %q: response code: %d", req.GetUrl(), resp.StatusCode)
		}
		rc = resp.Body
	}
	defer rc.Close()
	if err := bc.Put(ctx, rc, req.GetFilename(), fs.FileMode(req.GetMode())); err != nil {
		return nil, status.Errorf(codes.Aborted, "failed to send the file to the gomote instance: %s", err)
	}
	return &protos.WriteFileFromURLResponse{}, nil
}

// WriteTGZFromURL will instruct the gomote instance to download the tar.gz from the provided URL. The tar.gz file will be unpacked in the work directory
// relative to the directory provided.
func (s *Server) WriteTGZFromURL(ctx context.Context, req *protos.WriteTGZFromURLRequest) (*protos.WriteTGZFromURLResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("WriteTGZFromURL access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	if req.GetGomoteId() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid gomote ID")
	}
	if req.GetUrl() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "missing URL")
	}
	_, bc, err := s.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	url := req.GetUrl()
	if onObjectStore(s.gceBucketName, url) {
		object, err := objectFromURL(s.gceBucketName, url)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid URL")
		}
		url, err = s.signURLForDownload(object)
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "unable to sign url for download: %s", err)
		}
	}
	if err := bc.PutTarFromURL(ctx, url, req.GetDirectory()); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "unable to write tar.gz: %s", err)
	}
	return &protos.WriteTGZFromURLResponse{}, nil
}

// session is a helper function that retrieves a session associated with the gomoteID and ownerID.
func (s *Server) session(gomoteID, ownerID string) (*remote.Session, error) {
	session, err := s.buildlets.Session(gomoteID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "specified gomote instance does not exist")
	}
	if session.OwnerID != ownerID {
		return nil, status.Errorf(codes.PermissionDenied, "not allowed to modify this gomote session")
	}
	return session, nil
}

// sessionAndClient is a helper function that retrieves a session and buildlet client for the
// associated gomoteID and ownerID. The gomote instance timeout is renewed if the gomote id and owner id
// are valid.
func (s *Server) sessionAndClient(ctx context.Context, gomoteID, ownerID string) (*remote.Session, buildlet.Client, error) {
	session, err := s.session(gomoteID, ownerID)
	if err != nil {
		return nil, nil, err
	}
	bc, err := s.buildlets.BuildletClient(gomoteID)
	if err != nil {
		return nil, nil, status.Errorf(codes.NotFound, "specified gomote instance does not exist")
	}
	if err := s.buildlets.KeepAlive(ctx, gomoteID); err != nil {
		log.Printf("gomote: unable to keep alive %s: %s", gomoteID, err)
	}
	return session, bc, nil
}

// isPrivilegedUser returns true if the user is trusted to use sensitive machines.
// The user has to be a part of the appropriate IAM group.
func isPrivilegedUser(email string) bool {
	return strings.HasSuffix(email, "@google.com") || strings.HasSuffix(email, "@symbolic-datum-552.iam.gserviceaccount.com") ||
		strings.HasSuffix(email, "@go-security-trybots.iam.gserviceaccount.com")
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

// onObjectStore returns true if the url is for an object on GCS.
func onObjectStore(bucketName, url string) bool {
	return strings.HasPrefix(url, fmt.Sprintf("https://storage.googleapis.com/%s/", bucketName))
}

// objectFromURL returns the object name for an object on GCS.
func objectFromURL(bucketName, url string) (string, error) {
	if !onObjectStore(bucketName, url) {
		return "", errors.New("URL not for gomote transfer bucket")
	}
	objectName := strings.TrimPrefix(url, fmt.Sprintf("https://storage.googleapis.com/%s/", bucketName))
	return objectName, nil
}
