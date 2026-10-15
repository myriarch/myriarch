// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/gitops"
	"github.com/myriarch/myriarch/internal/netguard"
	"github.com/myriarch/myriarch/internal/notify/channels"
	"github.com/myriarch/myriarch/internal/release"
)

// Validate checks required fields and value ranges. In production mode
// (dev=false) it also rejects insecure settings: auth.devLogin.enabled,
// auth.cookieSecure=false, missing server.publicUrl,
// opamp.requireEnrollmentToken=false without opamp.requireClientCert, a
// plaintext listener without server.tls.terminatedUpstream, http:// URLs for
// credential-bearing outbound calls (Git, release tracker, notification
// channels, artifact upstream), Git sources that are not https/ssh, disabled
// artifact verification without artifacts.allowUnverified and webhook
// notification channels without secretFile. An empty
// executors.allowedWebhookHosts is valid: it refuses every webhook and AWX
// target. Returns a *domain.ValidationError listing all problems.
func Validate(c *config.Config, dev bool) error {
	return ValidateOptions(c, Options{Dev: dev})
}

// ValidateOptions is Validate with the options of the loader: it reads the
// files that the configuration refers to only if opts.CheckFiles is set (and
// not opts.SkipFiles); otherwise their paths count as present.
func ValidateOptions(c *config.Config, opts Options) error {
	dev := opts.Dev
	v := &validator{skipFiles: !opts.checksFiles()}

	// ---------------------------------------------------------- server ---
	if strings.TrimSpace(c.Server.ListenAddr) == "" {
		v.add("server.listenAddr", "must not be empty")
	}
	if c.Server.PublicURL == "" {
		if !dev {
			v.add("server.publicUrl", "is required (externally reachable base URL, e.g. https://fleet.example.com)")
		}
	} else if u, err := url.Parse(c.Server.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		v.add("server.publicUrl", fmt.Sprintf("must be an absolute http(s) URL, got %q", c.Server.PublicURL))
	} else if !dev && u.Scheme != "https" {
		v.add("server.publicUrl", "must use https in production (session cookies are Secure)")
	}
	if (c.Server.TLS.CertFile == "") != (c.Server.TLS.KeyFile == "") {
		v.add("server.tls", "certFile and keyFile must be set together")
	}
	if !dev && c.Server.TLS.CertFile == "" && !c.Server.TLS.TerminatedUpstream {
		v.add("server.tls.terminatedUpstream", "must be set in production when myriarchd serves plain HTTP (server.tls.certFile is empty): set it only when TLS is terminated by the ingress or load balancer in front of myriarchd, otherwise configure server.tls.certFile/keyFile")
	}
	for i, p := range c.Server.TrustedProxies {
		n, err := ParseCIDR(p)
		if err != nil {
			v.add(fmt.Sprintf("server.trustedProxies[%d]", i), err.Error())
			continue
		}
		if dev {
			continue
		}
		switch {
		case isCatchAll(n):
			v.add(fmt.Sprintf("server.trustedProxies[%d]", i), fmt.Sprintf("%s covers every address: forwarded client addresses and certificate headers would be trusted from any peer; list the proxies or their network", p))
		case isVeryBroad(n) && c.OpAMP.ClientCertHeader != "":
			v.add(fmt.Sprintf("server.trustedProxies[%d]", i), fmt.Sprintf("%s is a very broad range and opamp.clientCertHeader is set: a forwarded client certificate would be accepted from a large part of the network; list the proxies or their network", p))
		}
	}
	v.checkServerFiles(c)
	v.positive("server.shutdownTimeout", c.Server.ShutdownTimeout)

	// ----------------------------------------------------------- opamp ---
	v.checkEnrollmentTokens(c)
	if tok := c.Dev.SeedEnrollmentToken; tok != "" {
		if msg := domain.EnrollmentTokenProblem(tok); msg != "" {
			v.add("dev.seedEnrollmentToken", msg)
		}
	}
	if !strings.HasPrefix(c.OpAMP.Path, "/") {
		v.add("opamp.path", "must start with \"/\"")
	}
	if strings.HasPrefix(c.OpAMP.Path, "/api/") || c.OpAMP.Path == "/" {
		v.add("opamp.path", "must not collide with the REST API or the UI root")
	}
	v.positive("opamp.offlineAfter", c.OpAMP.OfflineAfter)
	v.positive("opamp.lastSeenFlushInterval", c.OpAMP.LastSeenFlushInterval)
	v.positive("opamp.sendTimeout", c.OpAMP.SendTimeout)
	if c.OpAMP.OfflineAfter > 0 && c.OpAMP.LastSeenFlushInterval > 0 && c.OpAMP.OfflineAfter < 2*c.OpAMP.LastSeenFlushInterval {
		v.add("opamp.offlineAfter", "must be at least twice opamp.lastSeenFlushInterval (agents would flap)")
	}
	if c.OpAMP.MaxMessageSize <= 0 {
		v.add("opamp.maxMessageSize", "must be positive")
	}
	if c.OpAMP.MaxEffectiveConfigBytes <= 0 {
		v.add("opamp.maxEffectiveConfigBytes", "must be positive")
	}
	if c.OpAMP.ConnectRatePerIP < 0 {
		v.add("opamp.connectRatePerIp", "must not be negative")
	}
	if c.OpAMP.MaxConnectionsPerToken < 0 {
		v.add("opamp.maxConnectionsPerToken", "must not be negative")
	}
	if c.OpAMP.MaxConnectionsPerReplica <= 0 {
		v.add("opamp.maxConnectionsPerReplica", "must be positive")
	}
	if c.OpAMP.ConnectRateLimit <= 0 {
		v.add("opamp.connectRateLimit", "must be positive")
	}
	if c.OpAMP.RequireClientCert {
		if c.Server.TLS.ClientCAFile == "" {
			v.add("server.tls.clientCaFile", "is required when opamp.requireClientCert is set")
		}
		if c.Server.TLS.CertFile == "" && c.OpAMP.ClientCertHeader == "" {
			v.add("opamp.requireClientCert", "needs server.tls.certFile (mTLS at myriarchd) or opamp.clientCertHeader (forwarded by a trusted proxy)")
		}
		if c.Server.TLS.CertFile == "" && len(c.Server.TrustedProxies) == 0 {
			v.add("server.trustedProxies", "must list the proxy when client certificates are forwarded via opamp.clientCertHeader")
		}
	}
	if !dev && !c.OpAMP.RequireEnrollmentToken && !c.OpAMP.RequireClientCert {
		v.add("opamp.requireEnrollmentToken", "must not be disabled in production unless opamp.requireClientCert is set")
	}

	if !dev && c.OpAMP.EnableCompression && !c.OpAMP.AllowUnboundedCompression {
		v.add("opamp.enableCompression", "is refused in production: decompressed WebSocket messages are not bounded by opamp.maxMessageSize (set opamp.allowUnboundedCompression=true to accept that)")
	}

	// -------------------------------------------------------- database ---
	v.checkDatabase(c.Database, dev)

	// ------------------------------------------------------------ auth ---
	v.positive("auth.sessionTtl", c.Auth.SessionTTL)
	if c.Auth.SessionTTL > 0 && c.Auth.SessionTTL < time.Minute {
		v.add("auth.sessionTtl", "must be at least 1m")
	}
	if c.Auth.APITokenMaxTTL < 0 {
		v.add("auth.apiTokenMaxTtl", "must not be negative (0 means unlimited)")
	}
	v.positive("auth.sessionMaxAge", c.Auth.SessionMaxAge)
	if c.Auth.SessionMaxAge > 0 && c.Auth.SessionTTL > c.Auth.SessionMaxAge {
		v.add("auth.sessionMaxAge", "must not be shorter than auth.sessionTtl")
	}
	if !dev {
		if c.Auth.DevLogin.Enabled {
			v.add("auth.devLogin.enabled", "must not be enabled in production")
		}
		if !c.Auth.CookieSecure {
			v.add("auth.cookieSecure", "must be true in production")
		}
	}
	v.downloadSecretFile(c.Auth.DownloadTokenSecretFile)
	o := c.Auth.OIDC
	if o.Enabled {
		if o.IssuerURL == "" {
			v.add("auth.oidc.issuerUrl", "is required when OIDC is enabled")
		} else if u, err := url.Parse(o.IssuerURL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			v.add("auth.oidc.issuerUrl", "must be an absolute http(s) URL")
		} else if !dev && u.Scheme != "https" {
			v.add("auth.oidc.issuerUrl", "must use https in production")
		} else if msg := checkIssuerHint(u); msg != "" {
			v.add("auth.oidc.issuerUrl", msg)
		}
		if o.ClientID == "" {
			v.add("auth.oidc.clientId", "is required when OIDC is enabled")
		}
		v.checkOIDCClientAuth(o)
		if c.Server.PublicURL == "" {
			v.add("server.publicUrl", "is required for the OIDC redirect URI")
		}
		if len(o.Scopes) == 0 {
			v.add("auth.oidc.scopes", "must contain at least \"openid\"")
		} else if !contains(o.Scopes, "openid") {
			v.add("auth.oidc.scopes", "must contain \"openid\"")
		}
		if o.GroupsClaim == "" {
			v.add("auth.oidc.groupsClaim", "must not be empty")
		}
		if o.NameClaim == "" {
			v.add("auth.oidc.nameClaim", "must not be empty")
		}
		if o.EmailClaim == "" {
			v.add("auth.oidc.emailClaim", "must not be empty")
		}
		if o.DefaultRole != "" && !roleValid(o.DefaultRole) {
			v.add("auth.oidc.defaultRole", fmt.Sprintf("unknown role %q (viewer, operator, admin or empty to deny)", o.DefaultRole))
		}
		rm := o.RoleMapping
		ru := rm.Users
		if o.DefaultRole == "" && len(rm.Admin)+len(rm.Operator)+len(rm.Viewer)+len(ru.Admin)+len(ru.Operator)+len(ru.Viewer) == 0 {
			v.add("auth.oidc.roleMapping", "is empty and no defaultRole is set: nobody could log in")
		}
		for _, r := range []struct {
			role    string
			entries []string
		}{{"admin", ru.Admin}, {"operator", ru.Operator}, {"viewer", ru.Viewer}} {
			for i, e := range r.entries {
				path := fmt.Sprintf("auth.oidc.roleMapping.users.%s[%d]", r.role, i)
				switch {
				case !validUserMapping(e):
					v.add(path, `must be "sub:<subject>" or "email:<address>"`)
				case strings.HasPrefix(strings.TrimSpace(e), "email:") && o.EmailClaim != "" && o.EmailClaim != "email":
					// email_verified attests the standard claim only
					v.add(path, `"email:" entries need auth.oidc.emailClaim "email" (the verification flag email_verified covers only that claim); use "sub:<subject>" instead`)
				}
			}
		}
	}
	v.checkBootstrapAdmin(c.Auth)
	if !dev && !o.Enabled && !c.Auth.BootstrapAdmin.Enabled {
		v.add("auth", fmt.Sprintf("no login path is configured for serve mode: enable auth.oidc (single sign-on) or auth.bootstrapAdmin with passwordHashFile or tokenFile (break-glass administrator); the dev login exists only in %q", brand.ServerBinary+" dev"))
	}
	if c.Auth.DevLogin.Enabled {
		seen := map[string]bool{}
		if len(c.Auth.DevLogin.Users) == 0 {
			v.add("auth.devLogin.users", "must list at least one user when devLogin is enabled")
		}
		for i, u := range c.Auth.DevLogin.Users {
			p := fmt.Sprintf("auth.devLogin.users[%d]", i)
			if u.Username == "" {
				v.add(p+".username", "must not be empty")
			}
			if seen[u.Username] {
				v.add(p+".username", fmt.Sprintf("duplicate user %q", u.Username))
			}
			seen[u.Username] = true
			if u.Password == "" {
				v.add(p+".password", "must not be empty")
			}
			if !roleValid(u.Role) {
				v.add(p+".role", fmt.Sprintf("unknown role %q", u.Role))
			}
		}
	}

	// ---------------------------------------------------------- gitops ---
	if c.GitOps.Enabled {
		if c.GitOps.RepoURL == "" {
			v.add("gitops.repoUrl", "is required when gitops.enabled is true")
		} else if !dev {
			if msg := checkProductionGitURL(c.GitOps.RepoURL); msg != "" {
				v.add("gitops.repoUrl", msg)
			}
		}
		v.merge("gitops", gitops.CheckConfig(c.GitOps, v.skipFiles))
		v.positive("gitops.pollInterval", c.GitOps.PollInterval)
		if c.GitOps.MaxRepoBytes <= 0 || c.GitOps.MaxFiles <= 0 || c.GitOps.MaxYAMLBytes <= 0 {
			v.add("gitops.maxRepoBytes", "gitops.maxRepoBytes, gitops.maxFiles and gitops.maxYamlBytes must be positive")
		}
	}

	// -------------------------------------------------------- releases ---
	v.merge("releases", release.CheckConfig(c.Releases))
	if c.Releases.Enabled {
		v.fileNotEmpty("releases.tokenFile", c.Releases.TokenFile)
		v.positive("releases.pollInterval", c.Releases.PollInterval)
		if len(c.Releases.Distributions) == 0 {
			v.add("releases.distributions", "must list at least one distribution")
		}
		if !dev && c.Releases.FixtureDir == "" {
			v.requireHTTPS("releases.githubApiUrl", c.Releases.GitHubAPIURL)
			v.requireHTTPS("releases.rawBaseUrl", c.Releases.RawBaseURL)
		}
	}

	// ------------------------------------------------------- artifacts ---
	if !dev {
		v.requireHTTPS("artifacts.sourceBaseUrl", c.Artifacts.SourceBaseURL)
		if (!c.Artifacts.Verify.RequireChecksum || !c.Artifacts.Verify.RequireSignature) && !c.Artifacts.AllowUnverified {
			v.add("artifacts.verify", "requireChecksum and requireSignature must stay enabled in production (artifacts.allowUnverified=true permits lab setups)")
		}
	} else if c.Artifacts.SourceBaseURL != "" {
		if u, err := url.Parse(c.Artifacts.SourceBaseURL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			v.add("artifacts.sourceBaseUrl", "must be an absolute http(s) URL")
		}
	}
	if c.Artifacts.ProxyURL != "" {
		if u, err := url.Parse(c.Artifacts.ProxyURL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			v.add("artifacts.proxyUrl", "must be an absolute http(s) URL (the proxy only tunnels TLS connections)")
		}
	}
	if c.Artifacts.DownloadTokenTTL < 0 {
		v.add("artifacts.downloadTokenTtl", "must not be negative")
	} else if c.Artifacts.DownloadTokenTTL == 0 && !dev {
		v.add("artifacts.downloadTokenTtl", "must be positive in production (download tokens expire)")
	}
	for i, p := range c.Artifacts.PrefetchPlatforms {
		if err := artifact.CheckPlatform(p); err != nil {
			v.add(fmt.Sprintf("artifacts.prefetchPlatforms[%d]", i), strings.TrimPrefix(err.Error(), domain.ErrInvalidArgument.Error()+": "))
		}
	}
	for i, raw := range c.Artifacts.AgentBaseURLs {
		path := fmt.Sprintf("artifacts.agentBaseUrls[%d]", i)
		u, err := artifact.CheckAgentBaseURL(raw)
		if err != nil {
			v.add(path, err.Error())
			continue
		}
		if !dev && u.Scheme != "https" && !c.Artifacts.AllowInsecureAgentBaseURLs {
			v.add(path, fmt.Sprintf("%q must use https in production (artifacts.allowInsecureAgentBaseUrls permits a cluster-internal http:// service)", raw))
		}
	}
	v.checkSourceAuth(c.Artifacts)
	v.checkVerify(c.Artifacts.Verify, dev)

	// -------------------------------------------------------- outbound ---
	for i, cidr := range c.Outbound.AllowedCIDRs {
		// the parser of the policy itself: what it refuses, serve would refuse at startup
		if _, err := netguard.ParsePrefix(cidr); err != nil {
			v.add(fmt.Sprintf("outbound.allowedCidrs[%d]", i), err.Error())
		}
	}
	if c.Outbound.CAFile != "" {
		v.pemCertificates("outbound.caFile", c.Outbound.CAFile)
	}

	// ------------------------------------------------ executors/rollouts ---
	v.positive("executors.defaultTimeout", c.Executors.DefaultTimeout)
	v.positive("executors.maxTimeout", c.Executors.MaxTimeout)
	// An empty executors.allowedWebhookHosts is valid: it refuses every webhook
	// and AWX target (fail closed) at the time of the call.
	if c.Executors.SecretDir != "" && !filepath.IsAbs(c.Executors.SecretDir) {
		v.add("executors.secretDir", "must be an absolute path")
	}
	for name, sec := range c.Executors.Secrets {
		p := "executors.secrets." + name
		if (sec.Env == "") == (sec.File == "") {
			v.add(p, "exactly one of env and file must be set")
		}
		if len(sec.AllowedHosts) == 0 {
			v.add(p+".allowedHosts", "must list the hosts this secret may be sent to")
		}
	}
	prefix := c.Executors.SecretEnvPrefix
	if prefix == "" {
		prefix = config.Default().Executors.SecretEnvPrefix
	}
	if msg := secretEnvPrefixCollision(prefix); msg != "" {
		v.add("executors.secretEnvPrefix", msg)
	}
	for name, hosts := range c.Executors.SecretEnvHosts {
		p := "executors.secretEnvHosts." + name
		if !strings.HasPrefix(name, prefix) {
			v.add(p, fmt.Sprintf("the variable name must start with executors.secretEnvPrefix (%s)", prefix))
		}
		if len(hosts) == 0 {
			v.add(p, "must list the hosts this secret may be sent to")
		}
	}
	for name, hosts := range c.Executors.SecretFileHosts {
		if len(hosts) == 0 {
			v.add("executors.secretFileHosts."+name, "must list the hosts this secret may be sent to")
		}
	}
	v.positive("rollouts.tickInterval", c.Rollouts.TickInterval)
	v.positive("rollouts.executorTimeout", c.Rollouts.ExecutorTimeout)
	if c.Rollouts.DefaultTimezone != "" {
		if _, err := time.LoadLocation(c.Rollouts.DefaultTimezone); err != nil {
			v.add("rollouts.defaultTimezone", fmt.Sprintf("unknown time zone %q", c.Rollouts.DefaultTimezone))
		}
	}

	// -------------------------------------------------------------- ui ---
	v.validateUI(c.UI)

	// ---------------------------------------------------- notifications ---
	names := map[string]bool{}
	for i, ch := range c.Notifications.Channels {
		before := len(v.issues)
		p := fmt.Sprintf("notifications.channels[%d]", i)
		if ch.Name == "" {
			v.add(p+".name", "must not be empty")
		}
		if names[ch.Name] {
			v.add(p+".name", fmt.Sprintf("duplicate channel %q", ch.Name))
		}
		names[ch.Name] = true
		switch ch.Type {
		case "webhook", "teams", "slack":
		default:
			v.add(p+".type", fmt.Sprintf("unknown type %q (webhook, teams, slack)", ch.Type))
		}
		if ch.URL == "" && ch.URLFile == "" {
			v.add(p+".url", "url or urlFile is required")
		}
		if !dev && ch.URL != "" {
			v.requireHTTPS(p+".url", ch.URL)
		}
		if !dev && ch.Type == "webhook" && ch.SecretFile == "" {
			v.add(p+".secretFile", "is required for type webhook in production (payloads are signed with HMAC-SHA256)")
		}
		switch ch.MinSeverity {
		case "", "info", "warning", "critical":
		default:
			v.add(p+".minSeverity", fmt.Sprintf("unknown severity %q (info, warning, critical)", ch.MinSeverity))
		}
		// everything else the dispatcher refuses (events, secret file, ...) once
		// the checks above found nothing
		if len(v.issues) == before {
			chk := ch
			if chk.URLFile != "" && chk.URL == "" {
				chk.URL = "https://url-file.invalid/" // the loader reports a url file it cannot read
			}
			chk.URLFile = ""
			if err := channels.CheckChannel(chk, !dev, v.skipFiles); err != nil {
				v.add(p, err.Error())
			}
		}
	}

	// ------------------------------------------------------- retention ---
	for _, r := range []struct {
		name string
		d    time.Duration
	}{
		{"retention.agentEvents", c.Retention.AgentEvents}, {"retention.audit", c.Retention.Audit},
		{"retention.offlineAgents", c.Retention.OfflineAgents}, {"retention.finishedRollouts", c.Retention.FinishedRollouts},
		{"retention.unreferencedConfig", c.Retention.UnreferencedConfig}, {"retention.gitopsSyncHistory", c.Retention.GitOpsSyncHistory},
	} {
		if r.d < 0 {
			v.add(r.name, "must not be negative")
		}
	}

	// ------------------------------------------------------- telemetry ---
	switch strings.ToLower(c.Telemetry.LogLevel) {
	case "debug", "info", "warn", "warning", "error":
	default:
		v.add("telemetry.logLevel", fmt.Sprintf("unknown level %q (debug, info, warn, error)", c.Telemetry.LogLevel))
	}
	switch c.Telemetry.LogFormat {
	case "json", "text":
	default:
		v.add("telemetry.logFormat", fmt.Sprintf("unknown format %q (json, text)", c.Telemetry.LogFormat))
	}

	// -------------------------------------------------------------- ha ---
	v.positive("ha.heartbeatInterval", c.HA.HeartbeatInterval)
	if c.HA.HeartbeatInterval > 10*time.Second {
		v.add("ha.heartbeatInterval", "must not exceed 10s (replicas are considered dead after 30s without heartbeat)")
	}

	if len(v.issues) == 0 {
		return nil
	}
	return domain.NewValidationError(v.issues...)
}

