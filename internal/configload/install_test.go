// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/config"
)

func TestInstallDefaults(t *testing.T) {
	in := config.Default().Install
	if !in.Enabled || in.GrantTTL != time.Hour {
		t.Errorf("install.enabled and install.grantTtl: %+v", in)
	}
	if in.BundleSourceBaseURL != "https://github.com/myriarch/myriarch/releases/download" {
		t.Errorf("install.bundleSourceBaseUrl = %q", in.BundleSourceBaseURL)
	}
	if got := strings.Join(in.Platforms, ","); got != "linux/amd64,linux/arm64,windows/amd64" {
		t.Errorf("install.platforms = %s", got)
	}
	c := prodConfig()
	if err := Validate(&c, false); err != nil {
		t.Errorf("the defaults are invalid in production: %v", err)
	}
	d := config.DevDefaults()
	if err := Validate(&d, true); err != nil {
		t.Errorf("the defaults are invalid in development: %v", err)
	}
}

func TestInstallGrantTTL(t *testing.T) {
	cases := []struct {
		name    string
		ttl     time.Duration
		dlTTL   time.Duration
		dev     bool
		wantErr string
	}{
		{"default", time.Hour, 24 * time.Hour, false, ""},
		{"equal to the download token lifetime", 24 * time.Hour, 24 * time.Hour, false, ""},
		{"above the download token lifetime", 25 * time.Hour, 24 * time.Hour, false, "must not exceed artifacts.downloadTokenTtl"},
		{"zero in production", 0, 24 * time.Hour, false, "must be positive in production"},
		{"negative in production", -time.Second, 24 * time.Hour, false, "must not be negative"},
		{"zero in development means no expiry", 0, 0, true, ""},
		{"negative in development", -time.Second, 0, true, "must not be negative"},
		{"above the lifetime in development", 48 * time.Hour, 24 * time.Hour, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c config.Config
			if tc.dev {
				c = config.DevDefaults()
			} else {
				c = prodConfig()
			}
			c.Install.GrantTTL, c.Artifacts.DownloadTokenTTL = tc.ttl, tc.dlTTL
			err := Validate(&c, tc.dev)
			if tc.wantErr == "" {
				if err != nil && issuePaths(err)["install.grantTtl"] {
					t.Fatalf("install.grantTtl refused: %v", err)
				}
				return
			}
			if err == nil || !issuePaths(err)["install.grantTtl"] || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an issue for install.grantTtl containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestInstallBundleSourceBaseURL(t *testing.T) {
	cases := []struct {
		name, url string
		dev       bool
		wantErr   string
	}{
		{"default", "https://github.com/myriarch/myriarch/releases/download", false, ""},
		{"a mirror", "https://mirror.example.com/myriarch/releases", false, ""},
		{"a mirror with a trailing slash", "https://mirror.example.com/myriarch/", false, ""},
		{"empty means the default", "", false, ""},
		{"http in production", "http://mirror.example.com/myriarch", false, "https URL in production"},
		{"http in development", "http://localhost:9999/mirror", true, ""},
		{"no scheme", "mirror.example.com/myriarch", true, "absolute http(s) URL"},
		{"another scheme", "ftp://mirror.example.com/myriarch", true, "absolute http(s) URL"},
		{"no host", "https:///myriarch", true, "absolute http(s) URL"},
		{"credentials", "https://user:pw@mirror.example.com/myriarch", false, "must not contain credentials"},
		{"user name only", "https://user@mirror.example.com/myriarch", false, "must not contain credentials"},
		{"query", "https://mirror.example.com/myriarch?x=1", false, "query or a fragment"},
		{"empty query", "https://mirror.example.com/myriarch?", false, "query or a fragment"},
		{"fragment", "https://mirror.example.com/myriarch#x", false, "query or a fragment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c config.Config
			if tc.dev {
				c = config.DevDefaults()
			} else {
				c = prodConfig()
			}
			c.Install.BundleSourceBaseURL = tc.url
			err := Validate(&c, tc.dev)
			if tc.wantErr == "" {
				if err != nil && issuePaths(err)["install.bundleSourceBaseUrl"] {
					t.Fatalf("install.bundleSourceBaseUrl refused: %v", err)
				}
				return
			}
			if err == nil || !issuePaths(err)["install.bundleSourceBaseUrl"] || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an issue for install.bundleSourceBaseUrl containing %q, got %v", tc.wantErr, err)
			}
			if strings.Contains(err.Error(), "pw@") {
				t.Errorf("the message repeats the credentials: %v", err)
			}
		})
	}
}

func TestInstallPlatforms(t *testing.T) {
	cases := []struct {
		name      string
		platforms []string
		wantPath  string
	}{
		{"all", []string{"linux/amd64", "linux/arm64", "windows/amd64"}, ""},
		{"one", []string{"linux/arm64"}, ""},
		{"duplicates are harmless", []string{"linux/amd64", "linux/amd64"}, ""},
		{"none", nil, "install.platforms"},
		{"windows on arm64 has no upstream supervisor", []string{"windows/arm64"}, "install.platforms[0]"},
		{"macOS", []string{"linux/amd64", "darwin/arm64"}, "install.platforms[1]"},
		{"an alias", []string{"linux/x86_64"}, "install.platforms[0]"},
		{"upper case", []string{"Linux/amd64"}, "install.platforms[0]"},
		{"no architecture", []string{"linux"}, "install.platforms[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := prodConfig()
			c.Install.Platforms = tc.platforms
			err := Validate(&c, false)
			if tc.wantPath == "" {
				if err != nil && strings.Contains(err.Error(), "install.platforms") {
					t.Fatalf("install.platforms refused: %v", err)
				}
				return
			}
			if err == nil || !issuePaths(err)[tc.wantPath] {
				t.Fatalf("want an issue for %s, got %v", tc.wantPath, err)
			}
		})
	}
}

