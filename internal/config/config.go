// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package config defines the configuration of myriarchd.
//
// This package only contains the struct, defaults and documentation. The
// loader (YAML file + env overrides + validation) lives in
// internal/configload.
//
// File format: YAML (myriarchd.yaml). Loader rules (implemented by internal/configload):
//   - Start from Default() (or DevDefaults() for "myriarchd dev"), then decode
//     the YAML file on top (unknown keys are an error).
//   - After decoding, every string field is expanded with ${VAR} and
//     ${VAR:-default} from the process environment (per value, so YAML
//     structure cannot be injected).
//   - Then environment overrides MYRIARCH_<PATH> are applied, where <PATH> is
//     the yaml path in upper case joined with "_" (e.g. MYRIARCH_DATABASE_DSN,
//     MYRIARCH_AUTH_OIDC_CLIENTSECRET, MYRIARCH_SERVER_PUBLICURL). Lists can be
//     overridden with comma separated values.
//   - *File fields (clientSecretFile, dsnFile, tokenFile, ...) are read at
//     startup and take precedence over the inline value. *File fields are read
//     at startup only while their feature is active (see internal/configload):
//     the OIDC files while auth.oidc.enabled, the bootstrap admin files while
//     the bootstrap admin is active, the repository files while gitops.enabled,
//     the token of the release tracker while releases.enabled.
//
// Secrets are provided via *File fields (mounted Kubernetes secrets) or env
// vars, never committed to Git.
package config

import (
	"time"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/domain"
)

// Config is the complete myriarchd configuration.
type Config struct {
	Server        ServerConfig        `yaml:"server"`
	OpAMP         OpAMPConfig         `yaml:"opamp"`
	Database      DatabaseConfig      `yaml:"database"`
	Auth          AuthConfig          `yaml:"auth"`
	GitOps        GitOpsConfig        `yaml:"gitops"`
	Releases      ReleasesConfig      `yaml:"releases"`
	Artifacts     ArtifactsConfig     `yaml:"artifacts"`
	Outbound      OutboundConfig      `yaml:"outbound"`
	Executors     ExecutorsConfig     `yaml:"executors"`
	Rollouts      RolloutsConfig      `yaml:"rollouts"`
	Notifications NotificationsConfig `yaml:"notifications"`
	Retention     RetentionConfig     `yaml:"retention"`
	Telemetry     TelemetryConfig     `yaml:"telemetry"`
	HA            HAConfig            `yaml:"ha"`
	UI            UIConfig            `yaml:"ui"`
	Dev           DevConfig           `yaml:"dev"`
}

// UIConfig configures the language options the web UI receives through the
// public GET /auth/config endpoint.
type UIConfig struct {
	// DefaultLanguage is the BCP 47 tag the UI uses when the user has no
	// stored choice and no browser language matches a shipped locale
	// (default "en").
	DefaultLanguage string `yaml:"defaultLanguage"`
	// Languages restricts the languages the UI offers (empty = every
	// language the UI ships). When set, it must contain DefaultLanguage.
	// Entries match a shipped language by the full tag or the primary subtag
	// ("de-DE" enables "de").
	Languages []string `yaml:"languages"`
}

// ServerConfig configures the main HTTP listener (UI, REST API, OpAMP).
type ServerConfig struct {
	// ListenAddr, default ":8080".
	ListenAddr string `yaml:"listenAddr"`
	// PublicURL is the externally reachable base URL (used for OIDC
	// redirects, artifact download URLs, notification links), e.g.
	// "https://fleet.example.com". Required in production.
	PublicURL string `yaml:"publicUrl"`
	// TLS: if CertFile and KeyFile are set myriarchd terminates TLS itself
	// (otherwise TLS terminates at the ingress and TLS.TerminatedUpstream must
	// be set explicitly in production).
	TLS TLSServerConfig `yaml:"tls"`
	// TrustedProxies (CIDRs) whose X-Forwarded-For / X-Forwarded-Proto /
	// X-Forwarded-Client-Cert headers are honored.
	TrustedProxies []string `yaml:"trustedProxies"`
	// MetricsListenAddr serves /metrics separately (default ":9090", "" =
	// serve /metrics on ListenAddr).
	MetricsListenAddr string `yaml:"metricsListenAddr"`
	// ShutdownTimeout for graceful shutdown (default 30s).
	ShutdownTimeout time.Duration `yaml:"shutdownTimeout"`
}

// TLSServerConfig is server-side TLS.
type TLSServerConfig struct {
	CertFile string `yaml:"certFile"`
	KeyFile  string `yaml:"keyFile"`
	// ClientCAFile enables (optional) client certificate verification for
	// the OpAMP endpoint (mTLS). Clients without certificate are still
	// accepted if OpAMP.RequireClientCert is false.
	ClientCAFile string `yaml:"clientCaFile"`
	// TerminatedUpstream declares that TLS is terminated in front of myriarchd
	// (ingress, load balancer) and that the plaintext listener is only
	// reachable from there. Production refuses to start with a plaintext
	// listener unless this is set (the Helm chart sets it); myriarchd logs a
	// warning at startup.
	TerminatedUpstream bool `yaml:"terminatedUpstream"`
}

