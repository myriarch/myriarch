// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"testing"

	"github.com/myriarch/myriarch/internal/brand"
)

func TestResolveToken(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		name string
		flag string
		env  map[string]string
		want string
	}{
		{"flag wins", "from-flag", map[string]string{brand.EnrollmentTokenEnv: "from-env"}, "from-flag"},
		{"environment when the flag is empty", "", map[string]string{brand.EnrollmentTokenEnv: "from-env"}, "from-env"},
		{"none", "", nil, ""},
	}
	for _, c := range cases {
		if got := resolveToken(c.flag, env(c.env)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
