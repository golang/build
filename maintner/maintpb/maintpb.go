// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintpb

// See https://github.com/golang/protobuf#installation and
// then run "go generate" in this directory to update.

//go:generate protoc --go_out=plugins=grpc:. maintner.proto
