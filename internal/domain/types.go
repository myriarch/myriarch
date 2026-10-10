// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AgentID is the OpAMP instance_uid of an agent (16 bytes). OpAMP requires 16
// bytes and recommends UUIDv7; the upstream supervisor generates UUIDv7 and
// persists it in <storage.directory>/persistent_state.yaml.
type AgentID = uuid.UUID

// ParseAgentID parses the canonical 36 character UUID string form.
func ParseAgentID(s string) (AgentID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return AgentID{}, fmt.Errorf("%w: invalid agent id %q", ErrInvalidArgument, s)
	}
	return id, nil
}

// AgentIDFromBytes converts an OpAMP instance_uid (must be exactly 16 bytes).
func AgentIDFromBytes(b []byte) (AgentID, error) {
	if len(b) != 16 {
		return AgentID{}, fmt.Errorf("%w: instance_uid must be 16 bytes, got %d", ErrInvalidArgument, len(b))
	}
	return uuid.FromBytes(b)
}

// Hash is a content hash (normally SHA-256, 32 bytes). JSON/YAML encoding is
// a lower-case hex string; the empty hash encodes as "".
type Hash []byte

// SHA256 returns the SHA-256 hash of data.
func SHA256(data []byte) Hash {
	s := sha256.Sum256(data)
	return Hash(s[:])
}

// String returns the lower-case hex form.
func (h Hash) String() string { return hex.EncodeToString(h) }

// Short returns the first 12 hex characters (for logs and UI).
func (h Hash) Short() string {
	s := h.String()
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// IsZero reports whether the hash is empty.
func (h Hash) IsZero() bool { return len(h) == 0 }

// Equal compares two hashes.
func (h Hash) Equal(o Hash) bool { return bytes.Equal(h, o) }

// ParseHash parses a hex string ("" -> nil).
func ParseHash(s string) (Hash, error) {
	if s == "" {
		return nil, nil
	}
	b, err := hex.DecodeString(strings.ToLower(s))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid hash %q", ErrInvalidArgument, s)
	}
	return Hash(b), nil
}

// MarshalText implements encoding.TextMarshaler.
func (h Hash) MarshalText() ([]byte, error) { return []byte(h.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (h *Hash) UnmarshalText(b []byte) error {
	v, err := ParseHash(string(b))
	if err != nil {
		return err
	}
	*h = v
	return nil
}

// Duration is a time.Duration that encodes as a Go duration string ("30s",
// "5m", "1h30m") in JSON and YAML. Plain integers are rejected to avoid unit
// ambiguity.
type Duration time.Duration

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// String implements fmt.Stringer.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("%w: invalid duration %q (use Go syntax like 30s, 5m, 1h)", ErrInvalidArgument, string(b))
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON encodes as a JSON string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON decodes a JSON string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("%w: duration must be a string like \"30s\"", ErrInvalidArgument)
	}
	return d.UnmarshalText([]byte(s))
}

// NormalizeVersion strips surrounding whitespace and a leading "v"
// ("v0.162.0" -> "0.162.0"). Collector BuildInfo reports versions without "v"
// (service.version = "0.162.0") while release tags use "v0.162.0".
func NormalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	return strings.TrimPrefix(v, "v")
}

// SourceRef identifies where a declarative (GitOps) object came from.
type SourceRef struct {
	// Path is the repository relative file path, e.g. "fleets/edge-nl.yaml".
	Path string `json:"path" yaml:"path"`
	// Commit is the Git commit SHA (or, for a plain directory source, the
	// "dir:<sha256 of all files>" pseudo revision).
	Commit string `json:"commit" yaml:"commit"`
	// ContentHash is the SHA-256 of the object's canonical JSON spec.
	ContentHash Hash `json:"contentHash,omitempty" yaml:"contentHash,omitempty"`
}
