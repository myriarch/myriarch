// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

// Problem is the error response body (RFC 9457, media type
// application/problem+json). Schema "Problem" in api/openapi.yaml.
type Problem struct {
	// Type is a URI reference. The server always sends "about:blank";
	// clients identify the problem by Code.
	Type string `json:"type"`
	// Title is a short English summary of the problem class.
	Title string `json:"title"`
	// Status is the HTTP status code.
	Status int `json:"status"`
	// Detail is an English, human readable explanation.
	Detail string `json:"detail,omitempty"`
	// Instance is the request path.
	Instance string `json:"instance,omitempty"`
	// Code is the stable machine readable error code (see ErrorCode*).
	Code string `json:"code"`
	// RequestID correlates with server logs (header X-Request-Id).
	RequestID string `json:"requestId,omitempty"`
	// Issues carries validation details for code "validation_failed".
	Issues domain.Issues `json:"issues,omitempty"`
}

// Stable error codes (Problem.code). The UI maps them to localized texts.
const (
	ErrorCodeBadRequest         = "bad_request"
	ErrorCodeUnauthorized       = "unauthorized"
	ErrorCodeForbidden          = "forbidden"
	ErrorCodeNotFound           = "not_found"
	ErrorCodeConflict           = "conflict"
	ErrorCodePreconditionFailed = "precondition_failed"
	ErrorCodeValidationFailed   = "validation_failed"
	ErrorCodeTooManyRequests    = "too_many_requests"
	ErrorCodeInternal           = "internal"
	ErrorCodeNotImplemented     = "not_implemented"
	ErrorCodeUnavailable        = "unavailable"
	ErrorCodeCSRF               = "csrf_failed"
)

// ProblemContentType is the media type of error responses.
const ProblemContentType = "application/problem+json"

// RequestIDHeader carries the request id (set by the middleware).
const RequestIDHeader = "X-Request-Id"

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// WriteProblem writes a Problem response.
func WriteProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	if r != nil {
		if p.Instance == "" {
			p.Instance = r.URL.Path
		}
		if p.RequestID == "" {
			p.RequestID = w.Header().Get(RequestIDHeader)
		}
	}
	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(p.Status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(p)
}

// WriteError maps err to a Problem using the domain sentinel errors:
//
//	*domain.ValidationError / ErrValidation -> 422 validation_failed (with issues)
//	ErrInvalidArgument -> 400, ErrUnauthorized -> 401, ErrForbidden -> 403,
//	ErrNotFound -> 404, ErrConflict -> 409, ErrPrecondition -> 412,
//	ErrNotImplemented -> 501, ErrUnavailable -> 503, everything else -> 500.
//
// For 500 responses the detail is generic and the real error is logged with
// slog.Default() including the request id.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	p := Problem{Detail: err.Error()}
	var ce *domain.ClientError
	if errors.As(err, &ce) {
		// the client gets the safe message; operation, constraint and driver text go to the log
		lvl := slog.LevelInfo
		if errors.Is(err, domain.ErrNotFound) {
			lvl = slog.LevelDebug
		}
		ctx := context.Background()
		if r != nil {
			ctx = r.Context()
		}
		slog.Default().Log(ctx, lvl, "request failed", "error", ce.Internal, "request_id", w.Header().Get(RequestIDHeader), "path", pathOf(r))
	}
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		p.Status, p.Code, p.Issues = http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ve.Issues
	case errors.Is(err, domain.ErrValidation):
		p.Status, p.Code = http.StatusUnprocessableEntity, ErrorCodeValidationFailed
	case errors.Is(err, domain.ErrInvalidArgument):
		p.Status, p.Code = http.StatusBadRequest, ErrorCodeBadRequest
	case errors.Is(err, domain.ErrUnauthorized):
		p.Status, p.Code = http.StatusUnauthorized, ErrorCodeUnauthorized
	case errors.Is(err, domain.ErrForbidden):
		p.Status, p.Code = http.StatusForbidden, ErrorCodeForbidden
	case errors.Is(err, domain.ErrNotFound):
		p.Status, p.Code = http.StatusNotFound, ErrorCodeNotFound
	case errors.Is(err, domain.ErrConflict):
		p.Status, p.Code = http.StatusConflict, ErrorCodeConflict
	case errors.Is(err, domain.ErrPrecondition):
		p.Status, p.Code = http.StatusPreconditionFailed, ErrorCodePreconditionFailed
	case errors.Is(err, domain.ErrNotImplemented):
		p.Status, p.Code = http.StatusNotImplemented, ErrorCodeNotImplemented
	case errors.Is(err, domain.ErrTooManyRequests):
		p.Status, p.Code = http.StatusTooManyRequests, ErrorCodeTooManyRequests
		var rl *domain.RateLimitError
		if errors.As(err, &rl) && rl.After > 0 {
			secs := int((rl.After + time.Second - 1) / time.Second)
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
	case errors.Is(err, domain.ErrUnsupportedMediaType):
		p.Status, p.Code = http.StatusUnsupportedMediaType, ErrorCodeBadRequest
	case errors.Is(err, domain.ErrUnavailable):
		// The cause (database, filesystem, upstream) may contain internal
		// details: log it, answer with a fixed text.
		p.Status, p.Code = http.StatusServiceUnavailable, ErrorCodeUnavailable
		reqID := w.Header().Get(RequestIDHeader)
		slog.Default().Warn("service unavailable", "error", err, "request_id", reqID, "path", pathOf(r))
		p.Detail = "service temporarily unavailable (request id " + reqID + ")"
	default:
		p.Status, p.Code = http.StatusInternalServerError, ErrorCodeInternal
		reqID := w.Header().Get(RequestIDHeader)
		slog.Default().Error("internal error", "error", err, "request_id", reqID, "path", pathOf(r))
		p.Detail = "internal server error (see server logs, request id " + reqID + ")"
	}
	WriteProblem(w, r, p)
}

