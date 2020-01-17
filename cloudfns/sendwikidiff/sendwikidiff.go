// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package sendwikidiff implements a Google Cloud background function that
// reacts to a pubsub message containing a GitHub webhook change payload.
// It assumes the payload is in reaction to a change to the Go wiki, then
// sends the full diff to the golang-wikichanges mailing list.
package sendwikidiff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"os/exec"
	"path"
	"sync"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
)

const repoURL = "https://github.com/golang/go.wiki.git"

var sendgridAPIKey = os.Getenv("SENDGRID_API_KEY")

type pubsubMessage struct {
	Data []byte `json:"data"`
}

func HandleWikiChangePubSub(ctx context.Context, m pubsubMessage) error {
	if sendgridAPIKey == "" {
		return fmt.Errorf("Environment variable SENDGRID_API_KEY is empty")
	}

	var payload struct {
		Pages []struct {
			PageName string `json:"page_name"`
			SHA      string `json:"sha"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(m.Data, &payload); err != nil {
		fmt.Fprintf(os.Stderr, "Unable to decode payload: %v", err)
		return err
	}

	repo := newGitRepo(repoURL, tempRepoDir(repoURL))
	if err := repo.update(); err != nil {
		fmt.Fprintf(os.Stderr, "Unable to update repo: %v", err)
		return err
	}
	for _, page := range payload.Pages {
		out, err := repo.cmdShow(page.SHA).Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not show SHA %q: %v", page.SHA, err)
			return err
		}
		if err := sendEmail(page.PageName, string(out)); err != nil {
			fmt.Fprintf(os.Stderr, "Could not send email: %v", err)
			return err
		}
	}
	return nil
}

func tempRepoDir(repoURL string) string {
	return path.Join(os.TempDir(), url.PathEscape(repoURL))
}

var htmlTmpl = template.Must(template.New("email").Parse(`<p><a href="{{.PageURL}}">View page</a></p>
<pre style="font-family: monospace,monospace; white-space: pre-wrap;">{{.Diff}}</pre>
`))

func emailBody(page, diff string) (string, error) {
	var buf bytes.Buffer
	if err := htmlTmpl.Execute(&buf, struct {
		PageURL, Diff string
	}{
		Diff:    diff,
		PageURL: fmt.Sprintf("https://golang.org/wiki/%s", page),
	}); err != nil {
		return "", fmt.Errorf("template.Execute: %v", err)
	}
	return buf.String(), nil
}

func sendEmailSendGrid(page, diff string) error {
	from := mail.NewEmail("WikiDiffBot", "nobody@golang.org")
	subject := fmt.Sprintf("golang.org/wiki/%s was updated", page)
	to := mail.NewEmail("", "golang-wikichanges@googlegroups.com")

	body, err := emailBody(page, diff)
	if err != nil {
		return fmt.Errorf("emailBody: %v", err)
	}
	message := mail.NewSingleEmail(from, subject, to, diff, body)
	client := sendgrid.NewSendClient(sendgridAPIKey)
	_, err = client.Send(message)
	return err
}

// sendEmail sends an email that the golang.org/wiki/$page was updated
// with the provided diff.
// Var for testing.
var sendEmail func(page, diff string) error = sendEmailSendGrid

type gitRepo struct {
	sync.RWMutex

	repo string // remote address of repo
	dir  string // location of the repo
}

func newGitRepo(repo, dir string) *gitRepo {
	return &gitRepo{
		repo: repo,
		dir:  dir,
	}
}

func (r *gitRepo) clone() error {
	r.Lock()
	defer r.Unlock()
	cmd := exec.Command("git", "clone", r.repo, r.dir)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func (r *gitRepo) pull() error {
	r.Lock()
	defer r.Unlock()
	cmd := exec.Command("git", "pull")
	cmd.Dir = r.dir
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func (r *gitRepo) update() error {
	r.RLock()
	_, err := os.Stat(r.dir)
	r.RUnlock()
	if os.IsNotExist(err) {
		if err := r.clone(); err != nil {
			return fmt.Errorf("could not clone %q into %q: %v", r.repo, r.dir, err)
		}
		return nil
	}

	if err := r.pull(); err != nil {
		return fmt.Errorf("could not pull %q: %v", r.repo, err)
	}
	return nil
}

func (r *gitRepo) cmdShow(ref string) *exec.Cmd {
	r.RLock()
	defer r.RUnlock()
	cmd := exec.Command("git", "show", ref)
	cmd.Dir = r.dir
	return cmd
}
