// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import "time"

// MiniConfig is the configuration of the golden test: a small structure with every shape that the generator handles.
type MiniConfig struct {
	Server MiniServer `yaml:"server"`
	Notify MiniNotify `yaml:"notify"`
	Dev    MiniDev    `yaml:"dev"`
}

// MiniServer configures the listener of the <mini> server (HTTP | HTTPS), see docs/operations/authentication.md and
// docs/reference/gitops-schema.md.
type MiniServer struct {
	// ListenAddr is the address of the listener, as host:port (default ":8080"). The value *must* not use a [scheme] and
	// the setting_name is escaped.
	ListenAddr string `yaml:"listenAddr"`
	// TLS configures the certificate. See `server.tls.certFile`.
	TLS MiniTLS `yaml:"tls"`
	// Timeout limits one request.
	// A second line of the comment joins the first.
	Timeout time.Duration `yaml:"timeout"`
	// Grace is a duration without trailing zero units.
	Grace time.Duration `yaml:"grace"`
	// MaxBodyBytes limits the body of a request.
	MaxBodyBytes int64 `yaml:"maxBodyBytes"`
	// CacheSize is a size that is not a multiple of 1024.
	CacheSize int64 `yaml:"cacheSize"`
	// Workers is the number of workers; the development default differs.
	Workers int `yaml:"workers"`
	// Ratio is a floating point number.
	Ratio float64 `yaml:"ratio"`
	// Hosts lists the allowed hosts as `*.example.com` or "a|b".
	Hosts []string `yaml:"hosts"`
	// Password is a secret that the loader keeps out of the output.
	Password string `yaml:"password"`
	// Labels is a map of strings.
	Labels map[string]string `yaml:"labels"`
	// Routes maps names to lists of hosts.
	Routes map[string][]string `yaml:"routes"`
	// Mode selects the mode.
	Mode   MiniMode   `yaml:"mode"`
	Backup MiniBackup `yaml:"backup"`
	// Debug is only set by tests.
	Debug bool `yaml:"-"`
	// Quiet switches the output off.
	Quiet   bool `yaml:"quiet"`
	Verbose bool `yaml:"verbose"` // Verbose adds detail to the output (documented on the line of the field).
}

// MiniBackup is the backup of the mini server; a field of this type relies on this comment.
type MiniBackup struct {
	// Enabled switches the backup on.
	Enabled bool `yaml:"enabled"`
}

// MiniMode is a named string type.
type MiniMode string

// MiniTLS is the TLS of the mini server.
type MiniTLS struct {
	// CertFile is the certificate file.
	CertFile string `yaml:"certFile"`
	// Secrets are named secrets with their allowed hosts.
	Secrets map[string]MiniSecret `yaml:"secrets"`
}

// MiniSecret is one named secret.
type MiniSecret struct {
	// Env is the variable that holds the secret.
	Env string `yaml:"env"`
	// AllowedHosts lists the destinations.
	AllowedHosts []string `yaml:"allowedHosts"`
}

// MiniNotify configures the notifications.
type MiniNotify struct {
	// Channels lists the channels.
	Channels []MiniChannel `yaml:"channels"`
}

// MiniChannel is one channel.
type MiniChannel struct {
	// Name identifies the channel.
	Name string `yaml:"name"`
	// URL is the target (a secret).
	URL string `yaml:"url"`
}

// MiniDev configures the development mode.
type MiniDev struct {
	// DataDir is the data directory.
	DataDir string `yaml:"dataDir"`
}

func miniDefaults() MiniConfig {
	return MiniConfig{
		Server: MiniServer{
			ListenAddr: ":8080", Timeout: time.Minute, Grace: 2 * time.Hour, MaxBodyBytes: 4 << 20, CacheSize: 1500,
			Workers: 4, Ratio: 0.5, Hosts: []string{"a.example.com", "b.example.com"}, Mode: "fast",
		},
		Dev: MiniDev{DataDir: ".dev"},
	}
}

func miniDevDefaults() MiniConfig {
	c := miniDefaults()
	c.Server.Workers = 1
	c.Server.ListenAddr = ""
	c.Server.Quiet = true
	c.Server.Hosts = nil
	return c
}
