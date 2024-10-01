// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file exists to create a dependency on the
// google.golang.org/grpc/cmd/protoc-gen-go-grpc module,
// so we can record the version of the gRPC code generator
// we're using in our go.mod.
//
// The only package in this module is a binary,
// so importing it is an error.
// Therefore, we depend on it in a file with "//go:build ignore".

//go:build ignore

package main

import _ "google.golang.org/grpc/cmd/protoc-gen-go-grpc"
