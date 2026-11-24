// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitopsprexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseBytes = 64 << 10
	maxFileBytes     = 1 << 20
	maxErrorBody     = 200
)

// rateLimitError is returned when the API refuses a call because of a rate
// limit. wait is the time the API asks to wait (zero when it did not say).
type rateLimitError struct {
	wait time.Duration
	msg  string
}

func (e *rateLimitError) Error() string { return e.msg }

// tokenSource returns the credential for the next call. The value is never
// logged and replaced by [redacted] in error texts.
type tokenSource func(ctx context.Context) (string, error)

// apiClient makes the calls of one provider. It never follows redirects.
type apiClient struct {
	client     *http.Client
	base       string // API base without trailing slash
	timeout    time.Duration
	token      tokenSource
	authHeader func(token string) (name, value string)
	now        func() time.Time
	// accept is the Accept header of JSON calls.
	accept string
	extra  map[string]string
}

type call struct {
	method string
	path   string // escaped path below base
	query  url.Values
	in     any
	out    any
	accept string
	limit  int64
	ok     []int
}

// do sends the call and decodes a JSON answer into c.out (or, with a
// *[]byte, takes the raw body). It returns the status; a status outside c.ok
// is an error.
func (a *apiClient) do(ctx context.Context, c call) (int, error) {
	tok, err := a.token(ctx)
	if err != nil {
		return 0, err
	}
	u := a.base + c.path
	redacted := a.base + c.path
	if len(c.query) > 0 {
		u += "?" + c.query.Encode()
	}
	var body io.Reader
	if c.in != nil {
		b, err := json.Marshal(c.in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, c.method, u, body)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %v", c.method, redactURL(redacted), scrub(err.Error(), tok))
	}
	name, value := a.authHeader(tok)
	req.Header.Set(name, value)
	acc := a.accept
	if c.accept != "" {
		acc = c.accept
	}
	req.Header.Set("Accept", acc)
	for k, v := range a.extra {
		req.Header.Set(k, v)
	}
	if c.in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %s", c.method, redactURL(redacted), scrub(stripURLError(err), tok))
	}
	defer resp.Body.Close()
	limit := c.limit
	if limit == 0 {
		limit = maxResponseBytes
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("%s %s: reading the answer failed", c.method, redactURL(redacted))
	}
	if int64(len(raw)) > limit {
		return resp.StatusCode, fmt.Errorf("%s %s: the answer is larger than %d bytes", c.method, redactURL(redacted), limit)
	}
	for _, s := range c.ok {
		if s == resp.StatusCode {
			switch out := c.out.(type) {
			case nil:
			case *[]byte:
				*out = raw
			default:
				if len(raw) > 0 {
					if err := json.Unmarshal(raw, out); err != nil {
						return resp.StatusCode, fmt.Errorf("%s %s: the answer is not valid JSON", c.method, redactURL(redacted))
					}
				}
			}
			return resp.StatusCode, nil
		}
	}
	msg := fmt.Sprintf("%s %s: status %d: %s", c.method, redactURL(redacted), resp.StatusCode, excerpt(raw, tok))
	if wait, limited := rateLimited(resp, a.now()); limited {
		return resp.StatusCode, &rateLimitError{wait: wait, msg: msg}
	}
	return resp.StatusCode, fmt.Errorf("%s", msg)
}

func stripURLError(err error) string {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err.Error()
	}
	return err.Error()
}

// redactURL drops credentials and query from a URL for messages.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid>"
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// scrub replaces the token in s.
func scrub(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}

// excerpt returns at most maxErrorBody bytes of an answer with the token
// replaced.
func excerpt(b []byte, token string) string {
	s := strings.Join(strings.Fields(scrub(string(b), token)), " ")
	if len(s) > maxErrorBody {
		s = strings.ToValidUTF8(s[:maxErrorBody], "") + "..."
	}
	return s
}

// rateLimited tells whether the answer is a rate-limit refusal and how long
// the API asks to wait.
func rateLimited(resp *http.Response, now time.Time) (time.Duration, bool) {
	h := resp.Header
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusForbidden {
		return 0, false
	}
	var wait time.Duration
	limited := resp.StatusCode == http.StatusTooManyRequests
	if ra := h.Get("Retry-After"); ra != "" {
		limited = true
		if n, err := strconv.Atoi(ra); err == nil && n > 0 {
			wait = time.Duration(n) * time.Second
		}
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		limited = true
	}
	if wait == 0 {
		for _, k := range []string{"X-RateLimit-Reset", "RateLimit-Reset"} {
			if n, err := strconv.ParseInt(h.Get(k), 10, 64); err == nil && n > 0 {
				if d := time.Unix(n, 0).Sub(now); d > 0 {
					wait = d
					break
				}
			}
		}
	}
	return wait, limited
}

// escapePath escapes each segment of a repository path and keeps the slashes.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
