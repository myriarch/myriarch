// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"regexp/syntax"
	"runtime"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/brand"
)

// Default certificate identity (upstream collector release workflow, plain
// vX.Y.Z tags) and issuer, see docs/design/upstream-notes.md,
// Release assets and signatures > Signatures.
const (
	DefaultCertificateIdentityRegexp = `^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$`
	DefaultCertificateOIDCIssuer     = "https://token.actions.githubusercontent.com"

	// ServiceName is the name of the updater's own OS service.
	ServiceName = brand.UpdaterServiceName
	// ServiceDisplayName is the Windows display name.
	ServiceDisplayName = brand.UpdaterDisplayName
	// AgentServiceName is the OpAMP service.name of the updater; myriarchd
	// classifies agents with this name as updaters.
	AgentServiceName = brand.UpdaterServiceName
	// AttrCollectorInstanceUID links the updater to the supervised collector.
	AttrCollectorInstanceUID = brand.AttrCollectorInstanceUID
)

// Duration is a time.Duration that unmarshals from YAML strings like "120s".
type Duration time.Duration

// D returns the value as time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	if strings.TrimSpace(s) == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Config is the updater configuration (updater.yaml).
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Supervisor SupervisorConfig `yaml:"supervisor"`
	Collector  CollectorConfig  `yaml:"collector"`
	Update     UpdateConfig     `yaml:"update"`
	Container  ContainerConfig  `yaml:"container"`
	StateDir   string           `yaml:"stateDir"`
	Log        LogConfig        `yaml:"log"`

	// Path is the file the configuration was loaded from (not part of the file; empty for ParseConfig).
	Path string `yaml:"-"`
}

// ContainerConfig configures "myriarch-updater container": the updater runs as
// PID 1 of a container, supervises the OpAMP supervisor as a child process
// and swaps the collector binary on a persistent volume. Only validated for
// that command (ValidateContainer).
type ContainerConfig struct {
	// SupervisorCommand is the supervisor command line (required).
	SupervisorCommand []string `yaml:"supervisorCommand"`
	// SeedBinary is the collector binary baked into the image (required).
	SeedBinary string `yaml:"seedBinary"`
	// SeedPolicy decides between the image binary and the active binary on
	// the volume: "newest" (default: the higher version wins, a tie keeps the
	// volume) or "image" (always activate the image binary: emergency switch).
	SeedPolicy string `yaml:"seedPolicy"`
	// StopTimeout is how long the child gets after SIGTERM before SIGKILL.
	StopTimeout Duration `yaml:"stopTimeout"`
	// RestartDelay is the first delay before an unexpectedly ended child is
	// restarted (doubles up to 60s).
	RestartDelay Duration `yaml:"restartDelay"`
	// MaxRestarts unexpected restarts within RestartWindow end the container
	// (exit 1, so that the kubelet restarts it and the crash loop is visible).
	MaxRestarts   int      `yaml:"maxRestarts"`
	RestartWindow Duration `yaml:"restartWindow"`
	// HealthListen serves /healthz and /readyz ("" = off).
	HealthListen string `yaml:"healthListen"`
}

// Seed policies.
const (
	SeedPolicyNewest = "newest"
	SeedPolicyImage  = "image"
)

// ServerConfig describes the myriarchd OpAMP endpoint.
type ServerConfig struct {
	Endpoint  string `yaml:"endpoint"`
	Token     string `yaml:"token"`
	TokenFile string `yaml:"tokenFile"`
	CAFile    string `yaml:"caFile"`
	// AllowInsecureTransport permits ws:// and http:// endpoints to a
	// non-loopback host (the bearer token would travel in clear text).
	AllowInsecureTransport bool `yaml:"allowInsecureTransport"`
}

// SupervisorConfig describes the supervisor next to the updater.
type SupervisorConfig struct {
	// StorageDir is the supervisor storage.directory (persistent_state.yaml,
	// effective.yaml).
	StorageDir string `yaml:"storageDir"`
	// ServiceName is the systemd unit / Windows service of the supervisor.
	ServiceName string `yaml:"serviceName"`
	// HealthURL is the supervisor healthcheck endpoint (optional).
	HealthURL string `yaml:"healthUrl"`
}

