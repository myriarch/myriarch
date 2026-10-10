// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"path/filepath"
	"testing"
)

// releases.tokenFile is checked only while the release tracker is enabled.
func TestReleasesTokenFileOnlyWhileEnabled(t *testing.T) {
	c := prodConfig()
	c.Releases.TokenFile = filepath.Join(t.TempDir(), "missing-token")
	opts := Options{CheckFiles: true}

	c.Releases.Enabled = true
	if err := ValidateOptions(&c, opts); err == nil || !issuePaths(err)["releases.tokenFile"] {
		t.Errorf("an unreadable releases.tokenFile must be reported while releases are enabled: %v", err)
	}
	c.Releases.Enabled = false
	if err := ValidateOptions(&c, opts); err != nil {
		t.Errorf("releases.tokenFile must not be read while releases are disabled: %v", err)
	}
}