// ParseCIDR parses a CIDR or a single IP (as /32 or /128).
func ParseCIDR(s string) (*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q", s)
		}
		return n, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("invalid IP or CIDR %q", s)
	}
	if ip4 := ip.To4(); ip4 != nil {
		return &net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)}, nil
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}, nil
}

// languageTagRE accepts the BCP 47 shapes the UI understands: a two or three
// letter primary language and optional subtags ("de", "pt-BR", "zh-Hant-TW").
var languageTagRE = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

func (v *validator) validateUI(ui config.UIConfig) {
	if ui.DefaultLanguage != "" && !languageTagRE.MatchString(ui.DefaultLanguage) {
		v.add("ui.defaultLanguage", fmt.Sprintf("%q is not a valid language tag (e.g. en, de, pt-BR)", ui.DefaultLanguage))
	}
	seen := map[string]bool{}
	contains := false
	for i, l := range ui.Languages {
		path := fmt.Sprintf("ui.languages[%d]", i)
		if !languageTagRE.MatchString(l) {
			v.add(path, fmt.Sprintf("%q is not a valid language tag (e.g. en, de, pt-BR)", l))
			continue
		}
		if seen[l] {
			v.add(path, fmt.Sprintf("duplicate language %q", l))
		}
		seen[l] = true
		if l == ui.DefaultLanguage {
			contains = true
		}
	}
	if len(ui.Languages) > 0 && ui.DefaultLanguage != "" && !contains && languageTagRE.MatchString(ui.DefaultLanguage) {
		v.add("ui.defaultLanguage", fmt.Sprintf("%q must be one of ui.languages", ui.DefaultLanguage))
	}
}

