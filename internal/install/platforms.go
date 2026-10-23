// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package install implements the installation of hosts from the server
// (ADR-0046, docs/design/opamp-server.md, "Installation from the server"): the
// plan that a host asks for with its enrollment token, and the grammar in which
// the plan is delivered. The server provides data; the host verifies code.
package install

import (
	"fmt"
	"strings"

	"github.com/myriarch/myriarch/internal/artifact"
)

// Platform is an operating system and an architecture, as they appear in the
// names of release assets ("linux", "amd64").
type Platform struct{ OS, Arch string }

// String is the form "os/arch" of the setting install.platforms.
func (p Platform) String() string { return p.OS + "/" + p.Arch }

// SupportedPlatforms are the platforms that a host can be installed on from
// the server: the platforms for which the release provides the updater archive
// with the installer scripts and for which the upstream project publishes the
// supervisor (it does not for Windows on arm64).
var SupportedPlatforms = []string{"linux/amd64", "linux/arm64", "windows/amd64"}

// CheckPlatform validates one entry of install.platforms.
func CheckPlatform(p string) error {
	for _, s := range SupportedPlatforms {
		if p == s {
			return nil
		}
	}
	return fmt.Errorf("%q is not supported (%s)", p, strings.Join(SupportedPlatforms, ", "))
}

// ParsePlatforms parses the entries of install.platforms. Duplicates are
// dropped, the order is kept.
func ParsePlatforms(list []string) ([]Platform, error) {
	var out []Platform
	seen := map[Platform]bool{}
	for _, p := range list {
		if err := CheckPlatform(p); err != nil {
			return nil, err
		}
		os, arch, _ := strings.Cut(p, "/")
		pl := Platform{OS: os, Arch: arch}
		if !seen[pl] {
			seen[pl] = true
			out = append(out, pl)
		}
	}
	return out, nil
}

// NormalizePlatform maps the operating system and architecture that a host
// reports to the names of the release assets: lower case, with the aliases of
// uname and the semantic conventions mapped (x86_64 to amd64, aarch64 to arm64).
func NormalizePlatform(os, arch string) Platform {
	return Platform{OS: artifact.NormalizeOS(os), Arch: artifact.NormalizeArch(arch)}
}
