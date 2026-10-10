// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/google/uuid"
)

// Distribution names.
const (
	DistributionContrib    = "otelcol-contrib"
	DistributionSupervisor = "opampsupervisor"
)

// Release is an upstream release of a distribution detected by the release
// tracker.
type Release struct {
	Distribution string `json:"distribution"`
	// Version without "v" ("0.162.0").
	Version     string     `json:"version"`
	Tag         string     `json:"tag"` // "v0.162.0" or "cmd/opampsupervisor/v0.162.0"
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	Prerelease  bool       `json:"prerelease"`
	HTMLURL     string     `json:"htmlUrl,omitempty"`
	// Notes is the release body (Markdown) as published.
	Notes string `json:"notes,omitempty"`
	// BreakingChanges/Deprecations are extracted from the core and contrib
	// CHANGELOG.md / CHANGELOG-API.md sections of exactly this version.
	BreakingChanges []ChangelogEntry `json:"breakingChanges"`
	Deprecations    []ChangelogEntry `json:"deprecations"`
	Assets          []ReleaseAsset   `json:"assets"`
	DetectedAt      time.Time        `json:"detectedAt"`
	AcknowledgedBy  string           `json:"acknowledgedBy,omitempty"`
	AcknowledgedAt  *time.Time       `json:"acknowledgedAt,omitempty"`
}

// ChangelogEntry is one bullet of a changelog section.
type ChangelogEntry struct {
	// Source: "core" (opentelemetry-collector), "contrib"
	// (opentelemetry-collector-contrib) or "releases"
	// (opentelemetry-collector-releases).
	Source string `json:"source"`
	// File: "CHANGELOG.md" (user facing) or "CHANGELOG-API.md" (Go API).
	File string `json:"file"`
	// Version the entry belongs to (without "v").
	Version string `json:"version"`
	// Component is the text in back-ticks at the start of the bullet, e.g.
	// "receiver/hostmetrics" or "pkg/ottl" (may be empty).
	Component string `json:"component,omitempty"`
	// Text is the bullet text (Markdown, without the component prefix).
	Text string `json:"text"`
	// Issues are referenced issue/PR numbers (#1234).
	Issues []int `json:"issues,omitempty"`
}

// ReleaseAsset is one primary downloadable file of a release. Companion
// files (.sha256, .sigstore.json, .sbom.json, ...) are not listed. OS/Arch are
// normalized: MSI "x64" -> "amd64", "x86" -> "386"; Format is one of
// tar.gz|zip|msi|deb|rpm|apk|binary.
type ReleaseAsset struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Size        int64  `json:"size"`
	OS          string `json:"os,omitempty"`   // linux|windows|darwin
	Arch        string `json:"arch,omitempty"` // amd64|arm64|ppc64le|s390x
	Format      string `json:"format,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

// VersionComparison summarizes changes between two versions.
type VersionComparison struct {
	Distribution    string           `json:"distribution"`
	From            string           `json:"from"`
	To              string           `json:"to"`
	Versions        []string         `json:"versions"` // versions in (from, to], ascending
	BreakingChanges []ChangelogEntry `json:"breakingChanges"`
	Deprecations    []ChangelogEntry `json:"deprecations"`
}

// ArtifactStatus is the cache state of an artifact.
type ArtifactStatus string

const (
	ArtifactPending     ArtifactStatus = "pending"
	ArtifactDownloading ArtifactStatus = "downloading"
	ArtifactReady       ArtifactStatus = "ready"
	ArtifactFailed      ArtifactStatus = "failed"
	// ArtifactDeleted only appears in bus events (artifact.changed): the
	// record was removed and every replica drops its local file.
	ArtifactDeleted ArtifactStatus = "deleted"
)

// Artifact is a cached, verified release asset served to updaters.
type Artifact struct {
	ID           uuid.UUID `json:"id"`
	Distribution string    `json:"distribution"`
	Version      string    `json:"version"`
	OS           string    `json:"os"`
	Arch         string    `json:"arch"`
	Format       string    `json:"format"` // tar.gz|zip
	Filename     string    `json:"filename"`
	Size         int64     `json:"size"`
	SHA256       Hash      `json:"sha256,omitempty"`
	// Signature verification result (cosign keyless sigstore bundle
	// "<asset>.sigstore.json", see docs/design/upstream-notes.md, Release assets
	// and signatures > Signatures).
	SignatureVerified bool   `json:"signatureVerified"`
	SignatureIdentity string `json:"signatureIdentity,omitempty"`
	VerificationError string `json:"verificationError,omitempty"`
	// SigstoreBundle is the raw bundle JSON (forwarded to updaters as
	// DownloadableFile.signature so they can verify offline).
	SigstoreBundle []byte         `json:"-"`
	SourceURL      string         `json:"sourceUrl"`
	Status         ArtifactStatus `json:"status"`
	Error          string         `json:"error,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

// GitOpsSyncStatus summarizes the GitOps syncer state.
type GitOpsSyncStatus struct {
	// Source is the repository URL (credentials stripped) or "dir:<path>".
	Source string `json:"source"`
	Branch string `json:"branch,omitempty"`
	Path   string `json:"path,omitempty"`
	// State: "ok", "error" (last sync failed; previous snapshot stays
	// active), "syncing", "never".
	State         string     `json:"state"`
	LastSyncAt    *time.Time `json:"lastSyncAt,omitempty"`
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`
	// AppliedCommit is the commit whose snapshot is active.
	AppliedCommit *GitCommit `json:"appliedCommit,omitempty"`
	// LatestCommit is the newest commit seen (may be invalid).
	LatestCommit *GitCommit         `json:"latestCommit,omitempty"`
	Issues       Issues             `json:"issues"`
	ObjectCounts map[ObjectKind]int `json:"objectCounts"`
}

// GitCommit describes a commit.
type GitCommit struct {
	SHA     string    `json:"sha"`
	Author  string    `json:"author,omitempty"`
	Message string    `json:"message,omitempty"`
	Time    time.Time `json:"time"`
}

// GitOpsSync is one entry of the sync history.
type GitOpsSync struct {
	ID         int64      `json:"id"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Trigger    string     `json:"trigger"` // poll|webhook|manual|startup
	Commit     *GitCommit `json:"commit,omitempty"`
	Result     string     `json:"result"` // applied|unchanged|invalid|error
	Issues     Issues     `json:"issues"`
	// Changes lists "<Kind>/<name>" objects added/changed/removed.
	Added   []string `json:"added,omitempty"`
	Changed []string `json:"changed,omitempty"`
	Removed []string `json:"removed,omitempty"`
}
