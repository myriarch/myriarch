// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package gitopsprtest holds test doubles of the GitHub and GitLab APIs for
// the tests of the gitops-pr executor. A double keeps a small in-memory
// repository (branches, commits, requests), serves exactly the endpoints that
// the executor calls, checks the token and records every call.
package gitopsprtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Request is a pull or merge request of the double.
type Request struct {
	ID          int
	Head, Base  string
	Title, Body string
	State       string // open, merged or closed
	MergeCommit string
	Comments    []string
	Labels      []string
	Reviewers   []string
}

type store struct {
	srv *httptest.Server

	mu      sync.Mutex
	token   string
	extra   map[string]bool // further accepted tokens (installation tokens)
	refs    map[string]string
	commits map[string]string
	trees   map[string]map[string]string
	reqs    []*Request
	calls   []string
	limitN  int
	echo    bool
	seq     int
	// appTokens counts the installation tokens issued by the App login.
	appTokens int
	// appHandler serves the endpoints of the GitHub App login; it reports
	// whether it handled the request.
	appHandler func(w http.ResponseWriter, r *http.Request) bool
}

func newStore(t *testing.T, token string, h func(*store, http.ResponseWriter, *http.Request), auth func(*http.Request) string, limitStatus int) *store {
	s := &store{token: token, extra: map[string]bool{}, refs: map[string]string{}, commits: map[string]string{}, trees: map[string]map[string]string{}}
	s.refs["main"] = s.addCommit(map[string]string{})
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, r.Method+" "+r.URL.Path)
		if s.appHandler != nil && s.appHandler(w, r) {
			return
		}
		if s.limitN > 0 {
			s.limitN--
			w.Header().Set("Retry-After", "120")
			s.fail(w, r, limitStatus, "rate limited")
			return
		}
		if tok := auth(r); tok != s.token && !s.extra[tok] {
			s.fail(w, r, http.StatusUnauthorized, "bad credentials")
			return
		}
		h(s, w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// ------------------------------------------------------------- controls ----

// URL is the base URL of the double.
func (s *store) URL() string { return s.srv.URL }

// Host is host:port of the double.
func (s *store) Host() string { return strings.TrimPrefix(s.srv.URL, "http://") }

// Token is the token that the double accepts.
func (s *store) Token() string { return s.token }

// SetFile puts a file on a branch (created from an empty tree if missing).
func (s *store) SetFile(branch, path, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := s.filesOf(branch)
	files[path] = content
	s.refs[branch] = s.addCommit(files)
}

// File returns a file on a branch.
func (s *store) File(branch, path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.refs[branch]; !ok {
		return "", false
	}
	c, ok := s.filesOf(branch)[path]
	return c, ok
}

// Branches lists the branch names.
func (s *store) Branches() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for b := range s.refs {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// Requests returns copies of all requests.
func (s *store) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, 0, len(s.reqs))
	for _, r := range s.reqs {
		out = append(out, *r)
	}
	return out
}

// Merge merges a request: the base branch takes the head.
func (s *store) Merge(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.req(id)
	r.State = "merged"
	r.MergeCommit = s.refs[r.Head]
	s.refs[r.Base] = s.refs[r.Head]
}

// CloseWithoutMerge closes a request as an operator would.
func (s *store) CloseWithoutMerge(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.req(id).State = "closed"
}

// RateLimitNext makes the next n calls fail with a rate-limit answer.
func (s *store) RateLimitNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limitN = n
}

// EchoAuthInErrors makes error answers repeat the credential headers of the
// call, to test that the executor redacts them.
func (s *store) EchoAuthInErrors(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.echo = on
}

// Calls returns "METHOD path" of every call so far.
func (s *store) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// AllowToken makes the double accept one more token.
func (s *store) AllowToken(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extra[tok] = true
}

// SetAppHandler installs the handler of the App login endpoints.
func (s *store) SetAppHandler(h func(w http.ResponseWriter, r *http.Request, allow func(string)) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appHandler = func(w http.ResponseWriter, r *http.Request) bool {
		return h(w, r, func(tok string) { s.extra[tok] = true })
	}
}

// ------------------------------------------------------------- internals ----

func (s *store) req(id int) *Request {
	for _, r := range s.reqs {
		if r.ID == id {
			return r
		}
	}
	panic(fmt.Sprintf("gitopsprtest: no request %d", id))
}

func (s *store) filesOf(branch string) map[string]string {
	out := map[string]string{}
	if sha, ok := s.refs[branch]; ok {
		for k, v := range s.trees[s.commits[sha]] {
			out[k] = v
		}
	}
	return out
}