// OpAMPConfig configures the OpAMP endpoint.
type OpAMPConfig struct {
	// Path, default "/v1/opamp".
	Path string `yaml:"path"`
	// RequireEnrollmentToken: reject connections without a valid
	// "Authorization: Bearer myre_..." header (default true; dev mode false).
	RequireEnrollmentToken bool `yaml:"requireEnrollmentToken"`
	// RequireClientCert: reject connections without a verified client cert
	// (mTLS, either terminated by myriarchd or forwarded by a trusted proxy).
	RequireClientCert bool `yaml:"requireClientCert"`
	// ClientCertHeader: header carrying the URL-encoded PEM client cert from
	// a trusted proxy (e.g. "X-Forwarded-Client-Cert" or "ssl-client-cert").
	ClientCertHeader string `yaml:"clientCertHeader"`
	// OfflineAfter: an agent without any message for this long is marked
	// offline (default 90s = 3 x supervisor default heartbeat of 30s).
	OfflineAfter time.Duration `yaml:"offlineAfter"`
	// LastSeenFlushInterval: batch interval for last_seen_at writes
	// (default 15s).
	LastSeenFlushInterval time.Duration `yaml:"lastSeenFlushInterval"`
	// MaxMessageSize in bytes (default 4 MiB; opamp-go default is 64 MiB).
	MaxMessageSize int64 `yaml:"maxMessageSize"`
	// AutoRebindOnTokenRotation lets an agent whose bound enrollment token was
	// revoked or expired move to another valid token (same client certificate
	// subject) without an admin action; the move is audited as agent.rebind
	// (default false: POST /agents/{id}/actions/reenroll is required).
	AutoRebindOnTokenRotation bool `yaml:"autoRebindOnTokenRotation"`
	// MaxEffectiveConfigBytes limits the effective config body of one agent
	// that is stored (default 1 MiB); larger bodies are not stored and
	// produce a warning event for the agent.
	MaxEffectiveConfigBytes int64 `yaml:"maxEffectiveConfigBytes"`
	// EnableCompression: WebSocket permessage-deflate (default false). The
	// read limit of the WebSocket library applies to the compressed frame
	// only, so decompressed messages would not be bounded by MaxMessageSize;
	// enable it only if that is acceptable. Production refuses it unless
	// AllowUnboundedCompression acknowledges the risk.
	EnableCompression bool `yaml:"enableCompression"`
	// AllowUnboundedCompression acknowledges that decompressed WebSocket
	// messages are not bounded by MaxMessageSize (default false). Only
	// consulted in production, where EnableCompression requires it.
	AllowUnboundedCompression bool `yaml:"allowUnboundedCompression"`
	// SendTimeout: write deadline for pushing a message to one agent
	// (default 10s). opamp-go's Connection.Send ignores ctx, therefore the
	// implementation sets a deadline on the underlying net.Conn.
	SendTimeout time.Duration `yaml:"sendTimeout"`
	// MaxConnectionsPerReplica: above this, new connections get 503 with
	// Retry-After (default 10000).
	MaxConnectionsPerReplica int `yaml:"maxConnectionsPerReplica"`
	// ConnectRateLimit: authenticated new connections per second per replica
	// (token bucket, burst = 2x; default 200). Excess gets 429 + Retry-After.
	// The bucket is charged after authentication, so unauthenticated
	// requests cannot exhaust it.
	ConnectRateLimit float64 `yaml:"connectRateLimit"`
	// ConnectRatePerIP: connection attempts per second and client IP (token
	// bucket, burst = 2x; default 50; 0 = default). Behind a proxy the client
	// IP is only known if server.trustedProxies lists the proxy; otherwise all
	// agents share the proxy address.
	ConnectRatePerIP float64 `yaml:"connectRatePerIp"`
	// MaxConnectionsPerToken limits concurrent WebSocket connections per
	// enrollment token and replica (default 0 = unlimited).
	MaxConnectionsPerToken int `yaml:"maxConnectionsPerToken"`
	// EnrollmentTokens declares enrollment tokens in the configuration (for
	// example from mounted Kubernetes Secrets). At startup every entry is
	// reconciled into the database: only the SHA-256 hash and the display
	// prefix are stored. Entries are never revoked automatically; removing
	// one only logs a notice (revoke the token in the UI or with
	// DELETE /api/v1/enrollment-tokens/{id}). Not overridable by environment
	// variables; ${VAR} expansion works inside the entries.
	EnrollmentTokens []EnrollmentTokenConfig `yaml:"enrollmentTokens"`
}

// EnrollmentTokenConfig is one declarative enrollment token.
type EnrollmentTokenConfig struct {
	// Name is a DNS label (lower case letters, digits and "-", 1 to 63
	// characters) and unique within the list.
	Name string `yaml:"name"`
	// TokenFile is the absolute path of a file with the token ("myre_"
	// followed by 32 to 120 letters, digits, "_" or "-").
	TokenFile string `yaml:"tokenFile"`
	// Labels are copied to the server-side labels of the agents that enroll
	// with the token (same rules as POST /enrollment-tokens).
	Labels map[string]string `yaml:"labels"`
	// ExpiresAt is an RFC 3339 timestamp; empty = no expiry.
	ExpiresAt string `yaml:"expiresAt"`
}

