// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readDoc(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// section returns the text of the "## <heading>" section of a Markdown document (up to the next H2).
func section(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, "\n## "+heading+"\n")
	if i < 0 {
		t.Fatalf("section %q not found", heading)
	}
	rest := doc[i+1:]
	if j := strings.Index(rest[3:], "\n## "); j >= 0 {
		rest = rest[:j+3]
	}
	return rest
}

// The limits of the sign-in and the bearer-token failure tracker live in memory of each replica: the security model says so.
func TestSecurityModelStatesThePerReplicaScopeOfInMemoryLimits(t *testing.T) {
	model := readDoc(t, "docs/security/model.md")
	users := section(t, model, "Users and automation")
	for _, row := range []string{
		"| The bootstrap admin is one configuration-managed administrator",
		"| Sign-in attempts per client address and user name for the development login",
		"| The OIDC endpoints are public.",
		"| Failed bearer-token lookups are counted per client address",
	} {
		line := ""
		for _, l := range strings.Split(users, "\n") {
			if strings.HasPrefix(l, row) {
				line = l
			}
		}
		if line == "" {
			t.Errorf("row %q not found", row)
			continue
		}
		if !strings.Contains(line, "per replica") || !strings.Contains(line, "authentication.md#27-rate-limit-and-backoff") {
			t.Errorf("row %q lacks the per-replica statement or the link to Authentication, section 2.7", row)
		}
	}
}

// Development mode lists every control it weakens.
func TestSecurityModelDevelopmentModeNamesEveryWeakenedControl(t *testing.T) {
	dev := section(t, readDoc(t, "docs/security/model.md"), "Development mode")
	for _, want := range []string{
		"requireEnrollmentToken", "cookieSecure", "devLogin", "metricsListenAddr", "outbound address check", "--allow-nonlocal",
	} {
		if !strings.Contains(dev, want) {
			t.Errorf("the Development mode section does not mention %q", want)
		}
	}
}

// The executor documentation names the timestamp window and what the signature covers.
func TestExecutorsDocNamesTheReplayWindowAndSignatureScope(t *testing.T) {
	doc := readDoc(t, "docs/concepts/executors.md")
	for _, want := range []string{"300 seconds", "X-Myriarch-Delivery", "not signed", "HTTP method"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/concepts/executors.md does not mention %q", want)
		}
	}
	model := readDoc(t, "docs/security/model.md")
	if !strings.Contains(model, "does not cover the method, the URL or other headers") {
		t.Error("the security model does not state the scope of the webhook signature")
	}
}
