// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

func TestWriteErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("agent x: %w", domain.ErrNotFound), 404, ErrorCodeNotFound},
		{domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: "missing_parameter", Message: "m"}), 422, ErrorCodeValidationFailed},
		{fmt.Errorf("x: %w", domain.ErrInvalidArgument), 400, ErrorCodeBadRequest},
		{domain.ErrUnauthorized, 401, ErrorCodeUnauthorized},
		{domain.ErrForbidden, 403, ErrorCodeForbidden},
		{domain.ErrConflict, 409, ErrorCodeConflict},
		{domain.ErrPrecondition, 412, ErrorCodePreconditionFailed},
		{domain.NotImplemented("x"), 501, ErrorCodeNotImplemented},
		{domain.ErrUnavailable, 503, ErrorCodeUnavailable},
		{fmt.Errorf("boom"), 500, ErrorCodeInternal},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		WriteError(rec, httptest.NewRequest(http.MethodGet, "/api/v1/x", nil), c.err)
		if rec.Code != c.status {
			t.Errorf("%v: status %d want %d", c.err, rec.Code, c.status)
		}
		if ct := rec.Header().Get("Content-Type"); ct != ProblemContentType {
			t.Errorf("content type %q", ct)
		}
		var p Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != c.code || p.Status != c.status || p.Instance != "/api/v1/x" {
			t.Errorf("%v: problem %+v (%v)", c.err, p, err)
		}
		if c.code == ErrorCodeValidationFailed && len(p.Issues) != 1 {
			t.Errorf("validation issues missing")
		}
	}
}

func TestPaginationAndSort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x?limit=20&offset=40&sort=-hostname", nil)
	p, err := ParsePagination(r)
	if err != nil || p.Limit != 20 || p.Offset != 40 {
		t.Fatalf("%+v %v", p, err)
	}
	if s, err := ParseSort(r, []string{"hostname"}, "hostname"); err != nil || s != "-hostname" {
		t.Fatalf("%q %v", s, err)
	}
	if _, err := ParsePagination(httptest.NewRequest(http.MethodGet, "/x?limit=501", nil)); err == nil {
		t.Fatal("limit > 500 must fail")
	}
	page := NewPage[int](nil, 0, p)
	if page.Items == nil {
		t.Fatal("items must not be nil")
	}
}

// REST-4: the cause of unavailable errors stays in the log.
func TestUnavailableErrorsDoNotLeakTheCause(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set(RequestIDHeader, "req-123")
	cause := fmt.Errorf("%w: session lookup: dial tcp 10.1.2.3:5432: connect: connection refused (user myriarchd, db myriarchd)", domain.ErrUnavailable)
	WriteError(rec, httptest.NewRequest(http.MethodGet, "/api/v1/x", nil), cause)
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 503 || p.Code != ErrorCodeUnavailable || p.Detail != "service temporarily unavailable (request id req-123)" {
		t.Fatalf("%d %+v", rec.Code, p)
	}
	if strings.Contains(rec.Body.String(), "10.1.2.3") || strings.Contains(rec.Body.String(), "myriarchd") {
		t.Fatalf("internal detail leaked: %s", rec.Body.String())
	}
}

// AUTH-BRUTE-1: rate limited requests are 429 and carry Retry-After.
func TestRateLimitErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest(http.MethodPost, "/x", nil), &domain.RateLimitError{Msg: "slow down", After: 2500 * time.Millisecond})
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "3" {
		t.Fatalf("%d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	var p Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Code != ErrorCodeTooManyRequests {
		t.Fatalf("%+v", p)
	}
	rec = httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest(http.MethodPost, "/x", nil), domain.ErrTooManyRequests)
	if rec.Code != 429 {
		t.Fatalf("plain sentinel: %d", rec.Code)
	}
}

// AUTH-CSRF-1: JSON bodies must be declared as JSON.
func TestDecodeJSONRequiresJSONContentType(t *testing.T) {
	decode := func(ct, body string, optional bool) error {
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		var v map[string]any
		if optional {
			return DecodeJSONOptional(r, &v)
		}
		return DecodeJSON(r, &v)
	}
	for _, ok := range []string{"application/json", "application/json; charset=utf-8", "application/merge-patch+json", ""} {
		if err := decode(ok, `{"a":1}`, false); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"text/plain", "text/plain;charset=UTF-8", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/xml", "garbage;;"} {
		err := decode(bad, `{"a":1}`, false)
		if !errors.Is(err, domain.ErrUnsupportedMediaType) {
			t.Errorf("%q: %v", bad, err)
		}
		if err := decode(bad, `{"a":1}`, true); !errors.Is(err, domain.ErrUnsupportedMediaType) {
			t.Errorf("optional %q: %v", bad, err)
		}
	}
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest(http.MethodPost, "/x", nil), decode0())
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d", rec.Code)
	}
}

func decode0() error {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("a=b"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var v map[string]any
	return DecodeJSON(r, &v)
}

// REST-3: deep paging is bounded.
func TestOffsetIsBounded(t *testing.T) {
	for q, ok := range map[string]bool{"offset=0": true, "offset=100000": true, "offset=100001": false, "offset=-1": false, "offset=999999999999": false} {
		_, err := ParsePagination(httptest.NewRequest(http.MethodGet, "/x?"+q, nil))
		if (err == nil) != ok {
			t.Errorf("%s: %v", q, err)
		}
	}
}