// DatabaseConfig configures PostgreSQL.
type DatabaseConfig struct {
	// DSN, e.g. "postgres://myriarch:secret@db:5432/myriarch?sslmode=verify-full".
	DSN string `yaml:"dsn"`
	// DSNFile reads the DSN from a file (takes precedence over DSN).
	DSNFile string `yaml:"dsnFile"`
	// MaxConns of the pgx pool (default 20).
	MaxConns int32 `yaml:"maxConns"`
	// MigrateOnStart runs pending migrations at startup (default true).
	// Migrations take a Postgres advisory lock, so concurrent replicas are safe.
	MigrateOnStart bool `yaml:"migrateOnStart"`
	// StatementTimeout is the server-side limit of one SQL statement of the
	// application pool (default 15s; migrations and housekeeping are exempt).
	StatementTimeout time.Duration `yaml:"statementTimeout"`
}

// AuthConfig configures authentication and authorization.
type AuthConfig struct {
	OIDC OIDCConfig `yaml:"oidc"`
	// BootstrapAdmin is the break-glass administrator that works without an
	// identity provider (see docs/operations/authentication.md).
	BootstrapAdmin BootstrapAdminConfig `yaml:"bootstrapAdmin"`
	// DevLogin enables local username/password login (dev mode only).
	DevLogin DevLoginConfig `yaml:"devLogin"`
	// SessionTTL is the idle timeout (default 2h), sliding.
	SessionTTL time.Duration `yaml:"sessionTtl"`
	// SessionMaxAge is the absolute lifetime of a session, independent of
	// activity (default 12h). After it users sign in again, so identity
	// provider changes (groups, disabled accounts) take effect.
	SessionMaxAge time.Duration `yaml:"sessionMaxAge"`
	// APITokenMaxTTL is the maximum lifetime of API tokens (default 90 days): a token created without
	// expiresAt gets it, a longer expiresAt is refused, and an older token stops working. 0 means unlimited
	// (production logs a warning). It is the only way a removal at the identity provider reaches API tokens
	// besides disabling the user in myriarch.
	APITokenMaxTTL time.Duration `yaml:"apiTokenMaxTtl"`
	// CookieSecure forces the Secure cookie flag (default true; dev false). With it the session cookie is
	// named __Host-myriarch_session.
	CookieSecure bool `yaml:"cookieSecure"`
	// DownloadTokenSecretFile: HMAC key for artifact download tokens
	// (32+ random bytes). Generated and stored in the KV store if empty.
	// Tokens: see ArtifactsConfig.DownloadTokenTTL.
	DownloadTokenSecretFile string `yaml:"downloadTokenSecretFile"`
}

// BootstrapAdminConfig configures the bootstrap admin: one administrator
// account whose credentials come from files (a mounted Secret), for the first
// sign-in before single sign-on exists and as a break-glass account later. The
// configuration is the only source of truth: the account is created, enabled
// and disabled at startup, and cannot be changed through the API.
type BootstrapAdminConfig struct {
	Enabled bool `yaml:"enabled"`
	// Username of the account (default "admin"): 1 to 64 characters from
	// letters, digits, ".", "_", "@" and "-".
	Username string `yaml:"username"`
	// PasswordHashFile holds a bcrypt hash (cost 10 or higher) of the
	// password, created with "myriarchd auth hash-password". Re-read on every
	// sign-in attempt, so a rotation needs no restart.
	PasswordHashFile string `yaml:"passwordHashFile"`
	// TokenFile holds a one-time token (32 to 512 characters). It works for
	// one sign-in; writing a new value into the file issues a new one.
	TokenFile string `yaml:"tokenFile"`
	// KeepWithOIDC keeps the account active while auth.oidc is enabled
	// (default false: single sign-on replaces the bootstrap admin).
	KeepWithOIDC bool `yaml:"keepWithOidc"`
}

// BootstrapActive reports whether the bootstrap admin accepts sign-ins: it is
// enabled, has a credential file and is not replaced by single sign-on.
func (a AuthConfig) BootstrapActive() bool {
	b := a.BootstrapAdmin
	return b.Enabled && (b.PasswordHashFile != "" || b.TokenFile != "") && (!a.OIDC.Enabled || b.KeepWithOIDC)
}

