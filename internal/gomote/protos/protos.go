// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package protos

// Run "go generate" in this directory to update. You need to have:
//
// a protoc binary (see https://github.com/protocolbuffers/protobuf/releases)
// protocol compiler plugins for Go:
// go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.26
// go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.1

//go:generate protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative gomote.proto