// CollectorConfig describes the collector installation layout.
type CollectorConfig struct {
	InstallDir   string `yaml:"installDir"`
	BinaryName   string `yaml:"binaryName"`
	HealthURL    string `yaml:"healthUrl"`
	KeepVersions int    `yaml:"keepVersions"`
	// Activation is "symlink" (Linux: <installDir>/current -> versions/<v>) or
	// "copy" (Windows: copy to <installDir>/bin). Default depends on the OS.
	Activation string `yaml:"activation"`
}

// UpdateConfig controls verification, installation and health checking.
type UpdateConfig struct {
	AllowedURLPrefixes        []string `yaml:"allowedUrlPrefixes"`
	RequireSignature          bool     `yaml:"requireSignature"`
	TrustedRootFile           string   `yaml:"trustedRootFile"`
	CertificateIdentityRegexp string   `yaml:"certificateIdentityRegexp"`
	CertificateOIDCIssuer     string   `yaml:"certificateOidcIssuer"`
	HealthTimeout             Duration `yaml:"healthTimeout"`
	PreflightValidate         bool     `yaml:"preflightValidate"`
	// MinimumVersion is a host-side floor: offers below it are refused
	// (never installed, whatever the server says). Empty = no floor.
	MinimumVersion string `yaml:"minimumVersion"`
	// AllowDowngrade permits installing versions older than the active one.
	// Default false: only the one-step rollback to the previous version is allowed.
	AllowDowngrade bool `yaml:"allowDowngrade"`

	// Tunables (defaults are production values; tests shorten them).
	HealthInterval      Duration `yaml:"healthInterval"`      // 5s
	HealthSuccesses     int      `yaml:"healthSuccesses"`     // 3
	ServiceTimeout      Duration `yaml:"serviceTimeout"`      // 60s stop/start wait
	PreflightTimeout    Duration `yaml:"preflightTimeout"`    // 60s
	FailedRetryBackoff  Duration `yaml:"failedRetryBackoff"`  // 10m
	MaxDownloadBytes    int64    `yaml:"maxDownloadBytes"`    // 1 GiB
	MaxBinaryBytes      int64    `yaml:"maxBinaryBytes"`      // 1 GiB
	DownloadIdleTimeout Duration `yaml:"downloadIdleTimeout"` // 60s
}

// LogConfig configures logging.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"` // json | text
	// File is an optional log file (size-rotated); empty = stderr only.
	File string `yaml:"file"`
}

// DefaultConfig returns the defaults for the current OS.
func DefaultConfig() Config { return defaultConfigFor(runtime.GOOS) }

func defaultConfigFor(goos string) Config {
	c := Config{
		Supervisor: SupervisorConfig{
			StorageDir:  "/var/lib/otelcol/supervisor",
			ServiceName: "otelcol-supervisor",
			HealthURL:   "http://localhost:13134/health",
		},
		Collector: CollectorConfig{
			InstallDir:   "/opt/myriarch/collector",
			BinaryName:   "otelcol-contrib",
			HealthURL:    "http://localhost:13133/",
			KeepVersions: 2,
			Activation:   "symlink",
		},
		Update: UpdateConfig{
			RequireSignature:          true,
			CertificateIdentityRegexp: DefaultCertificateIdentityRegexp,
			CertificateOIDCIssuer:     DefaultCertificateOIDCIssuer,
			HealthTimeout:             Duration(120 * time.Second),
			PreflightValidate:         true,
			HealthInterval:            Duration(5 * time.Second),
			HealthSuccesses:           3,
			ServiceTimeout:            Duration(60 * time.Second),
			PreflightTimeout:          Duration(60 * time.Second),
			FailedRetryBackoff:        Duration(10 * time.Minute),
			MaxDownloadBytes:          1 << 30,
			MaxBinaryBytes:            1 << 30,
			DownloadIdleTimeout:       Duration(60 * time.Second),
		},
		Container: ContainerConfig{
			SeedPolicy:    SeedPolicyNewest,
			StopTimeout:   Duration(30 * time.Second),
			RestartDelay:  Duration(2 * time.Second),
			MaxRestarts:   5,
			RestartWindow: Duration(10 * time.Minute),
			HealthListen:  "0.0.0.0:13135",
		},
		StateDir: "/var/lib/myriarch-updater",
		Log:      LogConfig{Level: "info", Format: "json"},
	}
	if goos == "windows" {
		c.Supervisor.StorageDir = `C:\ProgramData\Myriarch\supervisor`
		c.Collector.InstallDir = `C:\Program Files\Myriarch\collector`
		c.Collector.BinaryName = "otelcol-contrib.exe"
		c.Collector.Activation = "copy"
		c.StateDir = `C:\ProgramData\Myriarch\updater`
	}
	return c
}

