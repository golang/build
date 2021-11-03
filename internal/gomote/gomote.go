// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gomote

import (
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/gomote/protos"
)

// Server is a gomote server implementation.
type Server struct {
	// embed the unimplemented server.
	protos.UnimplementedGomoteServiceServer

	buildlets *remote.SessionPool
}

// New creates a gomote server.
func New(rsp *remote.SessionPool) *Server {
	return &Server{
		buildlets: rsp,
	}
}
