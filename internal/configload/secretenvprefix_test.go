// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"reflect"
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
)

// TestSecretEnvPrefixCollisionGuard: executors may read every variable under
// executors.secretEnvPrefix, so the prefix must not reach the configuration
// override names (MYRIARCH_<PATH>).
func TestSecretEnvPrefixCollisionGuard(t *testing.T) {
	cases := []struct {
		prefix string
		ok     bool
	}{
		{"", true}, // empty means the default
		{brand.ExecSecretEnvPrefix, true},
		{"ACME_EXEC_", true},
		{"MYRIARCH_", false},
		{"MYR", false},
		{"M", false},
		{"MYRIARCH_DATABASE_", false},
		{"MYRIARCH_AUTH_", false},
	}
	for _, tc := range cases {
		t.Run(tc.prefix, func(t *testing.T) {
			c := prodConfig()
			c.Executors.SecretEnvPrefix = tc.prefix
			err := Validate(&c, false)
			if tc.ok {
				if err != nil {
					t.Fatalf("prefix %q must be accepted: %v", tc.prefix, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("prefix %q must be rejected", tc.prefix)
			}
			msg := err.Error()
			if !strings.Contains(msg, "executors.secretEnvPrefix") || !strings.Contains(msg, brand.ExecSecretEnvPrefix) {
				t.Errorf("the message must name the field and suggest %s: %s", brand.ExecSecretEnvPrefix, msg)
			}
		})
	}
}

// TestOverrideNamesDoNotCollideWithReservedEnvironment: the executor secret
// namespace and the agent-side variables are never configuration overrides.
func TestOverrideNamesDoNotCollideWithReservedEnvironment(t *testing.T) {
	n := 0
	walkLeaves(reflect.ValueOf(&config.Config{}).Elem(), nil, func(path []string, _ reflect.Value) {
		n++
		name := envName(path)
		if !strings.HasPrefix(name, brand.EnvPrefix) {
			t.Errorf("override %s does not start with %s", name, brand.EnvPrefix)
		}
		if strings.HasPrefix(name, brand.ExecSecretEnvPrefix) {
			t.Errorf("override %s lies in the executor secret namespace %s", name, brand.ExecSecretEnvPrefix)
		}
		if name == brand.EnrollmentTokenEnv || name == brand.OpAMPEndpointEnv {
			t.Errorf("override %s collides with a reserved agent-side variable", name)
		}
	})
	if n < 50 {
		t.Fatalf("walked only %d configuration leaves", n)
	}
}