// OIDCConfig configures OpenID Connect login against any standards compliant
// identity provider. Provider specific notes are in
// docs/operations/authentication.md.
type OIDCConfig struct {
	Enabled bool `yaml:"enabled"`
	// IssuerURL is the issuer of the identity provider exactly as its
	// discovery document states it, e.g. "https://idp.example.com/realms/otel"
	// (no trailing slash). Some providers need a specific form, which
	// configload checks.
	IssuerURL        string `yaml:"issuerUrl"`
	ClientID         string `yaml:"clientId"`
	ClientSecret     string `yaml:"clientSecret"`
	ClientSecretFile string `yaml:"clientSecretFile"`
	// ClientCertificateFile and ClientKeyFile authenticate the client with a
	// certificate instead of a secret (private_key_jwt, for providers that
	// register a client certificate): PEM certificate of the client
	// registration and its RSA private key (PKCS#1 or PKCS#8). Both or none;
	// excludes ClientSecret and ClientSecretFile. The files are read again
	// when they change.
	ClientCertificateFile string   `yaml:"clientCertificateFile"`
	ClientKeyFile         string   `yaml:"clientKeyFile"`
	Scopes                []string `yaml:"scopes"` // default: openid profile email
	// GroupsClaim, default "groups". The claim may hold a string, a list of
	// strings or an object (its keys count); a dotted path reaches into
	// nested objects.
	GroupsClaim string `yaml:"groupsClaim"`
	// NameClaim is the claim that holds the display name (default "name";
	// "preferred_username" is the fallback when it is empty).
	NameClaim string `yaml:"nameClaim"`
	// EmailClaim is the claim that holds the e-mail address (default "email").
	// "email:" entries of roleMapping.users require the default: email_verified
	// attests only the standard claim.
	EmailClaim string `yaml:"emailClaim"`
	// RoleMapping maps groups (or roles) and individual users to roles; the
	// highest role wins.
	RoleMapping RoleMapping `yaml:"roleMapping"`
	// DefaultRole for users without a matching group. Default "" = deny
	// (access_denied, audited). Only set it deliberately (e.g. "viewer" when
	// every user of the IdP application may read); myriarchd logs a warning.
	DefaultRole domain.Role `yaml:"defaultRole"`
	// DisplayName shown on the login button (e.g. "Company SSO").
	DisplayName string `yaml:"displayName"`
}

// RoleMapping lists group names/IDs per role, plus individual users.
type RoleMapping struct {
	Admin    []string `yaml:"admin"`
	Operator []string `yaml:"operator"`
	Viewer   []string `yaml:"viewer"`
	// Users maps individual users to roles, for identity providers without
	// a groups claim.
	Users UserRoleMapping `yaml:"users"`
}

// UserRoleMapping lists users per role. Entries are "sub:<subject>" (exact
// match of the ID token subject) or "email:<address>" (case-insensitive, only
// for ID tokens with email_verified true).
type UserRoleMapping struct {
	Admin    []string `yaml:"admin"`
	Operator []string `yaml:"operator"`
	Viewer   []string `yaml:"viewer"`
}

// DevLoginConfig configures local accounts (never enable in production).
type DevLoginConfig struct {
	Enabled bool      `yaml:"enabled"`
	Users   []DevUser `yaml:"users"`
}

// DevUser is a local account.
type DevUser struct {
	Username string      `yaml:"username"`
	Password string      `yaml:"password"`
	Role     domain.Role `yaml:"role"`
}

// GitOpsConfig configures the GitOps source.
type GitOpsConfig struct {
	// Enabled, default true.
	Enabled bool `yaml:"enabled"`
	// RepoURL: https://..., ssh://git@..., git@host:org/repo.git or
	// "dir:///abs/path" / "dir:relative/path" for a plain directory (dev,
	// tests; revision = content hash).
	RepoURL string `yaml:"repoUrl"`
	Branch  string `yaml:"branch"` // default "main"
	// Path inside the repository (default "" = root).
	Path string `yaml:"path"`
	// Auth for the repository.
	Auth GitAuthConfig `yaml:"auth"`
	// PollInterval, default 60s.
	PollInterval time.Duration `yaml:"pollInterval"`
	// WebhookSecret / WebhookSecretFile: shared secret for
	// POST /api/v1/gitops/webhook (GitLab "X-Gitlab-Token" or GitHub
	// "X-Hub-Signature-256").
	WebhookSecret     string `yaml:"webhookSecret"`
	WebhookSecretFile string `yaml:"webhookSecretFile"`
	// MaxRepoBytes limits the size of the checked-out repository (default
	// 256 MiB), MaxFiles the number of YAML files (default 2000) and
	// MaxYAMLBytes their total size (default 64 MiB).
	MaxRepoBytes int64 `yaml:"maxRepoBytes"`
	MaxFiles     int   `yaml:"maxFiles"`
	MaxYAMLBytes int64 `yaml:"maxYamlBytes"`
	// CloneDir for the local clone. Default: a directory in the temporary directory, named after the
	// repository URL; production setups set a directory that only the server account can write
	// (a warning names an empty value).
	CloneDir string `yaml:"cloneDir"`
	// UIRepoWebURL builds "view in Git" links, e.g.
	// "https://gitlab.example.com/otel/fleet-config/-/blob/{commit}/{path}".
	UIRepoWebURL string `yaml:"uiRepoWebUrl"`
}

// GitAuthConfig configures Git credentials.
type GitAuthConfig struct {
	// Username + TokenFile for HTTPS (GitLab: username "oauth2" or any).
	Username  string `yaml:"username"`
	TokenFile string `yaml:"tokenFile"`
	// SSHKeyFile + KnownHostsFile for SSH.
	SSHKeyFile     string `yaml:"sshKeyFile"`
	KnownHostsFile string `yaml:"knownHostsFile"`
	// CAFile for self-signed Git servers.
	CAFile string `yaml:"caFile"`
}

