// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
)

// enrollmentToken returns a well-formed random enrollment token.
func enrollmentToken(t *testing.T) string {
	t.Helper()
	return brand.EnrollmentTokenPrefix + randomHex(t, 24)
}

func tokensConfig(entries ...config.EnrollmentTokenConfig) config.Config {
	c := prodConfig()
	c.OpAMP.EnrollmentTokens = entries
	return c
}

func TestEnrollmentTokenSettings(t *testing.T) {
	const domainLabelsRule = `at most 20 labels; keys 1 to 63 characters without whitespace or "=", values at most 253 characters`
	file := writeFile(t, "tok", "x")
	cases := []struct {
		name  string
		entry config.EnrollmentTokenConfig
		path  string
		msg   string
	}{
		{"upper case name", config.EnrollmentTokenConfig{Name: "Edge", TokenFile: file}, "opamp.enrollmentTokens[0].name", `must be a DNS label: lower case letters, digits and "-", 1 to 63 characters`},
		{"empty name", config.EnrollmentTokenConfig{TokenFile: file}, "opamp.enrollmentTokens[0].name", ""},
		{"leading dash", config.EnrollmentTokenConfig{Name: "-edge", TokenFile: file}, "opamp.enrollmentTokens[0].name", ""},
		{"trailing dash", config.EnrollmentTokenConfig{Name: "edge-", TokenFile: file}, "opamp.enrollmentTokens[0].name", ""},
		{"underscore", config.EnrollmentTokenConfig{Name: "edge_1", TokenFile: file}, "opamp.enrollmentTokens[0].name", ""},
		{"long name", config.EnrollmentTokenConfig{Name: strings.Repeat("a", 64), TokenFile: file}, "opamp.enrollmentTokens[0].name", ""},
		{"missing token file", config.EnrollmentTokenConfig{Name: "edge"}, "opamp.enrollmentTokens[0].tokenFile", "is required"},
		{"relative token file", config.EnrollmentTokenConfig{Name: "edge", TokenFile: "tokens/edge"}, "opamp.enrollmentTokens[0].tokenFile", "must be an absolute path"},
		{"too many labels", config.EnrollmentTokenConfig{Name: "edge", TokenFile: file, Labels: manyLabels(21)}, "opamp.enrollmentTokens[0].labels", domainLabelsRule},
		{"label key with a space", config.EnrollmentTokenConfig{Name: "edge", TokenFile: file, Labels: map[string]string{"a b": "x"}}, "opamp.enrollmentTokens[0].labels", domainLabelsRule},
		{"label key with =", config.EnrollmentTokenConfig{Name: "edge", TokenFile: file, Labels: map[string]string{"a=b": "x"}}, "opamp.enrollmentTokens[0].labels", domainLabelsRule},
		{"empty label key", config.EnrollmentTokenConfig{Name: "edge", TokenFile: file, Labels: map[string]string{"": "x"}}, "opamp.enrollmentTokens[0].labels", domainLabelsRule},
		{"long label key", config.EnrollmentTokenConfig{Name: "edge", TokenFile: file, Labels: map[string]string{strings.Repeat("k", 64): "x"}}, "opamp.enrollmentTokens[0].labels", domainLabelsRule},
		{"long label value", config.EnrollmentTokenConfig{Name: "edge", TokenFile: file, Labels: map[string]string{"k": strings.Repeat("v", 254)}}, "opamp.enrollmentTokens[0].labels", domainLabelsRule},
		{"expiry without time zone", config.EnrollmentTokenConfig{Name: "edge", TokenFile: file, ExpiresAt: "2027-01-01T00:00:00"}, "opamp.enrollmentTokens[0].expiresAt", "must be an RFC 3339 timestamp such as 2027-01-01T00:00:00Z"},
		{"expiry as a date", config.EnrollmentTokenConfig{Name: "edge", TokenFile: file, ExpiresAt: "2027-01-01"}, "opamp.enrollmentTokens[0].expiresAt", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tokensConfig(tc.entry)
			err := Validate(&c, false)
			if err == nil {
				t.Fatal("expected an error")
			}
			got, ok := problemMap(err)[tc.path]
			if !ok || (tc.msg != "" && got != tc.msg) {
				t.Errorf("%s: %q (%v)", tc.path, got, Problems(err))
			}
		})
	}

	good := tokensConfig(
		config.EnrollmentTokenConfig{Name: "edge-nl", TokenFile: file, Labels: map[string]string{"channel": "edge", "site": "nl-01"}, ExpiresAt: "2099-01-01T00:00:00Z"},
		config.EnrollmentTokenConfig{Name: "a", TokenFile: file, ExpiresAt: "2099-01-01T01:00:00+01:00"},
		config.EnrollmentTokenConfig{Name: strings.Repeat("a", 63), TokenFile: file},
		config.EnrollmentTokenConfig{Name: "0-9", TokenFile: file, Labels: manyLabels(20)},
	)
	if err := Validate(&good, false); err != nil {
		t.Errorf("valid entries: %v", err)
	}
	// the same rules hold in dev mode
	dev := config.DevDefaults()
	dev.OpAMP.EnrollmentTokens = []config.EnrollmentTokenConfig{{Name: "Bad"}}
	if err := Validate(&dev, true); err == nil {
		t.Error("dev mode checks the entries too")
	}
}

