// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package protos

// Run "go generate" in this directory to update. You need to have:
//
// - a protoc binary (see https://github.com/golang/protobuf#installation)
// - go get -u github.com/golang/protobuf/protoc-gen-go

//go:generate protoc --proto_path=$GOPATH/src:. --go_out=plugins=grpc:. coordinator.proto