// ReleasesConfig configures the upstream release tracker.
type ReleasesConfig struct {
	Enabled bool `yaml:"enabled"` // default true
	// GitHubAPIURL, default "https://api.github.com"; GitHub Enterprise:
	// "https://ghe.example.com/api/v3".
	GitHubAPIURL string `yaml:"githubApiUrl"`
	// RawBaseURL for CHANGELOG files, default
	// "https://raw.githubusercontent.com"; GHES: "https://ghe.example.com/raw".
	RawBaseURL string `yaml:"rawBaseUrl"`
	// TokenFile with a GitHub token (raises the rate limit from 60 to 5000/h).
	TokenFile string `yaml:"tokenFile"`
	// ReleasesRepo, default "open-telemetry/opentelemetry-collector-releases".
	ReleasesRepo string `yaml:"releasesRepo"`
	// CoreRepo, default "open-telemetry/opentelemetry-collector".
	CoreRepo string `yaml:"coreRepo"`
	// ContribRepo, default "open-telemetry/opentelemetry-collector-contrib".
	ContribRepo string `yaml:"contribRepo"`
	// Distributions to track, default ["otelcol-contrib", "opampsupervisor"].
	Distributions []string `yaml:"distributions"`
	// PollInterval, default 1h.
	PollInterval time.Duration `yaml:"pollInterval"`
	// IncludePrereleases, default false.
	IncludePrereleases bool `yaml:"includePrereleases"`
	// MinVersion: ignore older releases on first import (default "0.120.0").
	MinVersion string `yaml:"minVersion"`
	// FixtureDir: read releases/changelogs from recorded files instead of
	// the network (tests, offline dev).
	FixtureDir string `yaml:"fixtureDir"`
}

// ArtifactsConfig configures the artifact cache.
type ArtifactsConfig struct {
	// Dir for cached files. Per replica. Default: a fixed directory in the temporary directory;
	// production setups set a directory that only the server account can write (a warning names an
	// empty value).
	Dir string `yaml:"dir"`
	// SourceBaseURL: where release assets are fetched from. Default
	// "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download".
	// An internal mirror must use the same layout: <base>/<tag>/<asset>.
	SourceBaseURL string `yaml:"sourceBaseUrl"`
	// AgentBaseURLs is an allowlist of base URLs updaters download from
	// ("http://myriarchd:8080"): absolute http(s) URLs without path, query,
	// fragment or credentials. For every package offer myriarchd picks, per
	// updater, the entry whose host:port equals the host:port of the
	// attribute dev.myriarch.updater.server_endpoint the updater reported (the
	// OpAMP endpoint it is connected to); updaters without a match download
	// from server.publicUrl. This lets agents inside a cluster use an
	// internal service name while agents elsewhere keep the public route.
	// http:// entries need AllowInsecureAgentBaseURLs in production.
	AgentBaseURLs []string `yaml:"agentBaseUrls"`
	// AllowInsecureAgentBaseURLs permits http:// entries in AgentBaseURLs in
	// production (a cluster-internal service whose traffic never leaves the
	// namespace); myriarchd logs a warning. Package contents are verified by
	// SHA-256 and sigstore on the updater in any case.
	AllowInsecureAgentBaseURLs bool `yaml:"allowInsecureAgentBaseUrls"`
	// Platforms to prefetch on new target versions, e.g.
	// ["linux/amd64", "windows/amd64"]. Default: empty. Member platforms of
	// updater fleets are always added; an explicit prefetch request that
	// names no platforms falls back to the platforms of the connected
	// collectors that use the requested distribution.
	PrefetchPlatforms []string `yaml:"prefetchPlatforms"`
	// Verification of upstream artifacts.
	Verify ArtifactVerifyConfig `yaml:"verify"`
	// MaxCacheBytes (default 20 GiB); least recently used versions are evicted.
	MaxCacheBytes int64 `yaml:"maxCacheBytes"`
	// DownloadTokenTTL limits the age of updater download tokens (default
	// 24h; 0 = no expiry). A token (bound to artifact id and updater agent
	// id) is accepted only while that agent has a package offer referencing
	// the artifact; the offer is re-sent with a fresh token once the token
	// is older than half of the TTL.
	DownloadTokenTTL time.Duration `yaml:"downloadTokenTtl"`
	// SourceAuth are the credentials myriarchd sends to the artifact mirror
	// (sourceBaseUrl), e.g. an Artifactory access token or a Nexus user. They
	// are sent ONLY to requests with the same origin (scheme, host, port) as
	// sourceBaseUrl, never to redirect targets on other hosts. Hosts that
	// download from the mirror themselves are not affected.
	SourceAuth ArtifactSourceAuth `yaml:"sourceAuth"`
	// ProxyURL for outbound downloads (optional).
	ProxyURL string `yaml:"proxyUrl"`
	// AllowedRedirectHosts lists the hosts upstream downloads may be
	// redirected to (exact or "*.suffix"; https only, at most 5 hops).
	// Default: the GitHub release asset hosts plus the host of sourceBaseUrl.
	AllowedRedirectHosts []string `yaml:"allowedRedirectHosts"`
	// AllowUnverified permits verify.requireChecksum=false or
	// verify.requireSignature=false in production (lab use only).
	AllowUnverified bool `yaml:"allowUnverified"`
}

