// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"regexp"
	"strings"

	"github.com/myriarch/myriarch/internal/domain"
)

var (
	tagContribRE    = regexp.MustCompile(`^v(0\.\d+\.\d+)$`)
	tagSupervisorRE = regexp.MustCompile(`^cmd/opampsupervisor/v(0\.\d+\.\d+)$`)
	// <name>_<version>_<os>_<arch>[.ext]
	assetRE = regexp.MustCompile(`^(otelcol-contrib|opampsupervisor)_(\d+\.\d+\.\d+)_([a-z0-9]+)_([a-z0-9]+)(\.[a-z0-9.]+)?$`)
)

// classifyTag maps a release tag to (distribution, version).
func classifyTag(tag string) (dist, version string, ok bool) {
	if m := tagContribRE.FindStringSubmatch(tag); m != nil {
		return domain.DistributionContrib, m[1], true
	}
	if m := tagSupervisorRE.FindStringSubmatch(tag); m != nil {
		return domain.DistributionSupervisor, m[1], true
	}
	return "", "", false
}

// companionSuffixes mark files that belong to a primary asset.
var companionSuffixes = []string{".sha256", ".sigstore.json", ".sbom.json", ".sig", ".pem", ".txt"}

// parseAsset converts a GitHub asset of the given distribution into a
// ReleaseAsset. Companion files and assets of other distributions are
// skipped (ok == false).
func parseAsset(dist, name string) (domain.ReleaseAsset, bool) {
	for _, suf := range companionSuffixes {
		if strings.HasSuffix(name, suf) {
			return domain.ReleaseAsset{}, false
		}
	}
	m := assetRE.FindStringSubmatch(name)
	if m == nil || m[1] != dist {
		return domain.ReleaseAsset{}, false
	}
	a := domain.ReleaseAsset{Name: name, OS: m[3], Arch: normalizeArch(m[4])}
	switch ext := strings.TrimPrefix(m[5], "."); ext {
	case "":
		a.Format = "binary"
	case "exe":
		a.Format = "binary"
	case "tar.gz", "zip", "msi", "deb", "rpm", "apk":
		a.Format = ext
	default:
		return domain.ReleaseAsset{}, false
	}
	return a, true
}

// normalizeArch maps MSI names (x64, x86) to Go architecture names.
func normalizeArch(a string) string {
	switch a {
	case "x64":
		return "amd64"
	case "x86":
		return "386"
	}
	return a
}
