// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package version exposes build information injected via -ldflags:
//
//	go build -ldflags "-X github.com/myriarch/myriarch/internal/version.Version=1.2.3 \
//	  -X github.com/myriarch/myriarch/internal/version.Commit=$(git rev-parse --short HEAD) \
//	  -X github.com/myriarch/myriarch/internal/version.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
package version

import "runtime"

// Build information (overridden by -ldflags).
var (
	Version   = "0.0.0-dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// Info is the build information.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Get returns the build information.
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// UserAgent returns the User-Agent value "<binary>/<version>" that outbound HTTP
// clients of a binary send, for example UserAgent(brand.ServerBinary).
func UserAgent(binary string) string {
	return binary + "/" + Version
}

// String returns "<version> (<commit>, <date>)".
func (i Info) String() string {
	return i.Version + " (" + i.Commit + ", " + i.BuildDate + ", " + i.GoVersion + ", " + i.Platform + ")"
}
