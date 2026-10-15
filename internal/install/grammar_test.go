// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testGrantA = "myrd_ZXhhbXBsZS1jb2xsZWN0b3ItZ3JhbnQ.ZXhhbXBsZS1zaWduYXR1cmU"
	testGrantB = "myrd_ZXhhbXBsZS1zdXBlcnZpc29yLWdyYW50.ZXhhbXBsZS1zaWduYXR1cmU"
)

// samplePlan is the plan of the golden files.
func samplePlan() *Plan {
	return &Plan{
		Format:  PlanFormat,
		Fleet:   "linux-hosts",
		Version: PlanVersion{Source: SourceGit},
		OpAMP:   PlanOpAMP{Endpoint: "wss://fleet.example.com/v1/opamp"},
		Collector: PlanArtifact{
			Version: "0.162.0",
			URL:     "https://fleet.example.com/api/v1/artifacts/01a14077-2a29-7e29-874e-f4649a44ee59/content/otelcol-contrib_0.162.0_linux_amd64.tar.gz",
			Grant:   testGrantA,
		},
		Supervisor: PlanArtifact{
			Version: "0.162.0",
			URL:     "https://fleet.example.com/api/v1/artifacts/01a14077-2a29-7e29-874e-f4649a44ee60/content/opampsupervisor_0.162.0_linux_amd64",
			Grant:   testGrantB,
		},
		Updater: PlanUpdater{Install: true},
	}
}

