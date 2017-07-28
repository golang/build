// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"sort"
	"strings"

	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/maintnerd/apipb"
)

// apiService implements apipb.MaintnerServiceServer using the Corpus c.
type apiService struct {
	c *maintner.Corpus
	// There really shouldn't be any more fields here.
	// All state should be in c.
	// A bool like "in staging" should just be a global flag.
}

func (s apiService) HasAncestor(ctx context.Context, req *apipb.HasAncestorRequest) (*apipb.HasAncestorResponse, error) {
	if len(req.Commit) != 40 {
		return nil, errors.New("invalid Commit")
	}
	if len(req.Ancestor) != 40 {
		return nil, errors.New("invalid Ancestor")
	}
	s.c.RLock()
	defer s.c.RUnlock()

	commit := s.c.GitCommit(req.Commit)
	res := new(apipb.HasAncestorResponse)
	if commit == nil {
		// TODO: wait for it? kick off a fetch of it and then answer?
		// optional?
		res.UnknownCommit = true
		return res, nil
	}
	if a := s.c.GitCommit(req.Ancestor); a != nil {
		res.HasAncestor = commit.HasAncestor(a)
	}
	return res, nil
}

func isStagingCommit(cl *maintner.GerritCL) bool {
	return cl.Commit != nil &&
		strings.Contains(cl.Commit.Msg, "DO NOT SUBMIT") &&
		strings.Contains(cl.Commit.Msg, "STAGING")
}

func tryBotStatus(cl *maintner.GerritCL, forStaging bool) (try, done bool) {
	if cl.Commit == nil {
		return // shouldn't happen
	}
	if forStaging != isStagingCommit(cl) {
		return
	}
	for _, msg := range cl.Messages {
		if msg.Version != cl.Version {
			continue
		}
		firstLine := msg.Message
		if nl := strings.IndexByte(firstLine, '\n'); nl != -1 {
			firstLine = firstLine[:nl]
		}
		if !strings.Contains(firstLine, "TryBot") {
			continue
		}
		if strings.Contains(firstLine, "Run-TryBot+1") {
			try = true
		}
		if strings.Contains(firstLine, "-Run-TryBot") {
			try = false
		}
		if strings.Contains(firstLine, "TryBot-Result") {
			done = true
		}
	}
	return
}

func tryWorkItem(cl *maintner.GerritCL) *apipb.GerritTryWorkItem {
	return &apipb.GerritTryWorkItem{
		Project:  cl.Project.Project(),
		Branch:   strings.TrimPrefix(cl.Branch(), "refs/heads/"),
		ChangeId: cl.ChangeID(),
		Commit:   cl.Commit.Hash.String(),
	}
}

func (s apiService) GetRef(ctx context.Context, req *apipb.GetRefRequest) (*apipb.GetRefResponse, error) {
	s.c.RLock()
	defer s.c.RUnlock()
	gp := s.c.Gerrit().Project(req.GerritServer, req.GerritProject)
	if gp == nil {
		return nil, errors.New("unknown gerrit project")
	}
	res := new(apipb.GetRefResponse)
	hash := gp.Ref(req.Ref)
	if hash != "" {
		res.Value = hash.String()
	}
	return res, nil
}

func (s apiService) GoFindTryWork(ctx context.Context, req *apipb.GoFindTryWorkRequest) (*apipb.GoFindTryWorkResponse, error) {
	s.c.RLock()
	defer s.c.RUnlock()
	res := new(apipb.GoFindTryWorkResponse)

	goProj := s.c.Gerrit().Project("go.googlesource.com", "go")

	s.c.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		gp.ForeachOpenCL(func(cl *maintner.GerritCL) error {
			try, done := tryBotStatus(cl, req.ForStaging)
			if !try || done {
				return nil
			}
			work := tryWorkItem(cl)
			if work.Project != "go" {
				// Trybot on a subrepo.
				//
				// TODO: for Issue 17626, we need to append
				// master and the past two releases, but for
				// now we'll just do master.
				work.GoBranch = append(work.GoBranch, "master")
				work.GoCommit = append(work.GoCommit, goProj.Ref("refs/heads/master").String())
			}
			res.Waiting = append(res.Waiting, work)
			return nil
		})
		return nil
	})

	// Sort in some stable order.
	//
	// TODO: better would be sorting by time the trybot was
	// requested, or the time of the CL. But we don't return that
	// (yet?) because the coordinator has never needed it
	// historically. But if we do a proper scheduler (Issue
	// 19178), perhaps it would be good data to have in the
	// coordinator.
	sort.Slice(res.Waiting, func(i, j int) bool {
		return res.Waiting[i].Commit < res.Waiting[j].Commit
	})
	return res, nil
}
