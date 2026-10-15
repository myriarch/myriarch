// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package brand holds the fixed identifiers of the project (ADR-0015,
// ADR-0029): names, binaries, environment variables, namespaces, wire names,
// cookies, headers and token prefixes.
//
// The package contains constants only and imports nothing, so every other
// package can depend on it. Runtime branding (white labelling) is out of scope.
// Changing a value breaks agents, deployments, stored tokens or user
// configuration: the values are part of the product contract, and
// brand_test.go pins every one of them.
package brand

// Identity of the project.
const (
	Name        = "myriarch"
	DisplayName = "Myriarch"
	Authors     = "The Myriarch Authors"
	Domain      = "myriarch.dev"
	Website     = "https://myriarch.dev"
	ModulePath  = "github.com/myriarch/myriarch"
	Repository  = "https://github.com/myriarch/myriarch"
)

// Binaries. The names are lower case, also at the start of a sentence.
const (
	ServerBinary  = "myriarchd"
	CLIBinary     = "myrctl"
	CLIAlias      = "myr" // documented, not shipped as a second binary
	UpdaterBinary = "myriarch-updater"
	SimBinary     = "myriarch-sim"
	MCPBinary     = "myriarch-mcp" // reserved for the future MCP server
)

// Environment variable names.
const (
	// EnvPrefix prefixes every server configuration override (MYRIARCH_<PATH>).
	EnvPrefix = "MYRIARCH_"
	// ExecSecretEnvPrefix is the default of executors.secretEnvPrefix: the
	// only variables GitOps executor secrets may read.
	ExecSecretEnvPrefix = "MYRIARCH_EXEC_"
	// EnrollmentTokenEnv carries the enrollment token on managed hosts.
	EnrollmentTokenEnv = "MYRIARCH_ENROLLMENT_TOKEN"
	// OpAMPEndpointEnv carries the OpAMP endpoint of the site.
	OpAMPEndpointEnv = "MYRIARCH_OPAMP_ENDPOINT"
)

// Namespaces of attributes, Kubernetes keys, the GitOps API and metrics.
const (
	AttributePrefix       = "dev.myriarch."
	KubernetesLabelPrefix = "myriarch.dev/"
	APIGroup              = "myriarch.dev"
	APIVersion            = "myriarch.dev/v1alpha1"
	MetricsNamespace      = "myriarch"
	SimMetricsNamespace   = "myriarch_sim"
)

// OpAMP wire names.
const (
	// RemoteConfigKey is the file name of the rendered configuration in the
	// AgentConfigMap (a file name, not an attribute).
	RemoteConfigKey = "myriarch.yaml"

	UpdaterServiceName = "myriarch-updater"
	UpdaterDisplayName = "Myriarch Updater"

	AttrCollectorInstanceUID         = "dev.myriarch.collector.instance_uid"
	AttrUpdaterInstallMethod         = "dev.myriarch.updater.install_method"
	AttrUpdaterServerEndpoint        = "dev.myriarch.updater.server_endpoint"
	AttrUpdaterImageCollectorVersion = "dev.myriarch.updater.image_collector_version"
	AttrUpdaterSupervisorService     = "dev.myriarch.updater.supervisor_service"
	UpdateHealthComponent            = "dev.myriarch.update"
)

// HTTP names: cookies, webhook headers, AWX key and authentication realms.
const (
	SessionCookie = "myriarch_session"
	// SessionCookieHost is the name of the session cookie when cookies are Secure (auth.cookieSecure): the
	// __Host- prefix makes browsers accept it only with Secure, Path=/ and no Domain, so a sibling host cannot
	// plant a cookie of the same name for the parent domain.
	SessionCookieHost = "__Host-" + SessionCookie
	CSRFCookie        = "myriarch_csrf"
	OIDCStateCookie   = "myriarch_oidc"

	// WebhookHeaderPrefix is reserved: executors may not set headers with it.
	WebhookHeaderPrefix = "X-Myriarch-"
	DeliveryHeader      = "X-Myriarch-Delivery"
	TimestampHeader     = "X-Myriarch-Timestamp"
	SignatureHeader     = "X-Myriarch-Signature"

	// AWXExtraVarsKey is the extra_vars key that carries the rollout payload.
	AWXExtraVarsKey = "myriarch"

	AuthRealm          = "myriarch"
	ArtifactsAuthRealm = "myriarch artifacts"
)

// Token prefixes ('myr' + type letter + underscore). API and enrollment
// tokens are the prefix followed by 32 base62 characters; only the SHA-256 of
// the full token is stored. Download tokens are the prefix followed by a
// base64url payload, a dot and a base64url MAC; they are not stored.
//
// Secret-scanning patterns: myr[ae]_[0-9A-Za-z]{32} and
// myrd_[0-9A-Za-z_-]+\.[0-9A-Za-z_-]+.
const (
	APITokenPrefix        = "myra_"
	EnrollmentTokenPrefix = "myre_"
	DownloadTokenPrefix   = "myrd_"
)

// Installation of hosts from the server (ADR-0046).
const (
	// EnrollmentTokenPlaceholder stands for the enrollment token in a rendered
	// install command. The web UI replaces it in the browser, so the server
	// never sees the token that it stands for.
	EnrollmentTokenPlaceholder = "@@" + EnrollmentTokenEnv + "@@"
	// InstallSetupDir is the name of the private working directory of the
	// copied install command: below the home directory of root on Linux
	// (mktemp -d), below %ProgramData% on Windows.
	InstallSetupDir = "myriarch-setup"
)

// NotifyChannel is the PostgreSQL LISTEN/NOTIFY channel of the event bus.
const NotifyChannel = "myriarch_events"
