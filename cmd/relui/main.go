// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// relui is a web interface for managing the release process of Go.
package main

import (
	"context"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"cloud.google.com/go/datastore"
	"cloud.google.com/go/pubsub"
	"github.com/golang/protobuf/proto"
	"github.com/googleapis/google-cloud-go-testing/datastore/dsiface"
	reluipb "golang.org/x/build/cmd/relui/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	projectID = flag.String("project-id", os.Getenv("PUBSUB_PROJECT_ID"), "Pubsub project ID for communicating with workers. Uses PUBSUB_PROJECT_ID if unset.")
	topicID   = flag.String("topic-id", "relui-development", "Pubsub topic ID for communicating with workers.")
)

func main() {
	flag.Parse()
	ctx := context.Background()
	dsc, err := datastore.NewClient(ctx, *projectID)
	if err != nil {
		log.Fatalf("datastore.NewClient(_, %q) = _, %v, wanted no error", *projectID, err)
	}
	d := &dsStore{client: dsiface.AdaptClient(dsc)}
	s := &server{
		configs: loadWorkflowConfig("./workflows"),
		store:   d,
		topic:   getTopic(ctx),
	}
	http.Handle("/workflows/create", http.HandlerFunc(s.createWorkflowHandler))
	http.Handle("/workflows/new", http.HandlerFunc(s.newWorkflowHandler))
	http.Handle("/tasks/start", http.HandlerFunc(s.startTaskHandler))
	http.Handle("/", fileServerHandler(relativeFile("./static"), http.HandlerFunc(s.homeHandler)))
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Listening on :" + port)
	log.Fatal(http.ListenAndServe(":"+port, http.DefaultServeMux))
}

// getTopic creates and returns a pubsub topic from the project specified in projectId, which is to be used for
// communicating with relui workers.
//
// It is safe to call if a topic already exists. A reference to the topic will be returned.
func getTopic(ctx context.Context) *pubsub.Topic {
	client, err := pubsub.NewClient(ctx, *projectID)
	if err != nil {
		log.Fatalf("pubsub.NewClient(_, %q) = %v, wanted no error", *projectID, err)
	}
	_, err = client.CreateTopic(ctx, *topicID)
	if err != nil && status.Code(err) != codes.AlreadyExists {
		log.Fatalf("client.CreateTopic(_, %q) = %v, wanted no error", *topicID, err)
	}
	return client.Topic(*topicID)
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
