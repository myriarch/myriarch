// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/myriarch/myriarch/internal/brand"
)

// Role is an RBAC role. Roles are ordered: viewer < operator < admin.
type Role string

const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleViewer || r == RoleOperator || r == RoleAdmin }

// AtLeast reports whether r grants at least the privileges of other.
func (r Role) AtLeast(other Role) bool { return roleRank(r) >= roleRank(other) && roleRank(other) > 0 }

func roleRank(r Role) int {
	switch r {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// Providers of users and the reserved subject prefixes of the accounts that
// do not come from an identity provider. OIDC callbacks reject subjects with
// these prefixes.
const (
	ProviderOIDC      = "oidc"
	ProviderLocal     = "local"
	ProviderBootstrap = "bootstrap"

	LocalSubjectPrefix     = "local:"
	BootstrapSubjectPrefix = "bootstrap:"
)

// User is a human user (OIDC, dev/local login or the bootstrap admin).
type User struct {
	ID       uuid.UUID `json:"id"`
	Subject  string    `json:"subject"`  // OIDC "sub" (or "local:<name>", "bootstrap:<name>")
	Provider string    `json:"provider"` // "oidc" | "local" | "bootstrap"
	// Issuer of the OIDC provider the subject belongs to ("" for local users).
	Issuer string `json:"-"`
	Email  string `json:"email,omitempty"`
	Name   string `json:"name,omitempty"`
	Role   Role   `json:"role"`
	// RoleSource: "mapping" (derived from OIDC groups at each login) or
	// "manual" (set by an admin; overrides mapping).
	RoleSource  string     `json:"roleSource"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`
}

// Session is a server-side browser session (cookie `myriarch_session`).
type Session struct {
	// ID is the SHA-256 (hex) of the random session secret in the cookie.
	ID         string    `json:"-"`
	UserID     uuid.UUID `json:"userId"`
	CSRFToken  string    `json:"-"`
	CreatedAt  time.Time `json:"createdAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	UserAgent  string    `json:"userAgent,omitempty"`
	RemoteAddr string    `json:"remoteAddr,omitempty"`
}

// Token prefixes. Tokens are "<prefix><base62 random 32 chars>"; only the
// SHA-256 of the full token is stored, plus the first 12 characters
// ("display prefix") for identification in the UI.
const (
	APITokenPrefix        = brand.APITokenPrefix
	EnrollmentTokenPrefix = brand.EnrollmentTokenPrefix
	DownloadTokenPrefix   = brand.DownloadTokenPrefix // short-lived, HMAC signed, not stored
)

// APIToken is an automation token with a fixed role.
type APIToken struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	DisplayPrefix string    `json:"displayPrefix"`
	Hash          Hash      `json:"-"`
	Role          Role      `json:"role"`
	CreatedBy     string    `json:"createdBy"`
	// CreatedByUserID is the user who created the token (nil for tokens
	// created by automation or before the field existed). The token is
	// limited to the creator's current role and stops working when the
	// creator is disabled.
	CreatedByUserID *uuid.UUID `json:"-"`
	CreatedAt       time.Time  `json:"createdAt"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt      *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt       *time.Time `json:"revokedAt,omitempty"`
}

// RoleRank orders roles (viewer < operator < admin); unknown roles rank 0.
func RoleRank(r Role) int { return roleRank(r) }

// EnrollmentToken authenticates agents (supervisor and myriarch-updater) on the
// OpAMP endpoint: "Authorization: Bearer myre_...". Labels are copied to the
// agent's server-side labels on registration.
type EnrollmentToken struct {
	ID            uuid.UUID         `json:"id"`
	Name          string            `json:"name"`
	DisplayPrefix string            `json:"displayPrefix"`
	Hash          Hash              `json:"-"`
	Labels        map[string]string `json:"labels,omitempty"`
	CreatedBy     string            `json:"createdBy"`
	CreatedAt     time.Time         `json:"createdAt"`
	ExpiresAt     *time.Time        `json:"expiresAt,omitempty"`
	LastUsedAt    *time.Time        `json:"lastUsedAt,omitempty"`
	RevokedAt     *time.Time        `json:"revokedAt,omitempty"`
	UseCount      int64             `json:"useCount"`
}

// ActorType classifies who performed an audited action.
type ActorType string

const (
	ActorUser   ActorType = "user"
	ActorToken  ActorType = "token"
	ActorSystem ActorType = "system"
	ActorGitOps ActorType = "gitops"
	ActorAgent  ActorType = "agent"
)

// AuditEvent is an immutable audit log entry.
type AuditEvent struct {
	ID         int64          `json:"id"`
	Time       time.Time      `json:"time"`
	ActorType  ActorType      `json:"actorType"`
	ActorID    string         `json:"actorId"`
	ActorName  string         `json:"actorName"`
	Action     string         `json:"action"` // e.g. "rollout.pause", "token.create"
	TargetType string         `json:"targetType,omitempty"`
	TargetID   string         `json:"targetId,omitempty"`
	Result     string         `json:"result"` // success|failure
	Details    map[string]any `json:"details,omitempty"`
	SourceIP   string         `json:"sourceIp,omitempty"`
	RequestID  string         `json:"requestId,omitempty"`
}

// Replica describes one running myriarchd instance.
type Replica struct {
	ID              string    `json:"id"`
	Hostname        string    `json:"hostname"`
	Version         string    `json:"version"`
	StartedAt       time.Time `json:"startedAt"`
	LastHeartbeatAt time.Time `json:"lastHeartbeatAt"`
	IsLeader        bool      `json:"isLeader"`
	Connections     int       `json:"connections"`
}

// SavedFilter is a named agent-list filter of a user (UI).
type SavedFilter struct {
	ID        uuid.UUID      `json:"id"`
	UserID    uuid.UUID      `json:"userId"`
	Name      string         `json:"name"`
	Page      string         `json:"page"` // "agents"
	Query     map[string]any `json:"query"`
	Shared    bool           `json:"shared"`
	CreatedAt time.Time      `json:"createdAt"`
}

// Format rules of credentials that are read from files or sent by clients.
// They are shared by the configuration validation (internal/configload) and
// the authentication service (internal/authn), which must not import each
// other.

// Limits of the bootstrap admin credentials and of declarative labels.
const (
	// MinBcryptCost is the lowest bcrypt cost accepted for the hash of the
	// bootstrap admin password.
	MinBcryptCost = 10
	// BootstrapTokenMinLen and BootstrapTokenMaxLen bound the one-time token
	// of the bootstrap admin (characters).
	BootstrapTokenMinLen = 32
	BootstrapTokenMaxLen = 512

	maxLabels      = 20
	maxLabelKey    = 63
	maxLabelValue  = 253
	labelsRuleText = `at most 20 labels; keys 1 to 63 characters without whitespace or "=", values at most 253 characters`
)

// LabelsRule describes the label rule for messages.
const LabelsRule = labelsRuleText

// bootstrapUsernameRE is the form of the bootstrap admin user name.
var bootstrapUsernameRE = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// ValidBootstrapUsername reports whether name is a valid bootstrap admin user
// name: 1 to 64 characters from letters, digits, ".", "_", "@" and "-".
func ValidBootstrapUsername(name string) bool { return bootstrapUsernameRE.MatchString(name) }

// enrollmentTokenRE is the form of an enrollment token.
var enrollmentTokenRE = regexp.MustCompile(`^` + regexp.QuoteMeta(EnrollmentTokenPrefix) + `[A-Za-z0-9_-]{32,120}$`)

// ValidEnrollmentToken reports whether tok has the form of an enrollment token: the prefix followed by
// 32 to 120 letters, digits, "_" or "-". Tokens of another form are rejected without a lookup.
func ValidEnrollmentToken(tok string) bool { return enrollmentTokenRE.MatchString(tok) }

// dnsLabelRE is a DNS label: lower case letters, digits and "-", 1 to 63
// characters, starting and ending with a letter or digit.
var dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidDNSLabel reports whether s is a DNS label (names of declarative
// enrollment tokens).
func ValidDNSLabel(s string) bool { return dnsLabelRE.MatchString(s) }

// CheckLabels applies the rule for the labels of an enrollment token: at most
// 20 labels, keys of 1 to 63 characters without whitespace or "=", values of
// at most 253 characters. It returns nil for valid labels.
func CheckLabels(labels map[string]string) *Issue {
	if len(labels) > maxLabels {
		return &Issue{Severity: SeverityError, Code: "too_many_labels", Path: "labels", Message: "at most 20 labels are allowed"}
	}
	for k, v := range labels {
		if k == "" || len(k) > maxLabelKey || len(v) > maxLabelValue || strings.ContainsFunc(k, unicode.IsSpace) || strings.Contains(k, "=") {
			return &Issue{Severity: SeverityError, Code: "invalid_label", Path: "labels." + k,
				Message: "label keys must be 1-63 characters without spaces or '=', values at most 253 characters"}
		}
	}
	return nil
}

// BootstrapHashProblem checks the (trimmed) content of the password hash file
// of the bootstrap admin: a bcrypt hash with the prefix $2a$, $2b$ or $2y$ and
// a cost of MinBcryptCost or higher. It returns "" when the hash is fine,
// otherwise the reason (without the file name and never the content).
func BootstrapHashProblem(hash string) string {
	reason := fmt.Sprintf("does not contain a bcrypt hash ($2a$, $2b$ or $2y$) with cost %d or higher", MinBcryptCost)
	if !(strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$") || strings.HasPrefix(hash, "$2y$")) {
		return reason
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil || cost < MinBcryptCost {
		return reason
	}
	return ""
}

// BootstrapTokenProblem checks the (trimmed) content of the token file of the
// bootstrap admin. It returns "" when the token is fine, otherwise the reason.
func BootstrapTokenProblem(tok string) string {
	n := utf8.RuneCountInString(tok)
	if n < BootstrapTokenMinLen || n > BootstrapTokenMaxLen ||
		strings.ContainsFunc(tok, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Sprintf("must contain a token of %d to %d characters without whitespace", BootstrapTokenMinLen, BootstrapTokenMaxLen)
	}
	return ""
}

// EnrollmentTokenProblem checks the (trimmed) content of the file of a
// declarative enrollment token. It returns "" when the token is fine,
// otherwise the reason.
func EnrollmentTokenProblem(tok string) string {
	if !enrollmentTokenRE.MatchString(tok) {
		return fmt.Sprintf(`must contain an enrollment token: %s followed by 32 to 120 letters, digits, "_" or "-"`, EnrollmentTokenPrefix)
	}
	return ""
}
