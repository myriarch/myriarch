// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package myriarch

import (
	"strings"
	"testing"
)

func TestLicenseIsApache2(t *testing.T) {
	got := License()
	for _, want := range []string{"Apache License", "Version 2.0, January 2004"} {
		if !strings.Contains(got, want) {
			t.Errorf("License() does not contain %q", want)
		}
	}
}

func TestThirdPartyNoticesHeading(t *testing.T) {
	const want = "Third-party software notices and licenses for myriarch\n"
	if got := ThirdPartyNotices(); !strings.HasPrefix(got, want) {
		t.Errorf("ThirdPartyNotices() does not start with %q", want)
	}
}