type validator struct {
	issues    domain.Issues
	skipFiles bool
}

// merge adds the issues of a component check: a *domain.ValidationError keeps
// its own paths, any other error is reported under fallbackPath.
func (v *validator) merge(fallbackPath string, err error) {
	if err == nil {
		return
	}
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		v.issues = append(v.issues, ve.Issues...)
		return
	}
	v.add(fallbackPath, err.Error())
}

func (v *validator) add(path, msg string) {
	v.issues = append(v.issues, domain.Issue{
		Severity: domain.SeverityError, Code: "invalid_config", Path: path,
		Message: path + ": " + msg,
	})
}

// requireHTTPS adds an issue unless raw is an absolute https URL.
func (v *validator) requireHTTPS(path, raw string) {
	if raw == "" {
		return
	}
	if u, err := url.Parse(raw); err != nil || u.Host == "" || u.Scheme != "https" {
		v.add(path, "must be an absolute https URL in production")
	}
}

// checkProductionGitURL returns the reason a Git source is not acceptable in
// production ("" = fine): only https://, ssh:// and scp-like git@host:path
// without inline credentials.
func checkProductionGitURL(raw string) string {
	switch {
	case strings.HasPrefix(raw, "dir:"):
		return "dir: sources are for development only (use https:// or ssh://)"
	case strings.HasPrefix(raw, "file:"), strings.HasPrefix(raw, "/"), strings.HasPrefix(raw, "."):
		return "local repositories are not allowed in production (use https:// or ssh://)"
	case strings.HasPrefix(raw, "http://"), strings.HasPrefix(raw, "git://"):
		return "must use https:// or ssh:// (credentials and repository content must not travel in clear text)"
	}
	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "ssh://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "is not a valid URL"
		}
		if _, hasPw := u.User.Password(); hasPw {
			return "must not contain inline credentials (use gitops.auth.tokenFile)"
		}
		if u.Scheme == "https" && u.User != nil {
			return "must not contain inline credentials (use gitops.auth.tokenFile)"
		}
		return ""
	}
	// scp-like: [user@]host:path
	if i := strings.Index(raw, ":"); i > 0 && !strings.Contains(raw[:i], "/") {
		if strings.Contains(raw[:i], "@") && strings.Contains(strings.SplitN(raw[:i], "@", 2)[0], ":") {
			return "must not contain inline credentials"
		}
		return ""
	}
	return "must be an https://, ssh:// or git@host:path URL"
}

