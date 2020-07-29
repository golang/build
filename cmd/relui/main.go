// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// relui is a web interface for managing the release process of Go.
package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/golang/protobuf/proto"
	reluipb "golang.org/x/build/cmd/relui/protos"
)

func main() {
	s := &server{store: &memoryStore{}, configs: loadWorkflowConfig("./workflows")}
	http.Handle("/workflows/create", http.HandlerFunc(s.createWorkflowHandler))
	http.Handle("/workflows/new", http.HandlerFunc(s.newWorkflowHandler))
	http.Handle("/", fileServerHandler(relativeFile("./static"), http.HandlerFunc(s.homeHandler)))
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Listening on :" + port)
	log.Fatal(http.ListenAndServe(":"+port, http.DefaultServeMux))
}

// loadWorkflowConfig loads Workflow configuration files from dir. It expects all files to be in textproto format.
func loadWorkflowConfig(dir string) []*reluipb.Workflow {
	fs, err := filepath.Glob(filepath.Join(relativeFile(dir), "*.textpb"))
	if err != nil {
		log.Fatalf("Error perusing %q for configuration", filepath.Join(dir, "*.textpb"))
	}
	if len(fs) == 0 {
		log.Println("No workflow configuration found.")
	}
	var ws []*reluipb.Workflow
	for _, f := range fs {
		b, err := ioutil.ReadFile(f)
		if err != nil {
			log.Printf("ioutil.ReadFile(%q) = _, %v, wanted no error", f, err)
		}
		w := new(reluipb.Workflow)
		if err = proto.UnmarshalText(string(b), w); err != nil {
			log.Printf("Error unmarshalling Workflow from %q: %v", f, err)
			continue
		}
		ws = append(ws, w)
	}
	return ws
}