func pathOf(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.URL.Path
}

// requireJSON rejects requests that declare a non-JSON Content-Type. Browsers
// can send cross-site "simple" requests only with form or text types, so this
// closes login/mutation requests forged from other sites. A missing header is
// accepted (scripts, curl -d with an explicit empty type).
func requireJSON(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return nil
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || (mt != "application/json" && !strings.HasSuffix(mt, "+json")) {
		return fmt.Errorf("%w: Content-Type must be application/json", domain.ErrUnsupportedMediaType)
	}
	return nil
}

// DecodeJSON decodes the request body into v. Unknown fields are rejected.
// Errors wrap domain.ErrInvalidArgument (wrong Content-Type:
// domain.ErrUnsupportedMediaType). The body size limit is enforced by the
// middleware (Route.MaxBodyBytes).
func DecodeJSON(r *http.Request, v any) error {
	if err := requireJSON(r); err != nil {
		return err
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: request body is empty", domain.ErrInvalidArgument)
		}
		return fmt.Errorf("%w: invalid JSON body: %v", domain.ErrInvalidArgument, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: request body must contain a single JSON value", domain.ErrInvalidArgument)
	}
	return nil
}

// DecodeJSONOptional is DecodeJSON for optional request bodies: an empty
// body (Content-Length 0 or EOF) leaves v unchanged and returns nil.
func DecodeJSONOptional(r *http.Request, v any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	if err := requireJSON(r); err != nil {
		return err
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("%w: invalid JSON body: %v", domain.ErrInvalidArgument, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: request body must contain a single JSON value", domain.ErrInvalidArgument)
	}
	return nil
}

// Pagination limits.
const (
	DefaultLimit = 50
	MaxLimit     = 500
	// MaxOffset bounds deep paging (large offsets are expensive for the database).
	MaxOffset = 100000
)

// Pagination holds parsed limit/offset query parameters.
type Pagination struct {
	Limit  int
	Offset int
}

// ParsePagination reads "limit" (1..MaxLimit, default DefaultLimit) and
// "offset" (>= 0, default 0).
func ParsePagination(r *http.Request) (Pagination, error) {
	p := Pagination{Limit: DefaultLimit}
	q := r.URL.Query()
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > MaxLimit {
			return p, fmt.Errorf("%w: limit must be between 1 and %d", domain.ErrInvalidArgument, MaxLimit)
		}
		p.Limit = n
	}
	if s := q.Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > MaxOffset {
			return p, fmt.Errorf("%w: offset must be between 0 and %d", domain.ErrInvalidArgument, MaxOffset)
		}
		p.Offset = n
	}
	return p, nil
}

// ParseSort validates a "sort" query parameter against allowed field names
// (an optional leading "-" means descending). Returns def if empty.
func ParseSort(r *http.Request, allowed []string, def string) (string, error) {
	s := strings.TrimSpace(r.URL.Query().Get("sort"))
	if s == "" {
		return def, nil
	}
	field := strings.TrimPrefix(s, "-")
	for _, a := range allowed {
		if a == field {
			return s, nil
		}
	}
	return "", fmt.Errorf("%w: unsupported sort field %q (allowed: %s)", domain.ErrInvalidArgument, field, strings.Join(allowed, ", "))
}

// Page is the generic list envelope (schema "<X>Page" in api/openapi.yaml):
// {"items": [...], "total": 123, "limit": 50, "offset": 0}.
type Page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// NewPage builds a Page and guarantees a non-nil Items slice.
func NewPage[T any](items []T, total int, p Pagination) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, Total: total, Limit: p.Limit, Offset: p.Offset}
}

// List is the envelope for unpaginated lists: {"items": [...]}.
type List[T any] struct {
	Items []T `json:"items"`
}

// NewList builds a List and guarantees a non-nil Items slice.
func NewList[T any](items []T) List[T] {
	if items == nil {
		items = []T{}
	}
	return List[T]{Items: items}
}

// PathAgentID parses the "{id}" path value as agent id.
func PathAgentID(r *http.Request, name string) (domain.AgentID, error) {
	return domain.ParseAgentID(r.PathValue(name))
}
