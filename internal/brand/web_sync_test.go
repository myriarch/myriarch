// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package brand

import (
	"os"
	"regexp"
	"testing"
)

// TestWebBrandInSync keeps web/src/brand.ts equal to this package for every
// value both sides define. The TypeScript file holds one
// `export const X = 'value';` line per constant.
func TestWebBrandInSync(t *testing.T) {
	src, err := os.ReadFile("../../web/src/brand.ts")
	if err != nil {
		t.Fatalf("read web/src/brand.ts: %v", err)
	}
	re := regexp.MustCompile(`(?m)^export const ([A-Z0-9_]+) = '([^']*)';\s*$`)
	web := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		web[m[1]] = m[2]
	}

	shared := []struct{ ts, want string }{
		{"NAME", Name},
		{"DISPLAY_NAME", DisplayName},
		{"WEBSITE", Website},
		{"SERVER_BINARY", ServerBinary},
		{"CLI_BINARY", CLIBinary},
		{"CLI_ALIAS", CLIAlias},
		{"UPDATER_BINARY", UpdaterBinary},
		{"CSRF_COOKIE", CSRFCookie},
		{"ENROLLMENT_TOKEN_ENV", EnrollmentTokenEnv},
		{"API_TOKEN_PREFIX", APITokenPrefix},
		{"ENROLLMENT_TOKEN_PREFIX", EnrollmentTokenPrefix},
		{"ATTR_COLLECTOR_INSTANCE_UID", AttrCollectorInstanceUID},
	}
	for _, c := range shared {
		got, ok := web[c.ts]
		if !ok {
			t.Errorf("web/src/brand.ts does not define %s", c.ts)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %q in brand.ts, want %q", c.ts, got, c.want)
		}
	}
}