// ArtifactSourceAuth are the credentials for the artifact mirror: either a
// bearer token (TokenFile) or basic authentication (Username + PasswordFile).
// The files are read again when they change, so tokens can be rotated without
// restarting myriarchd.
type ArtifactSourceAuth struct {
	// Username for basic authentication (Nexus); only together with PasswordFile.
	Username string `yaml:"username"`
	// PasswordFile holds the password or user token for basic authentication.
	PasswordFile string `yaml:"passwordFile"`
	// TokenFile holds a bearer token (Artifactory access token). Excludes
	// Username and PasswordFile.
	TokenFile string `yaml:"tokenFile"`
}

// Configured reports whether any credential is set.
func (a ArtifactSourceAuth) Configured() bool {
	return a.Username != "" || a.PasswordFile != "" || a.TokenFile != ""
}

// ArtifactVerifyConfig configures checksum and signature verification.
type ArtifactVerifyConfig struct {
	// RequireChecksum: verify SHA-256 against the per-asset "<asset>.sha256"
	// file (releases >= v0.158.0; older releases: shared checksums.txt)
	// (default true).
	RequireChecksum bool `yaml:"requireChecksum"`
	// RequireSignature: verify the per-asset sigstore bundle
	// "<asset>.sigstore.json" (cosign keyless, Rekor + TSA) (default true).
	RequireSignature bool `yaml:"requireSignature"`
	// CertificateIdentityRegexp (SAN of the Fulcio certificate). Default
	// accepts the contrib workflow (base-release.yaml) and the supervisor
	// workflow (base-binary-release.yaml) for plain vX.Y.Z tags:
	// "^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-(binary-)?release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$".
	// Additionally the verifier requires the tag to equal the artifact version.
	CertificateIdentityRegexp string `yaml:"certificateIdentityRegexp"`
	// CertificateOIDCIssuer, default "https://token.actions.githubusercontent.com".
	CertificateOIDCIssuer string `yaml:"certificateOidcIssuer"`
	// TrustedRootFile: sigstore trusted_root.json for offline verification;
	// empty = fetch via TUF from the public Sigstore instance.
	TrustedRootFile string `yaml:"trustedRootFile"`
	// TUFRepositoryURL is the base URL of the Sigstore TUF repository or of a
	// 1:1 mirror of it (default "" = https://tuf-repo-cdn.sigstore.dev); the
	// embedded Sigstore root remains the trust anchor. Ignored if
	// TrustedRootFile is set. TUF requests use the same HTTP client as the
	// other outbound connections (proxy environment, outbound.caFile,
	// outbound.allowedCidrs, sourceAuth for the origin of sourceBaseUrl). A
	// mirror must not cache timestamp.json for long (it changes daily).
	TUFRepositoryURL string `yaml:"tufRepositoryUrl"`
}

// OutboundConfig is the destination policy of all outbound connections.
type OutboundConfig struct {
	// AllowedCIDRs opens address ranges (or single IPs) that are denied by
	// default: loopback, private (RFC 1918), shared, link-local (metadata),
	// unique-local and reserved ranges. Example: ["10.20.0.0/16"] for an AWX
	// server or Git server in the internal network. Applies to the resolved
	// address of every connection, including redirects.
	AllowedCIDRs []string `yaml:"allowedCidrs"`
	// CAFile is a PEM file with ADDITIONAL trusted CA certificates (on top of
	// the system roots) for ALL outbound TLS connections of myriarchd: OIDC, Git
	// over HTTPS, GitHub API and raw, the artifact upstream or mirror, the
	// Sigstore TUF repository, webhook and AWX executors, notification
	// channels. Typically the internal CA of the company. myriarchd refuses to
	// start if the file is unreadable or contains no certificate.
	CAFile string `yaml:"caFile"`
}

// ExecutorsConfig holds executor defaults.
type ExecutorsConfig struct {
	// AllowedWebhookHosts restricts webhook/AWX targets (host, host:port,
	// *.example.com; http://host allows plain HTTP). An empty list refuses
	// every webhook and AWX target (fail closed) in every mode; it does not
	// stop startup.
	AllowedWebhookHosts []string `yaml:"allowedWebhookHosts"`
	// DefaultTimeout for outbound executor calls (default 30s).
	DefaultTimeout time.Duration `yaml:"defaultTimeout"`
	// MaxTimeout caps timeouts requested by Executor objects in Git
	// (default 5m).
	MaxTimeout time.Duration `yaml:"maxTimeout"`
	// SecretEnvPrefix: environment variables referenced directly by an
	// Executor object (secretEnv/tokenEnv) must start with this prefix
	// (default `MYRIARCH_EXEC_`), so Git cannot read arbitrary variables.
	SecretEnvPrefix string `yaml:"secretEnvPrefix"`
	// SecretDir is the only directory executor secret and CA files
	// (secretFile/tokenFile/caFile) are read from; names must be relative
	// and may not leave it. Empty = file references are refused.
	SecretDir string `yaml:"secretDir"`
	// Secrets is the registry of named secrets. An Executor object may use a
	// registry name as secretEnv/tokenEnv/secretFile/tokenFile value; the
	// secret is then only sent to the listed hosts.
	Secrets map[string]ExecutorSecret `yaml:"secrets"`
	// SecretEnvHosts binds environment variables that are referenced by their
	// prefixed name (secretEnv/tokenEnv) to the only destinations they may be
	// sent to (host, host:port or "*.example.com"). In production an
	// unbound prefixed variable is refused.
	SecretEnvHosts map[string][]string `yaml:"secretEnvHosts"`
	// SecretFileHosts is SecretEnvHosts for files below SecretDir
	// (secretFile/tokenFile), keyed by the relative file name.
	SecretFileHosts map[string][]string `yaml:"secretFileHosts"`
	// RequireSecretHosts makes a secret referenced from Git without a
	// destination binding (Secrets, SecretEnvHosts, SecretFileHosts) an error.
	// Set by myriarchd outside dev mode; not read from the configuration file.
	RequireSecretHosts bool `yaml:"-"`
}

