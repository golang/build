// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	s := &server{store: &memoryStore{}}
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