// A turned off feature is not checked: an operator who disables the installation does not have to repair its settings.
func TestInstallDisabledSkipsTheChecks(t *testing.T) {
	c := prodConfig()
	c.Install = config.InstallConfig{Enabled: false, GrantTTL: -1, BundleSourceBaseURL: "ftp://x", Platforms: []string{"nope"}}
	if err := Validate(&c, false); err != nil && strings.Contains(err.Error(), "install.") {
		t.Fatalf("disabled installation refused: %v", err)
	}
}

func TestInstallWarnsWithoutReleaseTracker(t *testing.T) {
	has := func(c config.Config) bool {
		for _, w := range Warnings(&c, false) {
			if strings.Contains(w, "install.enabled") {
				return true
			}
		}
		return false
	}
	c := prodConfig()
	if has(c) {
		t.Error("warning with the release tracker on")
	}
	c.Releases.Enabled = false
	if !has(c) {
		t.Error("no warning although install.enabled is on and releases.enabled is off")
	}
	c.Install.Enabled = false
	if has(c) {
		t.Error("warning although install.enabled is off")
	}
}

func TestInstallSettingsCanBeOverriddenFromTheEnvironment(t *testing.T) {
	vars := map[string]string{
		"MYRIARCH_INSTALL_ENABLED":             "false",
		"MYRIARCH_INSTALL_GRANTTTL":            "30m",
		"MYRIARCH_INSTALL_BUNDLESOURCEBASEURL": "https://mirror.example.com/myriarch",
		"MYRIARCH_INSTALL_PLATFORMS":           "linux/amd64,windows/amd64",
	}
	c := config.Default()
	err := applyEnvOverrides(&c, func(name string) (string, bool) { v, ok := vars[name]; return v, ok })
	if err != nil {
		t.Fatalf("applyEnvOverrides: %v", err)
	}
	if c.Install.Enabled || c.Install.GrantTTL != 30*time.Minute || c.Install.BundleSourceBaseURL != "https://mirror.example.com/myriarch" ||
		strings.Join(c.Install.Platforms, ",") != "linux/amd64,windows/amd64" {
		t.Errorf("overrides not applied: %+v", c.Install)
	}
}