// ExecutorSecret is one entry of ExecutorsConfig.Secrets.
type ExecutorSecret struct {
	// Env is the environment variable that holds the secret (or File).
	Env string `yaml:"env"`
	// File is a file below executors.secretDir (or an absolute path
	// configured here by the operator).
	File string `yaml:"file"`
	// AllowedHosts: the only destinations this secret may be sent to
	// (host, host:port or "*.example.com"). Required.
	AllowedHosts []string `yaml:"allowedHosts"`
}

// RolloutsConfig configures the rollout engine.
type RolloutsConfig struct {
	// TickInterval of the engine loop on the leader (default 5s).
	TickInterval time.Duration `yaml:"tickInterval"`
	// DefaultTimezone for maintenance windows when an agent has no valid
	// timezone attribute (default "UTC").
	DefaultTimezone string `yaml:"defaultTimezone"`
	// AutoStart: start rollouts created by GitOps immediately (default true).
	AutoStart bool `yaml:"autoStart"`
	// ExecutorTimeout is the hard limit of one executor call (Start/Cancel
	// of the webhook/AWX/updater executors; default 2m). The engine never
	// waits for a slow executor inside a tick: calls run in the background
	// and their results are applied on a later tick.
	ExecutorTimeout time.Duration `yaml:"executorTimeout"`
}

// NotificationsConfig configures notification channels and routing.
type NotificationsConfig struct {
	Channels []NotificationChannel `yaml:"channels"`
}

// NotificationChannel is one destination.
type NotificationChannel struct {
	Name string `yaml:"name"`
	// Type: "webhook" | "teams" | "slack".
	Type string `yaml:"type"`
	// URL / URLFile (incoming webhook URLs are secrets).
	URL     string `yaml:"url"`
	URLFile string `yaml:"urlFile"`
	// SecretFile: HMAC secret for type webhook (header X-Myriarch-Signature).
	SecretFile string `yaml:"secretFile"`
	// Events routed to this channel, e.g. ["release.new", "rollout.*"];
	// empty = all.
	Events []string `yaml:"events"`
	// MinSeverity: "info" (default), "warning", "critical".
	MinSeverity string `yaml:"minSeverity"`
}

// RetentionConfig configures housekeeping (leader job, runs hourly).
type RetentionConfig struct {
	AgentEvents        time.Duration `yaml:"agentEvents"`        // default 30d
	Audit              time.Duration `yaml:"audit"`              // default 400d
	OfflineAgents      time.Duration `yaml:"offlineAgents"`      // default 30d, 0 = keep
	FinishedRollouts   time.Duration `yaml:"finishedRollouts"`   // default 180d
	UnreferencedConfig time.Duration `yaml:"unreferencedConfig"` // default 7d
	GitOpsSyncHistory  time.Duration `yaml:"gitopsSyncHistory"`  // default 30d
}

// TelemetryConfig configures myriarchd's own logs and metrics.
type TelemetryConfig struct {
	// LogLevel: debug|info|warn|error (default info).
	LogLevel string `yaml:"logLevel"`
	// LogFormat: json|text (default json; dev text).
	LogFormat string `yaml:"logFormat"`
	// Metrics enables /metrics (default true).
	Metrics bool `yaml:"metrics"`
}

// HAConfig configures multi-replica operation.
type HAConfig struct {
	// ReplicaID, default: hostname + "-" + random suffix.
	ReplicaID string `yaml:"replicaId"`
	// LeaderLockKey is the Postgres advisory lock key (default 7012024).
	LeaderLockKey int64 `yaml:"leaderLockKey"`
	// HeartbeatInterval of the replica registry (default 10s).
	HeartbeatInterval time.Duration `yaml:"heartbeatInterval"`
}

// DevConfig configures "myriarchd dev".
type DevConfig struct {
	// DataDir for embedded Postgres, clone dir, artifacts (default ".dev").
	DataDir string `yaml:"dataDir"`
	// PostgresPort for embedded Postgres (default 54329).
	PostgresPort uint32 `yaml:"postgresPort"`
	// SeedEnrollmentToken is created at startup (default
	// "myre_devtoken_insecure_do_not_use_in_prod").
	SeedEnrollmentToken string `yaml:"seedEnrollmentToken"`
}