func (v *validator) positive(path string, d time.Duration) {
	if d <= 0 {
		v.add(path, "must be a positive duration")
	}
}

// isCatchAll reports whether n covers every address of its family.
func isCatchAll(n *net.IPNet) bool {
	ones, _ := n.Mask.Size()
	return ones == 0
}

// isVeryBroad reports whether n is shorter than /8 (IPv4) or /32 (IPv6).
func isVeryBroad(n *net.IPNet) bool {
	ones, bits := n.Mask.Size()
	if bits == 32 {
		return ones < 8
	}
	return ones < 32
}

func contains(vs []string, s string) bool {
	for _, v := range vs {
		if v == s {
			return true
		}
	}
	return false
}

// Warnings returns findings that do not prevent startup but that an operator
// should see in the log (settings that weaken a control). myriarchd logs them at
// warn level when it starts; "myriarchd config validate" prints them as WARNING.
func Warnings(c *config.Config, dev bool) []string {
	var w []string
	if dev {
		return w
	}
	if c.Auth.OIDC.Enabled && c.Auth.OIDC.DefaultRole != "" {
		w = append(w, fmt.Sprintf("auth.oidc.defaultRole is %q: every user of the identity provider without a mapped group gets this role; leave it empty (deny) and map groups explicitly", c.Auth.OIDC.DefaultRole))
	}
	if c.Auth.OIDC.Enabled {
		w = append(w, providerWarnings(c.Auth.OIDC)...)
	}
	if c.Auth.APITokenMaxTTL == 0 {
		w = append(w, "auth.apiTokenMaxTtl is 0: API tokens can live forever and keep their role when a user is removed at the identity provider; set a maximum lifetime (default 90 days)")
	}
	if c.Auth.BootstrapActive() {
		if c.Auth.OIDC.Enabled {
			w = append(w, "auth.bootstrapAdmin.keepWithOidc keeps the break-glass bootstrap admin active next to OIDC: every sign-in is audited as auth.bootstrap.login; monitor these events and keep the credential in a vault")
		} else {
			w = append(w, "auth.bootstrapAdmin is the only login path: configure single sign-on (auth.oidc) and disable the bootstrap admin after the initial setup")
		}
	}
	now := time.Now()
	for i, t := range c.OpAMP.EnrollmentTokens {
		if exp, err := time.Parse(time.RFC3339, t.ExpiresAt); err == nil && !exp.After(now) {
			w = append(w, fmt.Sprintf("opamp.enrollmentTokens[%d] (%s) expired at %s: agents cannot enroll with it", i, t.Name, t.ExpiresAt))
		}
	}
	if c.OpAMP.EnableCompression && c.OpAMP.AllowUnboundedCompression {
		w = append(w, "opamp.enableCompression is on (acknowledged by opamp.allowUnboundedCompression): the WebSocket library limits the compressed frame only, so decompressed messages are not bounded by opamp.maxMessageSize")
	}
	for i, p := range c.Server.TrustedProxies {
		if n, err := ParseCIDR(p); err == nil && isVeryBroad(n) {
			w = append(w, fmt.Sprintf("server.trustedProxies[%d] (%s) is a very broad range: forwarded client addresses are trusted from every peer in it; list the proxies or their network", i, p))
		}
	}
	if c.Telemetry.Metrics && c.Server.MetricsListenAddr == "" {
		w = append(w, "server.metricsListenAddr is empty: the unauthenticated /metrics endpoint is served on the main listener and is reachable through the ingress; set a separate listener or block /metrics at the ingress")
	}
	if w2 := databaseTransportWarning(c.Database.DSN); w2 != "" {
		w = append(w, w2)
	}
	if c.Artifacts.Dir == "" {
		w = append(w, "artifacts.dir is empty: the artifact cache uses a fixed directory in the temporary directory; set a directory that only the server account can write")
	}
	if c.GitOps.Enabled && c.GitOps.CloneDir == "" && !strings.HasPrefix(c.GitOps.RepoURL, "dir:") {
		w = append(w, "gitops.cloneDir is empty: the Git clone is kept in a directory in the temporary directory; set a directory that only the server account can write")
	}
	if u, err := url.Parse(c.Artifacts.ProxyURL); err == nil && u.User != nil {
		w = append(w, "artifacts.proxyUrl contains credentials: they are stored in the configuration; prefer a proxy that authenticates by network location")
	}
	if len(c.Server.TrustedProxies) == 0 {
		w = append(w, "server.trustedProxies is empty: behind an ingress all agents and users share the address of the proxy (per-IP rate limits, audit source addresses)")
	}
	if c.Artifacts.AllowUnverified && (!c.Artifacts.Verify.RequireChecksum || !c.Artifacts.Verify.RequireSignature) {
		w = append(w, "artifact verification is disabled (artifacts.allowUnverified): downloaded collector packages are not checked against checksums or sigstore signatures")
	}
	for _, raw := range c.Artifacts.AgentBaseURLs {
		if strings.HasPrefix(raw, "http://") {
			w = append(w, fmt.Sprintf("artifacts.agentBaseUrls contains the plain http:// URL %s (artifacts.allowInsecureAgentBaseUrls): updaters download the collector package over an unencrypted connection; keep it to a cluster-internal service (the package is verified by SHA-256 and sigstore on the updater)", raw))
		}
	}
	if c.Artifacts.SourceAuth.Configured() {
		if strings.TrimRight(c.Artifacts.SourceBaseURL, "/") == strings.TrimRight(config.Default().Artifacts.SourceBaseURL, "/") {
			w = append(w, "artifacts.sourceAuth is set but artifacts.sourceBaseUrl is the public GitHub download URL: the mirror credentials would be sent to github.com")
		}
	}
	if c.Artifacts.Verify.TrustedRootFile != "" && c.Artifacts.Verify.TUFRepositoryURL != "" {
		w = append(w, "artifacts.verify.tufRepositoryUrl is ignored because artifacts.verify.trustedRootFile is set")
	}
	return w
}

