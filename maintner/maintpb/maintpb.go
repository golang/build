// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintpb

// Run "go generate" in this directory to update. You need to have:
// - a protoc binary
// - a protoc-gen-go binary
// - go get github.com/golang/protobuf
//
// See https://github.com/golang/protobuf#installation for more info.

//go:generate protoc --proto_path=$GOPATH/src:. --go_out=plugins=grpc:. maintner.proto