func (s *store) addTree(files map[string]string) string {
	s.seq++
	sha := fmt.Sprintf("%040x", s.seq)
	cp := map[string]string{}
	for k, v := range files {
		cp[k] = v
	}
	s.trees[sha] = cp
	return sha
}

func (s *store) addCommit(files map[string]string) string {
	tree := s.addTree(files)
	s.seq++
	sha := fmt.Sprintf("%040x", s.seq)
	s.commits[sha] = tree
	return sha
}

func (s *store) newRequest(head, base, title, body string) *Request {
	s.seq++
	r := &Request{ID: len(s.reqs) + 1, Head: head, Base: base, Title: title, Body: body, State: "open"}
	s.reqs = append(s.reqs, r)
	return r
}

// latestFor returns the newest request with the head branch.
func (s *store) latestFor(head string) *Request {
	for i := len(s.reqs) - 1; i >= 0; i-- {
		if s.reqs[i].Head == head {
			return s.reqs[i]
		}
	}
	return nil
}

func (s *store) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if s.echo {
		msg += " auth=" + r.Header.Get("Authorization") + r.Header.Get("PRIVATE-TOKEN")
	}
	writeJSON(w, status, map[string]string{"message": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func decode(r *http.Request, v any) { _ = json.NewDecoder(r.Body).Decode(v) }

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func unescape(s string) string {
	u, err := url.PathUnescape(s)
	if err != nil {
		return s
	}
	return u
}

// ---------------------------------------------------------------- GitHub ----

// GitHub is the double of the GitHub REST API.
type GitHub struct{ *store }

// NewGitHub starts the double. Repository and owner are not checked.
func NewGitHub(t *testing.T) *GitHub {
	t.Helper()
	g := &GitHub{}
	g.store = newStore(t, "ghp_test_token_0123456789", func(s *store, w http.ResponseWriter, r *http.Request) { g.handle(w, r) },
		func(r *http.Request) string { return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") }, http.StatusForbidden)
	return g
}

func (g *GitHub) pull(r *Request) map[string]any {
	m := map[string]any{"number": r.ID, "state": "open", "merged": false, "merged_at": nil, "merge_commit_sha": nil,
		"html_url": fmt.Sprintf("%s/example/deploy/pull/%d", g.srv.URL, r.ID), "head": map[string]string{"ref": r.Head}}
	switch r.State {
	case "closed":
		m["state"] = "closed"
	case "merged":
		m["state"], m["merged"], m["merged_at"], m["merge_commit_sha"] = "closed", true, "2026-01-01T00:00:00Z", r.MergeCommit
	}
	return m
}

func (g *GitHub) handle(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if rest, ok := strings.CutPrefix(p, "/repos/"); ok {
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) < 3 {
			g.fail(w, r, 404, "not found")
			return
		}
		p = "/" + parts[2]
	}
	m := r.Method
	switch {
	case m == "GET" && strings.HasPrefix(p, "/git/ref/heads/"):
		sha, ok := g.refs[strings.TrimPrefix(p, "/git/ref/heads/")]
		if !ok {
			g.fail(w, r, 404, "Not Found")
			return
		}
		writeJSON(w, 200, map[string]any{"object": map[string]string{"sha": sha}})
	case m == "GET" && strings.HasPrefix(p, "/git/commits/"):
		tree, ok := g.commits[strings.TrimPrefix(p, "/git/commits/")]
		if !ok {
			g.fail(w, r, 404, "Not Found")
			return
		}
		writeJSON(w, 200, map[string]any{"tree": map[string]string{"sha": tree}})
	case m == "POST" && p == "/git/trees":
		var in struct {
			BaseTree string `json:"base_tree"`
			Tree     []struct{ Path, Mode, Type, Content string }
		}
		decode(r, &in)
		files := map[string]string{}
		for k, v := range g.trees[in.BaseTree] {
			files[k] = v
		}
		for _, e := range in.Tree {
			files[e.Path] = e.Content
		}
		writeJSON(w, 201, map[string]string{"sha": g.addTree(files)})
	case m == "POST" && p == "/git/commits":
		var in struct {
			Tree    string   `json:"tree"`
			Parents []string `json:"parents"`
		}
		decode(r, &in)
		g.seq++
		sha := fmt.Sprintf("%040x", g.seq)
		g.commits[sha] = in.Tree
		writeJSON(w, 201, map[string]string{"sha": sha})
	case m == "POST" && p == "/git/refs":
		var in struct{ Ref, SHA string }
		decode(r, &in)
		b := strings.TrimPrefix(in.Ref, "refs/heads/")
		if _, ok := g.refs[b]; ok {
			g.fail(w, r, 422, "Reference already exists")
			return
		}
		g.refs[b] = in.SHA
		writeJSON(w, 201, map[string]string{"ref": in.Ref})
	case (m == "PATCH" || m == "DELETE") && strings.HasPrefix(p, "/git/refs/heads/"):
		b := strings.TrimPrefix(p, "/git/refs/heads/")
		if _, ok := g.refs[b]; !ok {
			g.fail(w, r, 422, "Reference does not exist")
			return
		}
		if m == "DELETE" {
			delete(g.refs, b)
			w.WriteHeader(204)
			return
		}
		var in struct{ SHA string }
		decode(r, &in)
		g.refs[b] = in.SHA
		writeJSON(w, 200, map[string]string{"ref": b})
	case m == "GET" && strings.HasPrefix(p, "/contents/"):
		b := r.URL.Query().Get("ref")
		c, ok := g.filesOf(b)[strings.TrimPrefix(p, "/contents/")]
		if _, has := g.refs[b]; !ok || !has {
			g.fail(w, r, 404, "Not Found")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.github.raw+json")
		_, _ = w.Write([]byte(c))
	case m == "GET" && p == "/pulls":
		_, head, _ := strings.Cut(r.URL.Query().Get("head"), ":")
		out := []map[string]any{}
		if rq := g.latestFor(head); rq != nil {
			out = append(out, g.pull(rq))
		}
		writeJSON(w, 200, out)
	case m == "POST" && p == "/pulls":
		var in struct{ Title, Head, Base, Body string }
		decode(r, &in)
		if _, ok := g.refs[in.Head]; !ok {
			g.fail(w, r, 422, "head branch missing")
			return
		}
		writeJSON(w, 201, g.pull(g.newRequest(in.Head, in.Base, in.Title, in.Body)))
	case strings.HasPrefix(p, "/pulls/") || strings.HasPrefix(p, "/issues/"):
		parts := strings.Split(strings.Trim(p, "/"), "/")
		rq := g.find(atoi(parts[1]))
		if rq == nil {
			g.fail(w, r, 404, "Not Found")
			return
		}
		switch {
		case len(parts) == 2 && m == "GET":
			writeJSON(w, 200, g.pull(rq))
		case len(parts) == 2 && m == "PATCH":
			rq.State = "closed"
			writeJSON(w, 200, g.pull(rq))
		case len(parts) == 3 && parts[2] == "comments" && m == "POST":
			var in struct{ Body string }
			decode(r, &in)
			rq.Comments = append(rq.Comments, in.Body)
			writeJSON(w, 201, map[string]int{"id": 1})
		case len(parts) == 3 && parts[2] == "labels" && m == "POST":
			var in struct{ Labels []string }
			decode(r, &in)
			rq.Labels = append(rq.Labels, in.Labels...)
			writeJSON(w, 200, []string{})
		case len(parts) == 3 && parts[2] == "requested_reviewers" && m == "POST":
			var in struct{ Reviewers []string }
			decode(r, &in)
			rq.Reviewers = append(rq.Reviewers, in.Reviewers...)
			writeJSON(w, 201, map[string]int{"number": rq.ID})
		default:
			g.fail(w, r, 404, "Not Found")
		}
	default:
		g.fail(w, r, 404, "Not Found")
	}
}

func (s *store) find(id int) *Request {
	for _, r := range s.reqs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// ---------------------------------------------------------------- GitLab ----

// GitLab is the double of the GitLab REST API v4.
type GitLab struct{ *store }

// NewGitLab starts the double.
func NewGitLab(t *testing.T) *GitLab {
	t.Helper()
	g := &GitLab{}
	g.store = newStore(t, "glpat-test-token-0123456789", func(s *store, w http.ResponseWriter, r *http.Request) { g.handle(w, r) },
		func(r *http.Request) string { return r.Header.Get("PRIVATE-TOKEN") }, http.StatusTooManyRequests)
	return g
}

func (g *GitLab) mr(r *Request) map[string]any {
	m := map[string]any{"iid": r.ID, "state": "opened", "merge_commit_sha": nil,
		"web_url": fmt.Sprintf("%s/example/deploy/-/merge_requests/%d", g.srv.URL, r.ID), "source_branch": r.Head}
	switch r.State {
	case "closed":
		m["state"] = "closed"
	case "merged":
		m["state"], m["merge_commit_sha"] = "merged", r.MergeCommit
	}
	return m
}

func (g *GitLab) handle(w http.ResponseWriter, r *http.Request) {
	p := r.URL.EscapedPath()
	m := r.Method
	if p == "/api/v4/users" && m == "GET" {
		writeJSON(w, 200, []map[string]int{{"id": 7}})
		return
	}
	rest, ok := strings.CutPrefix(p, "/api/v4/projects/")
	if !ok {
		g.fail(w, r, 404, "404 Not Found")
		return
	}
	_, rest, _ = strings.Cut(rest, "/")
	rest = "/" + rest
	switch {
	case strings.HasPrefix(rest, "/repository/branches/"):
		b := unescape(strings.TrimPrefix(rest, "/repository/branches/"))
		sha, ok := g.refs[b]
		if !ok {
			g.fail(w, r, 404, "404 Branch Not Found")
			return
		}
		if m == "DELETE" {
			delete(g.refs, b)
			w.WriteHeader(204)
			return
		}
		writeJSON(w, 200, map[string]any{"commit": map[string]string{"id": sha}})
	case m == "GET" && strings.HasPrefix(rest, "/repository/files/"):
		name := unescape(strings.TrimSuffix(strings.TrimPrefix(rest, "/repository/files/"), "/raw"))
		b := r.URL.Query().Get("ref")
		c, ok := g.filesOf(b)[name]
		if _, has := g.refs[b]; !ok || !has {
			g.fail(w, r, 404, "404 File Not Found")
			return
		}
		_, _ = w.Write([]byte(c))
	case m == "POST" && rest == "/repository/commits":
		var in struct {
			Branch        string `json:"branch"`
			StartBranch   string `json:"start_branch"`
			CommitMessage string `json:"commit_message"`
			Actions       []struct {
				Action   string `json:"action"`
				FilePath string `json:"file_path"`
				Content  string `json:"content"`
			} `json:"actions"`
		}
		decode(r, &in)
		if _, exists := g.refs[in.Branch]; exists == (in.StartBranch != "") {
			g.fail(w, r, 400, "branch exists or start_branch missing")
			return
		}
		src := in.Branch
		if in.StartBranch != "" {
			src = in.StartBranch
		}
		files := g.filesOf(src)
		for _, a := range in.Actions {
			if _, has := files[a.FilePath]; !has {
				g.fail(w, r, 400, "file does not exist")
				return
			}
			files[a.FilePath] = a.Content
		}
		sha := g.addCommit(files)
		g.refs[in.Branch] = sha
		writeJSON(w, 201, map[string]string{"id": sha})
	case m == "GET" && rest == "/merge_requests":
		out := []map[string]any{}
		if rq := g.latestFor(r.URL.Query().Get("source_branch")); rq != nil {
			out = append(out, g.mr(rq))
		}
		writeJSON(w, 200, out)
	case m == "POST" && rest == "/merge_requests":
		var in struct {
			Source string `json:"source_branch"`
			Target string `json:"target_branch"`
			Title  string `json:"title"`
			Desc   string `json:"description"`
			Labels string `json:"labels"`
			Revs   []int  `json:"reviewer_ids"`
		}
		decode(r, &in)
		if _, ok := g.refs[in.Source]; !ok {
			g.fail(w, r, 400, "source branch missing")
			return
		}
		rq := g.newRequest(in.Source, in.Target, in.Title, in.Desc)
		if in.Labels != "" {
			rq.Labels = strings.Split(in.Labels, ",")
		}
		for _, id := range in.Revs {
			rq.Reviewers = append(rq.Reviewers, strconv.Itoa(id))
		}
		writeJSON(w, 201, g.mr(rq))
	case strings.HasPrefix(rest, "/merge_requests/"):
		parts := strings.Split(strings.TrimPrefix(rest, "/merge_requests/"), "/")
		rq := g.find(atoi(parts[0]))
		if rq == nil {
			g.fail(w, r, 404, "404 Not Found")
			return
		}
		switch {
		case len(parts) == 1 && m == "GET":
			writeJSON(w, 200, g.mr(rq))
		case len(parts) == 1 && m == "PUT":
			rq.State = "closed"
			writeJSON(w, 200, g.mr(rq))
		case len(parts) == 2 && parts[1] == "notes" && m == "POST":
			var in struct{ Body string }
			decode(r, &in)
			rq.Comments = append(rq.Comments, in.Body)
			writeJSON(w, 201, map[string]int{"id": 1})
		default:
			g.fail(w, r, 404, "404 Not Found")
		}
	default:
		g.fail(w, r, 404, "404 Not Found")
	}
}