// secretEnvPrefixCollision returns the reason why prefix is unsafe as
// executors.secretEnvPrefix, or "" if it is fine. GitOps executors may read
// every environment variable that starts with the prefix, so it must neither
// cover the configuration override namespace (a prefix of brand.EnvPrefix,
// for example "MYR" or `MYRIARCH_`) nor any single override name (for example
// `MYRIARCH_DATABASE_`): that would let Git read server secrets such as the
// database DSN.
func secretEnvPrefixCollision(prefix string) string {
	suggest := "use a prefix such as " + brand.ExecSecretEnvPrefix
	if strings.HasPrefix(brand.EnvPrefix, prefix) {
		return fmt.Sprintf("%q is a prefix of the configuration override prefix %s; %s", prefix, brand.EnvPrefix, suggest)
	}
	var hit string
	walkLeaves(reflect.ValueOf(&config.Config{}).Elem(), nil, func(path []string, _ reflect.Value) {
		if name := envName(path); hit == "" && strings.HasPrefix(name, prefix) {
			hit = name
		}
	})
	if hit != "" {
		return fmt.Sprintf("%q covers the configuration override %s; %s", prefix, hit, suggest)
	}
	return ""
}

// databaseTransportWarning reports a database connection to a remote host that does not verify the
// server certificate. The message names the setting, never the connection string.
func databaseTransportWarning(dsn string) string {
	if strings.TrimSpace(dsn) == "" {
		return ""
	}
	pc, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return ""
	}
	hosts := []string{pc.Host}
	for _, f := range pc.Fallbacks {
		hosts = append(hosts, f.Host)
	}
	remote := false
	for _, h := range hosts {
		if h != "" && !strings.HasPrefix(h, "/") && !isLocalName(h) {
			remote = true
		}
	}
	if !remote {
		return ""
	}
	mode := dsnSSLMode(dsn)
	if mode == "verify-ca" || mode == "verify-full" {
		return ""
	}
	return fmt.Sprintf("database.dsn: the connection to a remote database uses sslmode=%s, which does not verify the server certificate; use sslmode=verify-full (or verify-ca) with the CA in sslrootcert", mode)
}

func isLocalName(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// dsnSSLMode returns the sslmode of a PostgreSQL connection string ("prefer" when absent, the libpq default).
func dsnSSLMode(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		if u, err := url.Parse(dsn); err == nil {
			if m := u.Query().Get("sslmode"); m != "" {
				return m
			}
		}
		return "prefer"
	}
	for _, f := range strings.Fields(dsn) {
		if m, ok := strings.CutPrefix(f, "sslmode="); ok {
			return strings.Trim(m, "'")
		}
	}
	return "prefer"
}