func manyLabels(n int) map[string]string {
	m := map[string]string{}
	for i := 0; i < n; i++ {
		m["k"+strings.Repeat("x", i)] = "v"
	}
	return m
}

func TestEnrollmentTokenDuplicateNames(t *testing.T) {
	file := writeFile(t, "tok", "x")
	c := tokensConfig(
		config.EnrollmentTokenConfig{Name: "edge", TokenFile: file},
		config.EnrollmentTokenConfig{Name: "core", TokenFile: file},
		config.EnrollmentTokenConfig{Name: "edge", TokenFile: file},
	)
	err := Validate(&c, false)
	if got := problemMap(err)["opamp.enrollmentTokens[2].name"]; got != `duplicate name "edge"` {
		t.Errorf("%q (%v)", got, Problems(err))
	}
}

func TestEnrollmentTokenFileChecks(t *testing.T) {
	check := func(entries ...config.EnrollmentTokenConfig) map[string]string {
		c := tokensConfig(entries...)
		err := ValidateOptions(&c, Options{CheckFiles: true})
		if err == nil {
			return nil
		}
		return problemMap(err)
	}
	const formatMsg = `must contain an enrollment token: myre_ followed by 32 to 120 letters, digits, "_" or "-"`

	t.Run("valid", func(t *testing.T) {
		a := writeFile(t, "a", enrollmentToken(t)+"\n")
		b := writeFile(t, "b", "  "+enrollmentToken(t)+"  \n")
		if pm := check(config.EnrollmentTokenConfig{Name: "a", TokenFile: a}, config.EnrollmentTokenConfig{Name: "b", TokenFile: b}); pm != nil {
			t.Errorf("%v", pm)
		}
	})
	t.Run("serve does not read the files", func(t *testing.T) {
		c := tokensConfig(config.EnrollmentTokenConfig{Name: "a", TokenFile: "/does/not/exist"})
		if err := ValidateOptions(&c, Options{}); err != nil {
			t.Errorf("%v", err)
		}
		if err := ValidateOptions(&c, Options{SkipFiles: true, CheckFiles: true}); err != nil {
			t.Errorf("--skip-files: %v", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		pm := check(config.EnrollmentTokenConfig{Name: "a", TokenFile: "/does/not/exist/token"})
		if got := pm["opamp.enrollmentTokens[0].tokenFile"]; !strings.HasPrefix(got, "cannot read /does/not/exist/token: ") {
			t.Errorf("%q", got)
		}
	})
	t.Run("wrong format", func(t *testing.T) {
		for name, content := range map[string]string{
			"empty":        "\n",
			"other prefix": "myra_" + randomHex(t, 24),
			"too short":    "myre_" + strings.Repeat("a", 31),
			"too long":     "myre_" + strings.Repeat("a", 121),
			"bad char":     "myre_" + strings.Repeat("a", 20) + "." + strings.Repeat("b", 20),
			"two lines":    enrollmentToken(t) + "\n" + enrollmentToken(t),
		} {
			f := writeFile(t, "tok", content)
			pm := check(config.EnrollmentTokenConfig{Name: "a", TokenFile: f})
			if got := pm["opamp.enrollmentTokens[0].tokenFile"]; got != "file "+f+" "+formatMsg {
				t.Errorf("%s: %q", name, got)
			}
		}
		for _, n := range []int{32, 120} {
			f := writeFile(t, "tok", "myre_"+strings.Repeat("Ab_-9", 40)[:n])
			if pm := check(config.EnrollmentTokenConfig{Name: "a", TokenFile: f}); pm != nil {
				t.Errorf("%d characters: %v", n, pm)
			}
		}
	})
	t.Run("development token", func(t *testing.T) {
		f := writeFile(t, "tok", config.Default().Dev.SeedEnrollmentToken+"\n")
		pm := check(config.EnrollmentTokenConfig{Name: "a", TokenFile: f})
		if got := pm["opamp.enrollmentTokens[0].tokenFile"]; got != "file "+f+" contains the well-known development enrollment token" {
			t.Errorf("%q", got)
		}
	})
	t.Run("same value twice", func(t *testing.T) {
		tok := enrollmentToken(t)
		a, b, c := writeFile(t, "a", tok), writeFile(t, "b", enrollmentToken(t)), writeFile(t, "c", tok+"\n")
		pm := check(config.EnrollmentTokenConfig{Name: "a", TokenFile: a}, config.EnrollmentTokenConfig{Name: "b", TokenFile: b}, config.EnrollmentTokenConfig{Name: "c", TokenFile: c})
		if got := pm["opamp.enrollmentTokens[2].tokenFile"]; got != "file "+c+" contains the same token as opamp.enrollmentTokens[0]" {
			t.Errorf("%q (%v)", got, pm)
		}
		if len(pm) != 1 {
			t.Errorf("only the repeated entry is reported: %v", pm)
		}
	})
	t.Run("messages never contain the token", func(t *testing.T) {
		tok := "myre_" + strings.Repeat("z", 10)
		f := writeFile(t, "tok", tok)
		for _, m := range check(config.EnrollmentTokenConfig{Name: "a", TokenFile: f}) {
			if strings.Contains(m, tok) {
				t.Errorf("the message leaks the token: %q", m)
			}
		}
	})
}

func TestWarningTokenExpired(t *testing.T) {
	file := writeFile(t, "tok", "x")
	past := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	c := tokensConfig(
		config.EnrollmentTokenConfig{Name: "old", TokenFile: file, ExpiresAt: past},
		config.EnrollmentTokenConfig{Name: "new", TokenFile: file, ExpiresAt: future},
		config.EnrollmentTokenConfig{Name: "forever", TokenFile: file},
	)
	c.Server.TrustedProxies = []string{"10.0.0.0/8"}
	ws := Warnings(&c, false)
	want := "opamp.enrollmentTokens[0] (old) expired at " + past + ": agents cannot enroll with it"
	if len(ws) != 1 || ws[0] != want {
		t.Errorf("warnings: %q, want [%q]", ws, want)
	}
	if w := Warnings(&c, true); len(w) != 0 {
		t.Errorf("dev: %v", w)
	}
}

// dev.seedEnrollmentToken must have the documented token form, or it would be seeded but never accepted.
func TestDevSeedEnrollmentTokenMustHaveTheDocumentedForm(t *testing.T) {
	for _, tok := range []string{brand.EnrollmentTokenPrefix + "short", brand.EnrollmentTokenPrefix + strings.Repeat("a", 31) + "!", "no-prefix"} {
		for _, dev := range []bool{true, false} {
			c := prodConfig()
			c.Dev.SeedEnrollmentToken = tok
			err := Validate(&c, dev)
			if err == nil {
				t.Fatalf("%q (dev=%v): expected an error", tok, dev)
			}
			if msg, ok := problemMap(err)["dev.seedEnrollmentToken"]; !ok || strings.Contains(msg, tok) {
				t.Fatalf("%q (dev=%v): problem missing or names the value: %q", tok, dev, msg)
			}
		}
	}
	c := prodConfig()
	c.Dev.SeedEnrollmentToken = enrollmentToken(t)
	if err := Validate(&c, false); err != nil {
		t.Fatalf("well-formed token rejected: %v", err)
	}
}
