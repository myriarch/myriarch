// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors shared by all packages. Wrap them with fmt.Errorf("...: %w", err)
// to add context; the HTTP layer (internal/api) maps them to status codes:
//
//	ErrNotFound        -> 404 not_found
//	ErrConflict        -> 409 conflict
//	ErrValidation      -> 422 validation_failed (use *ValidationError for details)
//	ErrInvalidArgument -> 400 bad_request
//	ErrUnauthorized    -> 401 unauthorized
//	ErrForbidden       -> 403 forbidden
//	ErrPrecondition    -> 412 precondition_failed (e.g. invalid state transition)
//	ErrTooManyRequests -> 429 too_many_requests
//	ErrUnsupportedMediaType -> 415 bad_request
//	ErrUnavailable     -> 503 unavailable
//	ErrNotImplemented  -> 501 not_implemented
var (
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
	ErrValidation      = errors.New("validation failed")
	ErrInvalidArgument = errors.New("invalid argument")
	ErrUnauthorized    = errors.New("unauthorized")
	ErrForbidden       = errors.New("forbidden")
	ErrPrecondition    = errors.New("precondition failed")
	ErrUnavailable     = errors.New("unavailable")
	// ErrTooManyRequests marks rate limited requests (429).
	ErrTooManyRequests = errors.New("too many requests")
	// ErrUnsupportedMediaType marks request bodies with a wrong Content-Type (415).
	ErrUnsupportedMediaType = errors.New("unsupported media type")
	// ErrNotImplemented is returned by scaffold stubs. Every stub in the
	// repository returns (or panics with) an error wrapping this sentinel so
	// that missing implementations are easy to find: grep -rn ErrNotImplemented.
	ErrNotImplemented = errors.New("not implemented")
)

// ClientError is an error whose message is safe to show to API clients: it names the kind of failure ("not
// found", "already exists") but no operation, constraint or driver text. The detail for the logs is in Internal,
// the sentinel in Kind (errors.Is) and the original error in Cause.
type ClientError struct {
	Msg      string
	Kind     error
	Cause    error
	Internal string
}

func (e *ClientError) Error() string { return e.Msg }

// Unwrap lets errors.Is find the sentinel and the original error.
func (e *ClientError) Unwrap() []error {
	out := make([]error, 0, 2)
	if e.Kind != nil {
		out = append(out, e.Kind)
	}
	if e.Cause != nil {
		out = append(out, e.Cause)
	}
	return out
}

// RateLimitError is a rate limited request: it matches ErrTooManyRequests and
// carries the time after which the client may retry (header Retry-After).
type RateLimitError struct {
	Msg   string
	After time.Duration
}

func (e *RateLimitError) Error() string { return ErrTooManyRequests.Error() + ": " + e.Msg }

// Is makes errors.Is(err, ErrTooManyRequests) true.
func (e *RateLimitError) Is(target error) bool { return target == ErrTooManyRequests }

// NotImplemented returns an error wrapping ErrNotImplemented naming the
// function that still needs an implementation, e.g.
// NotImplemented("opamp.Server.Start").
func NotImplemented(what string) error {
	return fmt.Errorf("%s: %w", what, ErrNotImplemented)
}

// Issue is a single validation finding. Used by GitOps validation, blueprint
// rendering, config validation and request validation.
type Issue struct {
	// Severity is "error" or "warning". Only errors make an object invalid.
	Severity IssueSeverity `json:"severity" yaml:"severity"`
	// Code is a stable machine readable identifier, e.g. "unknown_component",
	// "missing_parameter", "selector_invalid", "yaml_syntax".
	Code string `json:"code" yaml:"code"`
	// Message is a human readable English message.
	Message string `json:"message" yaml:"message"`
	// File is the repository relative path (GitOps) if known.
	File string `json:"file,omitempty" yaml:"file,omitempty"`
	// Line is the 1-based line in File if known (0 = unknown).
	Line int `json:"line,omitempty" yaml:"line,omitempty"`
	// Path is a confmap-style path ("service::pipelines::logs") or a field
	// path ("spec.selector") the issue refers to.
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
	// Object is "<Kind>/<name>" of the GitOps object if applicable.
	Object string `json:"object,omitempty" yaml:"object,omitempty"`
}

// IssueSeverity is the severity of an Issue.
type IssueSeverity string

const (
	SeverityError   IssueSeverity = "error"
	SeverityWarning IssueSeverity = "warning"
)

// Issues is a list of validation findings.
type Issues []Issue

// HasErrors reports whether at least one issue has SeverityError.
func (is Issues) HasErrors() bool {
	for _, i := range is {
		if i.Severity == SeverityError {
			return true
		}
	}
	return false
}

// ValidationError carries validation issues. errors.Is(err, ErrValidation)
// is true for *ValidationError.
type ValidationError struct {
	Issues Issues
}

// Error implements error.
func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return ErrValidation.Error()
	}
	msgs := make([]string, 0, len(e.Issues))
	for _, i := range e.Issues {
		if i.Severity == SeverityError {
			msgs = append(msgs, i.Message)
		}
	}
	return fmt.Sprintf("%s: %s", ErrValidation.Error(), strings.Join(msgs, "; "))
}

// Unwrap makes errors.Is(err, ErrValidation) work.
func (e *ValidationError) Unwrap() error { return ErrValidation }

// NewValidationError builds a *ValidationError from issues.
func NewValidationError(issues ...Issue) *ValidationError {
	return &ValidationError{Issues: issues}
}

// SettingError builds a *ValidationError with one issue for a configuration
// setting ("gitops.auth.tokenFile"): the message names the setting.
func SettingError(setting, msg string) *ValidationError {
	return NewValidationError(Issue{Severity: SeverityError, Code: "invalid_config", Path: setting, Message: setting + ": " + msg})
}
