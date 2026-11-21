// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitopsprexec

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

// gitLab implements provider with the GitLab REST API v4.
type gitLab struct {
	c       *apiClient
	project string
	log     *slog.Logger
}

func newGitLabAPI(client *http.Client, base string, timeout time.Duration, tok tokenSource, now func() time.Time) *apiClient {
	return &apiClient{
		client: client, base: base + "/api/v4", timeout: timeout, token: tok, now: now,
		authHeader: func(t string) (string, string) { return "PRIVATE-TOKEN", t },
		accept:     "application/json",
	}
}

func (g *gitLab) p(rest string) string {
	return "/projects/" + url.PathEscape(g.project) + rest
}

func (g *gitLab) refFor(iid string) string {
	return domain.ChangeRequestRefGitLab + g.project + "!" + iid
}

func (g *gitLab) headSHA(ctx context.Context, branch string) (string, bool, error) {
	var out struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	st, err := g.c.do(ctx, call{method: "GET", path: g.p("/repository/branches/" + url.PathEscape(branch)), out: &out, ok: []int{200, 404}})
	if err != nil || st == 404 {
		return "", false, err
	}
	return out.Commit.ID, true, nil
}

func (g *gitLab) readFile(ctx context.Context, ref, path string) ([]byte, bool, error) {
	var raw []byte
	st, err := g.c.do(ctx, call{method: "GET", path: g.p("/repository/files/" + url.PathEscape(path) + "/raw"), query: url.Values{"ref": {ref}},
		limit: maxFileBytes, out: &raw, ok: []int{200, 404}})
	if err != nil || st == 404 {
		return nil, false, err
	}
	return raw, true, nil
}

func (g *gitLab) commit(ctx context.Context, branch, base, createFrom, msg string, files map[string][]byte) (string, error) {
	_, exists, err := g.headSHA(ctx, branch)
	if err != nil {
		return "", err
	}
	type action struct {
		Action   string `json:"action"`
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	var actions []action
	for _, p := range sortedKeys(files) {
		actions = append(actions, action{Action: "update", FilePath: p, Content: string(files[p])})
	}
	in := map[string]any{"branch": branch, "commit_message": msg, "actions": actions}
	if !exists {
		in["start_branch"] = createFrom
	}
	var out struct {
		ID string `json:"id"`
	}
	_, err = g.c.do(ctx, call{method: "POST", path: g.p("/repository/commits"), in: in, out: &out, ok: []int{201}})
	return out.ID, err
}

type glMR struct {
	IID            int    `json:"iid"`
	State          string `json:"state"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	WebURL         string `json:"web_url"`
	SourceBranch   string `json:"source_branch"`
}

func (g *gitLab) remote(m glMR) *remoteRequest {
	n := strconv.Itoa(m.IID)
	r := &remoteRequest{ID: n, Ref: g.refFor(n), URL: m.WebURL, Head: m.SourceBranch, State: domain.ChangeRequestOpen}
	switch m.State {
	case "merged":
		r.State, r.MergeCommit = domain.ChangeRequestMerged, m.MergeCommitSHA
	case "closed":
		r.State = domain.ChangeRequestClosed
	}
	return r
}

func (g *gitLab) findRequest(ctx context.Context, head string) (*remoteRequest, error) {
	var list []glMR
	q := url.Values{"source_branch": {head}, "state": {"all"}, "order_by": {"created_at"}, "sort": {"desc"}, "per_page": {"1"}}
	if _, err := g.c.do(ctx, call{method: "GET", path: g.p("/merge_requests"), query: q, out: &list, ok: []int{200}}); err != nil || len(list) == 0 {
		return nil, err
	}
	return g.remote(list[0]), nil
}

func (g *gitLab) openRequest(ctx context.Context, head, base, title, body string, labels, reviewers []string) (*remoteRequest, error) {
	in := map[string]any{"source_branch": head, "target_branch": base, "title": title, "description": body}
	if len(labels) > 0 {
		in["labels"] = strings.Join(labels, ",")
	}
	var ids []int
	for _, name := range reviewers {
		var users []struct {
			ID int `json:"id"`
		}
		if _, err := g.c.do(ctx, call{method: "GET", path: "/users", query: url.Values{"username": {name}}, out: &users, ok: []int{200}}); err != nil {
			g.log.Warn("looking up a reviewer failed", "error", err)
		} else if len(users) > 0 {
			ids = append(ids, users[0].ID)
		}
	}
	if len(ids) > 0 {
		sort.Ints(ids)
		in["reviewer_ids"] = ids
	}
	var m glMR
	if _, err := g.c.do(ctx, call{method: "POST", path: g.p("/merge_requests"), in: in, out: &m, ok: []int{201}}); err != nil {
		return nil, err
	}
	return g.remote(m), nil
}

func (g *gitLab) getRequest(ctx context.Context, id string) (*remoteRequest, error) {
	var m glMR
	if _, err := g.c.do(ctx, call{method: "GET", path: g.p("/merge_requests/" + url.PathEscape(id)), out: &m, ok: []int{200}}); err != nil {
		return nil, err
	}
	return g.remote(m), nil
}

func (g *gitLab) closeRequest(ctx context.Context, id, comment string) error {
	base := g.p("/merge_requests/" + url.PathEscape(id))
	if _, err := g.c.do(ctx, call{method: "POST", path: base + "/notes", in: map[string]any{"body": comment}, ok: []int{201}}); err != nil {
		return err
	}
	_, err := g.c.do(ctx, call{method: "PUT", path: base, in: map[string]any{"state_event": "close"}, ok: []int{200}})
	return err
}

func (g *gitLab) deleteBranch(ctx context.Context, branch string) error {
	_, err := g.c.do(ctx, call{method: "DELETE", path: g.p("/repository/branches/" + url.PathEscape(branch)), ok: []int{204, 404}})
	return err
}
