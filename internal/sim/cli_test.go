// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/version"
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

func TestRunWritesTheVersionToStdoutAndErrorsToStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("version: exit code %d, stderr %q", code, stderr.String())
	}
	if want := version.Get().String() + "\n"; stdout.String() != want {
		t.Errorf("version: stdout is %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("version: stderr is %q, want empty", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"nosuch"}, &stdout, &stderr); code != 1 {
		t.Errorf("unknown command: exit code %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("unknown command: stdout is %q, want empty", stdout.String())
	}
	if !strings.HasPrefix(stderr.String(), brand.SimBinary+": ") {
		t.Errorf("unknown command: stderr is %q, want the prefix %q", stderr.String(), brand.SimBinary+": ")
	}
}
