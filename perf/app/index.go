// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package app

import (
	_ "embed"
	"log"
	"net/http"

	"github.com/google/safehtml/template"
	"golang.org/x/build/perfdata"
)

// index redirects / to /search.
func (a *App) index(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	t, err := template.New("index.html").ParseFS(tmplFS, "template/index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var uploads []perfdata.UploadInfo
	ul := a.StorageClient.ListUploads(ctx, "", []string{"by", "upload-time"}, 16)
	defer ul.Close()
	for ul.Next() {
		uploads = append(uploads, ul.Info())
	}
	if err := ul.Err(); err != nil {
		log.Printf("failed to fetch recent uploads: %v", err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, struct{ RecentUploads []perfdata.UploadInfo }{uploads}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
}