// goldenFile compares got with testdata/<name>; INSTALL_GOLDEN_UPDATE=1 rewrites the file.
func goldenFile(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("INSTALL_GOLDEN_UPDATE") == "1" {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run INSTALL_GOLDEN_UPDATE=1 go test ./internal/install to write the golden files")
	assert.Equal(t, string(want), got, name)
}

// The line format is a contract between the server and the installers (bash and PowerShell): the golden file pins it.
func TestPlanTextGolden(t *testing.T) {
	text, err := samplePlan().Text()
	require.NoError(t, err)
	goldenFile(t, "plan.txt", text)
	assert.Len(t, strings.Split(strings.TrimSuffix(text, "\n"), "\n"), len(PlanKeys))
}

func TestPlanJSONGolden(t *testing.T) {
	raw, err := json.MarshalIndent(samplePlan(), "", "  ")
	require.NoError(t, err)
	goldenFile(t, "plan.json", string(raw)+"\n")
}

func TestPlanTextRoundTrip(t *testing.T) {
	p := samplePlan()
	text, err := p.Text()
	require.NoError(t, err)
	got, err := ParseText(text)
	require.NoError(t, err)
	for _, kv := range p.Pairs() {
		assert.Equal(t, kv[1], got[kv[0]], kv[0])
	}
	assert.Len(t, got, len(PlanKeys))
	// the order is the order of the keys, "format" first
	lines := strings.Split(text, "\n")
	for i, k := range PlanKeys {
		assert.True(t, strings.HasPrefix(lines[i], k+"="), "line %d: %s", i, lines[i])
	}
	assert.NoError(t, p.Check())

	// every variant of the plan that the server can produce survives the round trip
	for _, src := range []string{SourceGit, SourceUI, SourceRollout, SourceRequest} {
		for _, upd := range []bool{true, false} {
			q := samplePlan()
			q.Version.Source, q.Updater.Install = src, upd
			text, err := q.Text()
			require.NoError(t, err)
			got, err := ParseText(text)
			require.NoError(t, err)
			assert.Equal(t, src, got[KeyVersionSource])
			assert.Equal(t, fmt.Sprint(upd), got[KeyUpdaterInstall])
		}
	}
}

func TestPlanAcceptsAnIPv6LiteralInTheURLValues(t *testing.T) {
	p := samplePlan()
	p.OpAMP.Endpoint = "wss://[2001:db8::1]:8443/v1/opamp"
	p.Collector.URL = "https://[2001:db8::1]:8443/api/v1/artifacts/01a14077-2a29-7e29-874e-f4649a44ee59/content/otelcol-contrib_0.162.0_linux_amd64.tar.gz"
	p.Supervisor.URL = "https://[2001:db8::1]/api/v1/artifacts/01a14077-2a29-7e29-874e-f4649a44ee60/content/opampsupervisor_0.162.0_linux_amd64"
	text, err := p.Text()
	require.NoError(t, err)
	assert.Contains(t, text, "opamp.endpoint=wss://[2001:db8::1]:8443/v1/opamp\n")
	_, err = ParseText(text)
	assert.NoError(t, err)
}

// Brackets are for the IPv6 literal in the authority of a URL value; they are no character for any other value.
func TestPlanRefusesBracketsEverywhereElse(t *testing.T) {
	for name, mut := range map[string]func(p *Plan){
		"in the fleet":             func(p *Plan) { p.Fleet = "linux[hosts]" },
		"in a version":             func(p *Plan) { p.Collector.Version = "0.162.0[1]" },
		"in a grant":               func(p *Plan) { p.Collector.Grant = testGrantA + "[x]" },
		"in the source":            func(p *Plan) { p.Version.Source = "git[1]" },
		"in the path of a URL":     func(p *Plan) { p.OpAMP.Endpoint = "wss://fleet.example.com/[v1]/opamp" },
		"after the literal":        func(p *Plan) { p.OpAMP.Endpoint = "wss://[2001:db8::1]/a]" },
		"two literals":             func(p *Plan) { p.OpAMP.Endpoint = "wss://[2001:db8::1][2001:db8::2]/a" },
		"an empty literal":         func(p *Plan) { p.OpAMP.Endpoint = "wss://[]/v1/opamp" },
		"a literal that is no IP":  func(p *Plan) { p.OpAMP.Endpoint = "wss://[evil.example.com]/v1/opamp" },
		"a literal behind a host":  func(p *Plan) { p.OpAMP.Endpoint = "wss://fleet.example.com[::1]/v1/opamp" },
		"a literal behind a user":  func(p *Plan) { p.OpAMP.Endpoint = "wss://user@[::1]/v1/opamp" },
		"an unclosed literal":      func(p *Plan) { p.Collector.URL = "https://[2001:db8::1/api" },
		"brackets in the query":    func(p *Plan) { p.Collector.URL = "https://fleet.example.com/a?x=[1]" },
		"a bracket only":           func(p *Plan) { p.Supervisor.URL = "https://fleet.example.com/a]" },
		"brackets in the updater":  func(p *Plan) { p.Updater.Install = true; p.Version.Source = "[" },
		"the format with brackets": func(p *Plan) { p.Format = 1; p.Fleet = "[1]" },
	} {
		p := samplePlan()
		mut(p)
		text, err := p.Text()
		assert.Error(t, err, name)
		assert.Empty(t, text, name)
		assert.Error(t, p.Check(), name)
	}
}

// A value has the characters A-Z a-z 0-9 . _ : / @ + = - and nothing else. Every character that a shell, PowerShell, a
// URL parser or a log would interpret is refused in every value.
func TestPlanRefusesEveryCharacterOutsideTheGrammar(t *testing.T) {
	forbidden := []string{
		" ", "\t", "\n", "\r", "\x00", "\x1b", "\x7f", "$", ";", "`", "'", "\"", "\\", "%", "&", "?", "#", "|", "<", ">", "(", ")",
		"{", "}", "~", "!", "*", ",", "^",
		"\u2018", "\u2019", "\u201a", "\u201b", "\u201c", "\u201d", "\u201e", "\u00a0", "\u200b", "\u202e", "\u00e9", "\u212a", "\ufeff", "\U0001f600",
		"\xc3\x28", // invalid UTF-8
	}
	mutations := map[string]func(p *Plan, s string){
		"fleet":              func(p *Plan, s string) { p.Fleet += s },
		"version source":     func(p *Plan, s string) { p.Version.Source += s },
		"endpoint":           func(p *Plan, s string) { p.OpAMP.Endpoint += s },
		"collector version":  func(p *Plan, s string) { p.Collector.Version += s },
		"collector url":      func(p *Plan, s string) { p.Collector.URL += s },
		"collector grant":    func(p *Plan, s string) { p.Collector.Grant += s },
		"supervisor version": func(p *Plan, s string) { p.Supervisor.Version += s },
		"supervisor url":     func(p *Plan, s string) { p.Supervisor.URL += s },
		"supervisor grant":   func(p *Plan, s string) { p.Supervisor.Grant += s },
	}
	for name, mut := range mutations {
		for _, ch := range forbidden {
			p := samplePlan()
			mut(p, ch)
			text, err := p.Text()
			assert.Error(t, err, "%s with %q", name, ch)
			assert.Empty(t, text, "%s with %q", name, ch)
		}
	}
	// and in front of the value and in its middle
	p := samplePlan()
	p.Fleet = "linux\nupdater.install=false"
	_, err := p.Text()
	assert.Error(t, err, "a value must not add a line")
}

func TestPlanRefusesValuesThatDoNotMatchTheirKey(t *testing.T) {
	cases := map[string]func(p *Plan){
		"empty fleet":                 func(p *Plan) { p.Fleet = "" },
		"upper case fleet":            func(p *Plan) { p.Fleet = "Linux-Hosts" },
		"fleet with a dot":            func(p *Plan) { p.Fleet = "linux.hosts" },
		"fleet too long":              func(p *Plan) { p.Fleet = strings.Repeat("a", 64) },
		"unknown source":              func(p *Plan) { p.Version.Source = "latest" },
		"empty source":                func(p *Plan) { p.Version.Source = "" },
		"format 2":                    func(p *Plan) { p.Format = 2 },
		"format 0":                    func(p *Plan) { p.Format = 0 },
		"endpoint without scheme":     func(p *Plan) { p.OpAMP.Endpoint = "fleet.example.com/v1/opamp" },
		"endpoint with ftp":           func(p *Plan) { p.OpAMP.Endpoint = "ftp://fleet.example.com/v1/opamp" },
		"endpoint with credentials":   func(p *Plan) { p.OpAMP.Endpoint = "wss://user:pw@fleet.example.com/v1/opamp" },
		"endpoint with a user":        func(p *Plan) { p.OpAMP.Endpoint = "wss://user@fleet.example.com/v1/opamp" },
		"endpoint without a host":     func(p *Plan) { p.OpAMP.Endpoint = "wss:///v1/opamp" },
		"endpoint with a huge port":   func(p *Plan) { p.OpAMP.Endpoint = "wss://fleet.example.com:123456/v1/opamp" },
		"endpoint with an empty port": func(p *Plan) { p.OpAMP.Endpoint = "wss://fleet.example.com:/v1/opamp" },
		"collector url with file":     func(p *Plan) { p.Collector.URL = "file:///etc/passwd" },
		"collector url with a data":   func(p *Plan) { p.Collector.URL = "data:text/plain;base64,AAAA" },
		"version with v":              func(p *Plan) { p.Collector.Version = "v0.162.0" },
		"version short":               func(p *Plan) { p.Collector.Version = "0.162" },
		"version leading zero":        func(p *Plan) { p.Collector.Version = "0.0162.0" },
		"version word":                func(p *Plan) { p.Supervisor.Version = "latest" },
		"version too long":            func(p *Plan) { p.Supervisor.Version = "0.162.0-" + strings.Repeat("a", 60) },
		"grant of another prefix":     func(p *Plan) { p.Collector.Grant = "myre_" + strings.Repeat("a", 32) },
		"grant of the API":            func(p *Plan) { p.Collector.Grant = "myra_" + strings.Repeat("a", 32) },
		"grant without a signature":   func(p *Plan) { p.Supervisor.Grant = "myrd_eyJhIjoiYSJ9" },
		"grant empty":                 func(p *Plan) { p.Supervisor.Grant = "" },
	}
	for name, mut := range cases {
		p := samplePlan()
		mut(p)
		text, err := p.Text()
		assert.Error(t, err, name)
		assert.Empty(t, text, name)
	}
	// and the ones that are fine
	for name, mut := range map[string]func(p *Plan){
		"prerelease":     func(p *Plan) { p.Collector.Version, p.Supervisor.Version = "0.163.0-rc.1", "0.163.0-rc.1" },
		"build metadata": func(p *Plan) { p.Collector.Version = "0.162.0+build.5" },
		"plain http":     func(p *Plan) { p.OpAMP.Endpoint = "ws://localhost:8080/v1/opamp" },
		"a path prefix":  func(p *Plan) { p.OpAMP.Endpoint = "wss://example.com/myriarch/v1/opamp" },
		"no path":        func(p *Plan) { p.OpAMP.Endpoint = "wss://fleet.example.com" },
	} {
		p := samplePlan()
		mut(p)
		_, err := p.Text()
		assert.NoError(t, err, name)
	}
}

// The reader applies the rules to a text that anyone could have produced: a compromised server, a proxy, a test.
func TestParseTextRefusesWhatTheGrammarForbids(t *testing.T) {
	good, err := samplePlan().Text()
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(good, "\n"), "\n")
	join := func(l []string) string { return strings.Join(l, "\n") + "\n" }
	without := func(i int) []string { return append(append([]string{}, lines[:i]...), lines[i+1:]...) }
	with := func(i int, v string) []string { c := append([]string{}, lines...); c[i] = v; return c }

	cases := map[string]string{
		"empty":                    "",
		"no newline at the end":    strings.TrimSuffix(good, "\n"),
		"crlf":                     strings.ReplaceAll(good, "\n", "\r\n"),
		"an empty line":            join(append(append([]string{}, lines[:3]...), append([]string{""}, lines[3:]...)...)),
		"a blank line at the end":  good + "\n",
		"a comment":                join(append([]string{"# plan"}, lines...)),
		"format is not first":      join(append(append([]string{}, lines[1:]...), lines[0])),
		"unknown format":           join(with(0, "format=2")),
		"a format of text":         join(with(0, "format=one")),
		"a key without a value":    join(with(1, "fleet")),
		"an empty value":           join(with(1, "fleet=")),
		"an unknown key":           join(append(append([]string{}, lines...), "extra=1")),
		"an unknown key (case)":    join(with(1, "Fleet=linux-hosts")),
		"a key with a space":       join(with(1, "fleet =linux-hosts")),
		"a value with a space":     join(with(1, "fleet= linux-hosts")),
		"a repeated key":           join(append(append([]string{}, lines...), lines[1])),
		"a missing key":            join(without(len(lines) - 1)),
		"a missing key in between": join(without(6)),
		"a shell command":          join(with(1, "fleet=x;rm")),
		"a command substitution":   join(with(1, "fleet=$(id)")),
		"a quote":                  join(with(4, "collector.version='0.162.0'")),
		"a url with credentials":   join(with(3, "opamp.endpoint=wss://u:p@fleet.example.com/v1/opamp")),
		"a null byte":              strings.Replace(good, "git", "gi\x00", 1),
		"a byte order mark":        "\ufeff" + good,
		"too many lines":           join(append(append([]string{}, lines...), manyKeys(MaxPlanLines)...)),
		"too many bytes":           join(with(1, "fleet="+strings.Repeat("a", MaxPlanBytes))),
	}
	for name, text := range cases {
		got, err := ParseText(text)
		assert.Error(t, err, name)
		assert.Nil(t, got, name)
	}
	_, err = ParseText(good)
	require.NoError(t, err)
}

func manyKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("k%d=v", i)
	}
	return out
}

func TestParseTextNamesTheKeyButNeverEchoesTheValue(t *testing.T) {
	_, err := ParseText(strings.Replace(mustText(t), "fleet=linux-hosts", "fleet=secret-value;", 1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fleet")
	assert.NotContains(t, err.Error(), "secret-value", "a value may be a credential")
	_, err = ParseText(strings.Replace(mustText(t), "collector.grant="+testGrantA, "collector.grant="+testGrantA+"!", 1))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testGrantA)
}

func mustText(t *testing.T) string {
	t.Helper()
	s, err := samplePlan().Text()
	require.NoError(t, err)
	return s
}

func TestPlanTextStaysWithinTheLimits(t *testing.T) {
	text := mustText(t)
	assert.Less(t, len(text), MaxPlanBytes/4, "a plan is far from the limit")
	assert.LessOrEqual(t, len(PlanKeys), MaxPlanLines)
	assert.Equal(t, 11, len(PlanKeys))
	p := samplePlan()
	p.Collector.URL = "https://fleet.example.com/" + strings.Repeat("a", MaxPlanBytes)
	_, err := p.Text()
	assert.Error(t, err, "the text must not exceed 8 KiB")
}

// A reader of random text never panics, and what it accepts is a plan that the writer could have produced.
func FuzzParseText(f *testing.F) {
	good, _ := samplePlan().Text()
	f.Add(good)
	f.Add("")
	f.Add("format=1\n")
	f.Add(strings.Replace(good, "git", "ui", 1))
	f.Add(strings.ReplaceAll(good, "\n", "\r\n"))
	f.Add("format=1\nfleet=a;b\n")
	f.Fuzz(func(t *testing.T, text string) {
		got, err := ParseText(text)
		if err != nil {
			require.Nil(t, got)
			return
		}
		require.Len(t, got, len(PlanKeys))
		for _, k := range PlanKeys {
			v, ok := got[k]
			require.True(t, ok, k)
			require.NoError(t, checkValue(k, v), k)
			for _, r := range v {
				require.True(t, r < 0x7f && r > 0x20, "%s holds %q", k, r)
			}
		}
		// and the same lines come out again, whatever their order after "format"
		var want []string
		for _, k := range PlanKeys {
			want = append(want, k+"="+got[k])
		}
		have := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		sort.Strings(want)
		sort.Strings(have)
		require.Equal(t, want, have)
	})
}
