// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package version

import "testing"

func TestUserAgent(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })

	Version = "1.2.3"
	if got, want := UserAgent("myriarchd"), "myriarchd/1.2.3"; got != want {
		t.Errorf("UserAgent = %q, want %q", got, want)
	}
	Version = "0.0.0-dev"
	if got, want := UserAgent("myriarch-sim"), "myriarch-sim/0.0.0-dev"; got != want {
		t.Errorf("UserAgent = %q, want %q", got, want)
	}
}
