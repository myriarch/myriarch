// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/install"
)

// checkInstall validates the settings of the installation of hosts from the
// server. A setting of a turned off feature is not checked.
func (v *validator) checkInstall(c *config.Config, dev bool) {
	in := c.Install
	if !in.Enabled {
		return
	}
	switch {
	case in.GrantTTL < 0:
		v.add("install.grantTtl", "must not be negative")
	case !dev && in.GrantTTL == 0:
		v.add("install.grantTtl", "must be positive in production (install grants expire)")
	case !dev && c.Artifacts.DownloadTokenTTL > 0 && in.GrantTTL > c.Artifacts.DownloadTokenTTL:
		v.add("install.grantTtl", fmt.Sprintf("must not exceed artifacts.downloadTokenTtl (%s): an install grant is a download credential like the download token of an updater", c.Artifacts.DownloadTokenTTL))
	}
	if raw := in.BundleSourceBaseURL; raw != "" {
		u, err := url.Parse(raw)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https"):
			v.add("install.bundleSourceBaseUrl", "must be an absolute http(s) URL")
		case !dev && u.Scheme != "https":
			v.add("install.bundleSourceBaseUrl", "must be an absolute https URL in production")
		case u.User != nil:
			v.add("install.bundleSourceBaseUrl", "must not contain credentials")
		case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#"):
			v.add("install.bundleSourceBaseUrl", "must not contain a query or a fragment: the server appends /v<version>/<asset>")
		}
	}
	if len(in.Platforms) == 0 {
		v.add("install.platforms", "must list at least one platform ("+strings.Join(install.SupportedPlatforms, ", ")+") or install.enabled must be false")
	}
	for i, p := range in.Platforms {
		if err := install.CheckPlatform(p); err != nil {
			v.add(fmt.Sprintf("install.platforms[%d]", i), err.Error())
		}
	}
}
