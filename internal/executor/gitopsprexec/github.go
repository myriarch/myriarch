// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitopsprexec

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

// gitHub implements provider with the GitHub REST API (v3).
type gitHub struct {
	c     *apiClient
	owner string
	repo  string
	log   *slog.Logger
}

func newGitHubAPI(client *http.Client, base string, timeout time.Duration, tok tokenSource, now func() time.Time) *apiClient {
	return &apiClient{
		client: client, base: base, timeout: timeout, token: tok, now: now,
		authHeader: func(t string) (string, string) { return "Authorization", "Bearer " + t },
		accept:     "application/vnd.github+json",
		extra:      map[string]string{"X-GitHub-Api-Version": "2022-11-28"},
	}
}

func newGitHub(c *apiClient, repository string, log *slog.Logger) *gitHub {
	owner, repo, _ := strings.Cut(repository, "/")
	return &gitHub{c: c, owner: owner, repo: repo, log: log}
}

func (g *gitHub) p(format string, a ...any) string {
	return "/repos/" + url.PathEscape(g.owner) + "/" + url.PathEscape(g.repo) + fmt.Sprintf(format, a...)
}

func (g *gitHub) refFor(n string) string {
	return domain.ChangeRequestRefGitHub + g.owner + "/" + g.repo + "#" + n
}

func (g *gitHub) headSHA(ctx context.Context, branch string) (string, bool, error) {
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	st, err := g.c.do(ctx, call{method: "GET", path: g.p("/git/ref/heads/%s", escapePath(branch)), out: &out, ok: []int{200, 404}})
	if err != nil || st == 404 {
		return "", false, err
	}
	return out.Object.SHA, true, nil
}

func (g *gitHub) readFile(ctx context.Context, ref, path string) ([]byte, bool, error) {
	var raw []byte
	st, err := g.c.do(ctx, call{method: "GET", path: g.p("/contents/%s", escapePath(path)), query: url.Values{"ref": {ref}},
		accept: "application/vnd.github.raw+json", limit: maxFileBytes, out: &raw, ok: []int{200, 404}})
	if err != nil || st == 404 {
		return nil, false, err
	}
	return raw, true, nil
}

func (g *gitHub) commit(ctx context.Context, branch, base, createFrom, msg string, files map[string][]byte) (string, error) {
	parent, exists, err := g.headSHA(ctx, branch)
	if err != nil {
		return "", err
	}
	if !exists {
		var ok bool
		if parent, ok, err = g.headSHA(ctx, createFrom); err != nil {
			return "", err
		} else if !ok {
			return "", fmt.Errorf("base branch %s does not exist", createFrom)
		}
	}
	var pc struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if _, err := g.c.do(ctx, call{method: "GET", path: g.p("/git/commits/%s", url.PathEscape(parent)), out: &pc, ok: []int{200}}); err != nil {
		return "", err
	}
	type entry struct {
		Path    string `json:"path"`
		Mode    string `json:"mode"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	var entries []entry
	for _, p := range sortedKeys(files) {
		entries = append(entries, entry{Path: p, Mode: "100644", Type: "blob", Content: string(files[p])})
	}
	var tree, cm struct {
		SHA string `json:"sha"`
	}
	if _, err := g.c.do(ctx, call{method: "POST", path: g.p("/git/trees"), in: map[string]any{"base_tree": pc.Tree.SHA, "tree": entries}, out: &tree, ok: []int{201}}); err != nil {
		return "", err
	}
	if _, err := g.c.do(ctx, call{method: "POST", path: g.p("/git/commits"), in: map[string]any{"message": msg, "tree": tree.SHA, "parents": []string{parent}}, out: &cm, ok: []int{201}}); err != nil {
		return "", err
	}
	if exists {
		_, err = g.c.do(ctx, call{method: "PATCH", path: g.p("/git/refs/heads/%s", escapePath(branch)), in: map[string]any{"sha": cm.SHA, "force": false}, ok: []int{200}})
	} else {
		_, err = g.c.do(ctx, call{method: "POST", path: g.p("/git/refs"), in: map[string]any{"ref": "refs/heads/" + branch, "sha": cm.SHA}, ok: []int{201}})
	}
	return cm.SHA, err
}

type ghPull struct {
	Number         int    `json:"number"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergedAt       string `json:"merged_at"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	HTMLURL        string `json:"html_url"`
	Head           struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

func (g *gitHub) remote(p ghPull) *remoteRequest {
	n := strconv.Itoa(p.Number)
	r := &remoteRequest{ID: n, Ref: g.refFor(n), URL: p.HTMLURL, Head: p.Head.Ref, State: domain.ChangeRequestOpen}
	switch {
	case p.Merged || p.MergedAt != "":
		r.State, r.MergeCommit = domain.ChangeRequestMerged, p.MergeCommitSHA
	case p.State == "closed":
		r.State = domain.ChangeRequestClosed
	}
	return r
}

func (g *gitHub) findRequest(ctx context.Context, head string) (*remoteRequest, error) {
	var list []ghPull
	q := url.Values{"head": {g.owner + ":" + head}, "state": {"all"}, "sort": {"created"}, "direction": {"desc"}, "per_page": {"1"}}
	if _, err := g.c.do(ctx, call{method: "GET", path: g.p("/pulls"), query: q, out: &list, ok: []int{200}}); err != nil || len(list) == 0 {
		return nil, err
	}
	return g.remote(list[0]), nil
}

func (g *gitHub) openRequest(ctx context.Context, head, base, title, body string, labels, reviewers []string) (*remoteRequest, error) {
	var p ghPull
	in := map[string]any{"title": title, "head": head, "base": base, "body": body}
	if _, err := g.c.do(ctx, call{method: "POST", path: g.p("/pulls"), in: in, out: &p, ok: []int{201}}); err != nil {
		return nil, err
	}
	r := g.remote(p)
	if len(labels) > 0 {
		if _, err := g.c.do(ctx, call{method: "POST", path: g.p("/issues/%s/labels", r.ID), in: map[string]any{"labels": labels}, ok: []int{200}}); err != nil {
			g.log.Warn("adding labels failed", "ref", r.Ref, "error", err)
		}
	}
	if len(reviewers) > 0 {
		if _, err := g.c.do(ctx, call{method: "POST", path: g.p("/pulls/%s/requested_reviewers", r.ID), in: map[string]any{"reviewers": reviewers}, ok: []int{201}}); err != nil {
			g.log.Warn("requesting reviewers failed", "ref", r.Ref, "error", err)
		}
	}
	return r, nil
}

func (g *gitHub) getRequest(ctx context.Context, id string) (*remoteRequest, error) {
	var p ghPull
	if _, err := g.c.do(ctx, call{method: "GET", path: g.p("/pulls/%s", url.PathEscape(id)), out: &p, ok: []int{200}}); err != nil {
		return nil, err
	}
	return g.remote(p), nil
}

func (g *gitHub) closeRequest(ctx context.Context, id, comment string) error {
	if _, err := g.c.do(ctx, call{method: "POST", path: g.p("/issues/%s/comments", url.PathEscape(id)), in: map[string]any{"body": comment}, ok: []int{201}}); err != nil {
		return err
	}
	_, err := g.c.do(ctx, call{method: "PATCH", path: g.p("/pulls/%s", url.PathEscape(id)), in: map[string]any{"state": "closed"}, ok: []int{200}})
	return err
}

func (g *gitHub) deleteBranch(ctx context.Context, branch string) error {
	_, err := g.c.do(ctx, call{method: "DELETE", path: g.p("/git/refs/heads/%s", escapePath(branch)), ok: []int{204, 404, 422}})
	return err
}