// Default returns the production defaults described on the fields.
func Default() Config {
	return Config{
		Server: ServerConfig{
			ListenAddr:        ":8080",
			MetricsListenAddr: ":9090",
			ShutdownTimeout:   30 * time.Second,
		},
		OpAMP: OpAMPConfig{
			Path:                     "/v1/opamp",
			RequireEnrollmentToken:   true,
			OfflineAfter:             90 * time.Second,
			LastSeenFlushInterval:    15 * time.Second,
			MaxMessageSize:           4 << 20,
			MaxEffectiveConfigBytes:  1 << 20,
			EnableCompression:        false,
			SendTimeout:              10 * time.Second,
			MaxConnectionsPerReplica: 10000,
			ConnectRateLimit:         200,
			ConnectRatePerIP:         50,
		},
		Database: DatabaseConfig{MaxConns: 20, MigrateOnStart: true, StatementTimeout: 15 * time.Second},
		Auth: AuthConfig{
			OIDC: OIDCConfig{
				Scopes:      []string{"openid", "profile", "email"},
				GroupsClaim: "groups",
				NameClaim:   "name",
				EmailClaim:  "email",
				DefaultRole: "", // deny users without a mapped role
				DisplayName: "SSO",
			},
			BootstrapAdmin: BootstrapAdminConfig{Username: "admin"},
			SessionTTL:     2 * time.Hour,
			SessionMaxAge:  12 * time.Hour,
			APITokenMaxTTL: 90 * 24 * time.Hour,
			CookieSecure:   true,
		},
		GitOps: GitOpsConfig{
			Enabled:      true,
			Branch:       "main",
			PollInterval: 60 * time.Second,
			MaxRepoBytes: 256 << 20,
			MaxFiles:     2000,
			MaxYAMLBytes: 64 << 20,
		},
		Releases: ReleasesConfig{
			Enabled:       true,
			GitHubAPIURL:  "https://api.github.com",
			RawBaseURL:    "https://raw.githubusercontent.com",
			ReleasesRepo:  "open-telemetry/opentelemetry-collector-releases",
			CoreRepo:      "open-telemetry/opentelemetry-collector",
			ContribRepo:   "open-telemetry/opentelemetry-collector-contrib",
			Distributions: []string{domain.DistributionContrib, domain.DistributionSupervisor},
			PollInterval:  time.Hour,
			MinVersion:    "0.120.0",
		},
		Artifacts: ArtifactsConfig{
			SourceBaseURL: "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download",
			Verify: ArtifactVerifyConfig{
				RequireChecksum:           true,
				RequireSignature:          true,
				CertificateIdentityRegexp: `^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-(binary-)?release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$`,
				CertificateOIDCIssuer:     "https://token.actions.githubusercontent.com",
			},
			MaxCacheBytes:    20 << 30,
			DownloadTokenTTL: 24 * time.Hour,
		},
		Executors: ExecutorsConfig{DefaultTimeout: 30 * time.Second, MaxTimeout: 5 * time.Minute, SecretEnvPrefix: brand.ExecSecretEnvPrefix},
		Rollouts: RolloutsConfig{
			TickInterval:    5 * time.Second,
			DefaultTimezone: "UTC",
			AutoStart:       true,
			ExecutorTimeout: 2 * time.Minute,
		},
		Retention: RetentionConfig{
			AgentEvents:        30 * 24 * time.Hour,
			Audit:              400 * 24 * time.Hour,
			OfflineAgents:      30 * 24 * time.Hour,
			FinishedRollouts:   180 * 24 * time.Hour,
			UnreferencedConfig: 7 * 24 * time.Hour,
			GitOpsSyncHistory:  30 * 24 * time.Hour,
		},
		UI:        UIConfig{DefaultLanguage: "en"},
		Telemetry: TelemetryConfig{LogLevel: "info", LogFormat: "json", Metrics: true},
		HA: HAConfig{
			LeaderLockKey:     7012024,
			HeartbeatInterval: 10 * time.Second,
		},
		Dev: DevConfig{
			DataDir:             ".dev",
			PostgresPort:        54329,
			SeedEnrollmentToken: brand.EnrollmentTokenPrefix + "devtoken_insecure_do_not_use_in_prod",
		},
	}
}

// DevDefaults returns Default() adjusted for "myriarchd dev": embedded
// Postgres (DSN filled in by the dev bootstrap), dev login (admin/admin,
// operator/operator, viewer/viewer), no enrollment token required, text logs,
// insecure cookies, GitOps from dir:examples/quickstart, release tracker
// using recorded fixtures if FixtureDir is set.
func DevDefaults() Config {
	c := Default()
	c.Server.PublicURL = "http://localhost:8080"
	c.Server.MetricsListenAddr = ""
	c.OpAMP.RequireEnrollmentToken = false
	c.Auth.CookieSecure = false
	c.Auth.DevLogin = DevLoginConfig{Enabled: true, Users: []DevUser{
		{Username: "admin", Password: "admin", Role: domain.RoleAdmin},
		{Username: "operator", Password: "operator", Role: domain.RoleOperator},
		{Username: "viewer", Password: "viewer", Role: domain.RoleViewer},
	}}
	c.GitOps.RepoURL = "dir:examples/quickstart"
	c.GitOps.PollInterval = 10 * time.Second
	c.Rollouts.TickInterval = 2 * time.Second
	c.Telemetry.LogFormat = "text"
	c.Telemetry.LogLevel = "debug"
	c.Artifacts.Dir = ".dev/artifacts"
	c.GitOps.CloneDir = ".dev/gitops"
	return c
}