// LoadConfig reads, expands and validates a configuration file on top of the
// OS defaults.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		return Config{}, err
	}
	cfg.Path = path
	return cfg, nil
}

// ParseConfig parses YAML (strict: unknown keys are errors), expands
// ${env:VAR} / ${env:VAR:-default} in all string values and validates.
func ParseConfig(data []byte) (Config, error) {
	cfg := DefaultConfig()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := expandEnv(reflect.ValueOf(&cfg).Elem(), os.LookupEnv); err != nil {
		return Config{}, fmt.Errorf("expand config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

var envRef = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// expandString replaces ${env:VAR} and ${env:VAR:-default}. An unset variable
// without default is an error (fail early instead of sending an empty token).
func expandString(s string, lookup func(string) (string, bool)) (string, error) {
	var firstErr error
	out := envRef.ReplaceAllStringFunc(s, func(m string) string {
		sub := envRef.FindStringSubmatch(m)
		if v, ok := lookup(sub[1]); ok {
			return v
		}
		if strings.Contains(m, ":-") {
			return sub[2]
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("environment variable %s is not set", sub[1])
		}
		return ""
	})
	return out, firstErr
}

func expandEnv(v reflect.Value, lookup func(string) (string, bool)) error {
	switch v.Kind() {
	case reflect.String:
		if !v.CanSet() {
			return nil
		}
		s, err := expandString(v.String(), lookup)
		if err != nil {
			return err
		}
		v.SetString(s)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := expandEnv(v.Field(i), lookup); err != nil {
				return err
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if err := expandEnv(v.Index(i), lookup); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validate checks the configuration and fills derived defaults.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	u, err := url.Parse(c.Server.Endpoint)
	switch {
	case c.Server.Endpoint == "":
		add("server.endpoint is required")
	case err != nil || u.Host == "" || !isOneOf(u.Scheme, "ws", "wss", "http", "https"):
		add("server.endpoint %q must be a ws/wss/http/https URL", c.Server.Endpoint)
	}
	if err == nil && u != nil && isOneOf(u.Scheme, "ws", "http") && !c.Server.AllowInsecureTransport && !isLoopbackHost(u.Hostname()) {
		add("server.endpoint %q uses plain ws/http: use wss/https (or set server.allowInsecureTransport for a lab setup)", c.Server.Endpoint)
	}
	if c.Update.MinimumVersion != "" {
		if _, err := semver.StrictNewVersion(strings.TrimPrefix(c.Update.MinimumVersion, "v")); err != nil {
			add("update.minimumVersion %q is not a valid version", c.Update.MinimumVersion)
		}
	}
	if c.Server.Token != "" && c.Server.TokenFile != "" {
		add("server.token and server.tokenFile are mutually exclusive")
	}
	if c.StateDir == "" {
		add("stateDir is required")
	}
	if c.Supervisor.ServiceName == "" {
		add("supervisor.serviceName is required")
	}
	if c.Supervisor.StorageDir == "" {
		add("supervisor.storageDir is required")
	}
	if c.Collector.InstallDir == "" {
		add("collector.installDir is required")
	}
	if c.Collector.BinaryName == "" || c.Collector.BinaryName != filepath.Base(c.Collector.BinaryName) ||
		strings.ContainsAny(c.Collector.BinaryName, `/\`) || c.Collector.BinaryName == "." || c.Collector.BinaryName == ".." {
		add("collector.binaryName %q must be a plain file name", c.Collector.BinaryName)
	}
	if c.Collector.KeepVersions < 2 {
		c.Collector.KeepVersions = 2 // active + previous are needed for rollback
	}
	switch c.Collector.Activation {
	case "symlink", "copy":
	default:
		add("collector.activation must be symlink or copy, got %q", c.Collector.Activation)
	}
	if c.Update.RequireSignature || c.Update.CertificateIdentityRegexp != "" {
		if _, err := regexp.Compile(c.Update.CertificateIdentityRegexp); err != nil {
			add("update.certificateIdentityRegexp: %v", err)
		} else if c.Update.CertificateIdentityRegexp != "" && !anchoredRegexp(c.Update.CertificateIdentityRegexp) {
			add("update.certificateIdentityRegexp must be anchored: start with ^ and end with $ (an unanchored pattern also accepts identities that merely contain the expected text)")
		}
	}
	if c.Update.RequireSignature && c.Update.CertificateIdentityRegexp == "" {
		add("update.certificateIdentityRegexp is required when update.requireSignature is true")
	}
	if c.Update.HealthTimeout <= 0 {
		add("update.healthTimeout must be > 0")
	}
	if c.Update.HealthInterval <= 0 {
		add("update.healthInterval must be > 0")
	}
	if c.Update.HealthSuccesses < 1 {
		add("update.healthSuccesses must be >= 1")
	}
	if c.Update.MaxDownloadBytes <= 0 || c.Update.MaxBinaryBytes <= 0 {
		add("update.maxDownloadBytes and update.maxBinaryBytes must be > 0")
	}
	if len(c.Update.AllowedURLPrefixes) == 0 && u != nil && u.Host != "" {
		// Safe default: artifacts are served by the same myriarchd that we talk OpAMP with.
		scheme := "https"
		if u.Scheme == "ws" || u.Scheme == "http" {
			scheme = "http"
		}
		c.Update.AllowedURLPrefixes = []string{scheme + "://" + u.Host + "/api/v1/artifacts/"}
	}
	for _, p := range c.Update.AllowedURLPrefixes {
		pu, err := url.Parse(p)
		if err != nil || pu.Host == "" || !isOneOf(pu.Scheme, "http", "https") || pu.User != nil {
			add("update.allowedUrlPrefixes entry %q must be an absolute http(s) URL without credentials", p)
		}
	}
	return errors.Join(errs...)
}

// ValidateContainer checks the container section (only needed by the
// "container" command) and the settings container mode depends on.
func (c *Config) ValidateContainer() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	ct := &c.Container
	if len(ct.SupervisorCommand) == 0 || strings.TrimSpace(ct.SupervisorCommand[0]) == "" {
		add("container.supervisorCommand is required")
	}
	if strings.TrimSpace(ct.SeedBinary) == "" {
		add("container.seedBinary is required")
	}
	switch ct.SeedPolicy {
	case SeedPolicyNewest, SeedPolicyImage:
	default:
		add("container.seedPolicy must be %s or %s, got %q", SeedPolicyNewest, SeedPolicyImage, ct.SeedPolicy)
	}
	if ct.StopTimeout <= 0 {
		add("container.stopTimeout must be > 0")
	}
	if ct.RestartDelay <= 0 {
		add("container.restartDelay must be > 0")
	}
	if ct.MaxRestarts < 1 {
		add("container.maxRestarts must be >= 1")
	}
	if ct.RestartWindow <= 0 {
		add("container.restartWindow must be > 0")
	}
	if ct.HealthListen != "" {
		if _, _, err := net.SplitHostPort(ct.HealthListen); err != nil {
			add("container.healthListen %q must be host:port: %v", ct.HealthListen, err)
		}
	}
	if c.Collector.Activation != "symlink" {
		add("collector.activation must be symlink in container mode, got %q", c.Collector.Activation)
	}
	return errors.Join(errs...)
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func isOneOf(s string, set ...string) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
}

// SupervisorStatePath is <supervisor.storageDir>/persistent_state.yaml.
func (c *Config) SupervisorStatePath() string {
	return filepath.Join(c.Supervisor.StorageDir, "persistent_state.yaml")
}

// EffectiveConfigPath is <supervisor.storageDir>/effective.yaml.
func (c *Config) EffectiveConfigPath() string {
	return filepath.Join(c.Supervisor.StorageDir, "effective.yaml")
}

// anchoredRegexp reports whether the pattern matches only whole strings: its top level is a
// concatenation that begins with ^ and ends with $ (so "^a|b$" and an escaped final "\$" do not count).
func anchoredRegexp(pattern string) bool {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil || re.Op != syntax.OpConcat || len(re.Sub) < 2 {
		return false
	}
	return re.Sub[0].Op == syntax.OpBeginText && re.Sub[len(re.Sub)-1].Op == syntax.OpEndText
}
