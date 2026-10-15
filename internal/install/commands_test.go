// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/domain"
)

// ------------------------------------------------------------------- fixtures ----

// testToken has the form of an enrollment token and is made up, so that a secret scanner finds nothing in the source.
var testToken = "myre_" + strings.Repeat("Ab1x", 8)

// stubBundles is a BundleStore with the state that a test gives it.
type stubBundles struct {
	mu       sync.Mutex
	list     BundleList
	prepared int
}

func (s *stubBundles) State() BundleList {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.list
	cp.Items = append([]Bundle(nil), s.list.Items...)
	return cp
}

func (s *stubBundles) Find(p Platform) (Bundle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.list.Items {
		if b.OS == p.OS && b.Arch == p.Arch {
			return b, true
		}
	}
	return Bundle{}, false
}

func (s *stubBundles) Prepare() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepared++
}

func (s *stubBundles) Open(string) (*os.File, Bundle, error) { return nil, Bundle{}, ErrBundleNotFound }

func (s *stubBundles) set(os, arch, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list.Items {
		if s.list.Items[i].OS == os && s.list.Items[i].Arch == arch {
			s.list.Items[i].Status = status
			if status == BundleFailed {
				s.list.Items[i].Error = "the last attempt failed"
			}
		}
	}
}

// goldenBundles are the bundles of the release 0.2.0 with hashes that do not change.
func goldenBundles() *stubBundles {
	s := &stubBundles{list: BundleList{Source: BundleSourceRelease, Version: "0.2.0", Items: []Bundle{}}}
	for _, p := range allPlatforms {
		name := BundleFilename("0.2.0", p)
		s.list.Items = append(s.list.Items, Bundle{OS: p.OS, Arch: p.Arch, Version: "0.2.0", Filename: name, Status: BundleReady,
			SHA256: sumHex([]byte("bundle " + p.String())), Size: 8453486})
	}
	return s
}

type commandsEnv struct {
	t       *testing.T
	planner *Planner
	bundles *stubBundles
}

// newCommandsEnv builds a planner whose public URL is https://fleet.example.com.
func newCommandsEnv(t *testing.T, mut ...func(d *Deps, s *stubBundles)) *commandsEnv {
	t.Helper()
	pe := newPlanEnv(t)
	s := goldenBundles()
	d := Deps{Store: pe.st, Assigner: pe.assigner, Artifacts: pe.arts, Config: pe.cfg, PublicURL: "https://fleet.example.com", OpAMPPath: "/v1/opamp", Bundles: s}
	for _, m := range mut {
		m(&d, s)
	}
	p, err := NewPlanner(d)
	require.NoError(t, err)
	return &commandsEnv{t: t, planner: p, bundles: s}
}

func devEnv(t *testing.T) *commandsEnv {
	return newCommandsEnv(t, func(d *Deps, s *stubBundles) { d.DevMode, d.PublicURL = true, "http://localhost:8080" })
}

// site is a request for a host at an edge site.
func site(mut ...func(r *CommandsRequest)) CommandsRequest {
	r := CommandsRequest{Attributes: map[string]string{"site.type": "edge", "site.id": "042"}}
	for _, m := range mut {
		m(&r)
	}
	return r
}

func (e *commandsEnv) render(req CommandsRequest) (*Commands, error) {
	return e.planner.RenderCommands(req)
}

func (e *commandsEnv) mustRender(req CommandsRequest) *Commands {
	e.t.Helper()
	c, err := e.render(req)
	require.NoError(e.t, err)
	require.NotNil(e.t, c)
	return c
}

// fullRequest uses every option.
func fullRequest(variant string) CommandsRequest {
	return CommandsRequest{
		Variant: variant,
		Fleet:   "linux-hosts",
		Attributes: map[string]string{
			"site.type": "edge", "site.id": "042", "site.name": "Edge site 42 (north)", "site.country": "NL", "site.region": "north",
			"site.timezone": "Europe/Amsterdam", "deployment.environment.name": "prod",
		},
		CollectorVersion: "0.162.0",
		TokenFiles:       tokenFilesFor(variant),
		CAFile:           "/etc/ssl/myriarch-ca.pem",
	}
}

func tokenFilesFor(variant string) *TokenFiles {
	if variant != VariantTokenFile {
		return nil
	}
	return &TokenFiles{Linux: "/etc/myriarch/enrollment-token", Windows: `C:\Secrets\myriarch token.txt`}
}

// ---------------------------------------------------------------------- golden ----

// The text of the commands is a contract with the people who read it before they paste it, and with the hash rules and
// the log of the hosts: the golden files pin it, for every variant and platform.
func TestCommandsGolden(t *testing.T) {
	for _, variant := range []string{VariantTokenInline, VariantTokenFile, VariantLab} {
		t.Run(variant, func(t *testing.T) {
			e := newCommandsEnv(t)
			if variant == VariantLab {
				e = devEnv(t)
			}
			c := e.mustRender(fullRequest(variant))
			require.Len(t, c.Commands, 3)
			for _, cmd := range c.Commands {
				name := strings.NewReplacer("/", "_").Replace(variant+"_"+cmd.Platform) + ".txt"
				goldenFile(t, filepath.Join("commands", name), cmd.Command)
			}
			// as the API writes it: indented, and the characters of HTML are not escaped
			var raw bytes.Buffer
			enc := json.NewEncoder(&raw)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			require.NoError(t, enc.Encode(c))
			goldenFile(t, filepath.Join("commands", variant+"_response.json"), raw.String())
		})
	}
}

// ------------------------------------------------------------------- invariants ----

type renderedCommand struct {
	variant string
	cmd     Command
}

func allRendered(t *testing.T) []renderedCommand {
	t.Helper()
	var out []renderedCommand
	for _, variant := range []string{VariantTokenInline, VariantTokenFile, VariantLab} {
		e := newCommandsEnv(t)
		if variant == VariantLab {
			e = devEnv(t)
		}
		for _, req := range []CommandsRequest{fullRequest(variant), withVariant(site(), variant)} {
			for _, cmd := range e.mustRender(req).Commands {
				out = append(out, renderedCommand{variant, cmd})
			}
		}
	}
	return out
}

func withVariant(r CommandsRequest, variant string) CommandsRequest {
	r.Variant = variant
	r.TokenFiles = tokenFilesFor(variant)
	return r
}

// linuxParts splits a Linux command into the script, the quoted words after it and the here document.
func linuxParts(t *testing.T, command string) (script string, words []string, heredoc string) {
	t.Helper()
	require.True(t, strings.HasPrefix(command, "sudo sh -c '"), command)
	rest := strings.TrimPrefix(command, "sudo sh -c '")
	i := strings.Index(rest, "'\n")
	if i < 0 {
		i = strings.Index(rest, "' sh ")
	}
	script = rest[:strings.Index(rest, "' sh ")]
	after := rest[len(script)+len("' sh "):]
	line, heredoc, _ := strings.Cut(after, "\n")
	line = strings.TrimSuffix(line, " <<'"+heredocDelimiter+"'")
	words = splitQuoted(t, line)
	return script, words, heredoc
}

// splitQuoted reads a sequence of single-quoted words separated by single spaces, the form in which the commands carry
// their values; anything else is a failure.
func splitQuoted(t *testing.T, s string) []string {
	t.Helper()
	var out []string
	for s != "" {
		require.True(t, strings.HasPrefix(s, "'"), "a word is not quoted: %q", s)
		end := strings.Index(s[1:], "'")
		require.GreaterOrEqual(t, end, 0, "unterminated word: %q", s)
		out = append(out, s[1:1+end])
		s = s[end+2:]
		if s != "" {
			require.True(t, strings.HasPrefix(s, " "), "words are not separated by one space: %q", s)
			s = s[1:]
		}
	}
	return out
}

// windowsParts splits a Windows command into the script block and the data after it.
func windowsParts(t *testing.T, command string) (script string, data string) {
	t.Helper()
	require.True(t, strings.HasPrefix(command, "& {\n"), command)
	i := strings.LastIndex(command, "\n} @{\n")
	require.GreaterOrEqual(t, i, 0, command)
	return command[len("& {\n"):i], command[i+len("\n} @{\n"):]
}

func TestCommandsNeverContainAToken(t *testing.T) {
	for _, r := range allRendered(t) {
		for _, prefix := range []string{brand.EnrollmentTokenPrefix, brand.APITokenPrefix, brand.DownloadTokenPrefix} {
			assert.NotContains(t, r.cmd.Command, prefix, "%s %s", r.variant, r.cmd.Platform)
		}
		placeholders := strings.Count(r.cmd.Command, brand.EnrollmentTokenPlaceholder)
		if r.variant == VariantTokenFile {
			assert.Zero(t, placeholders, "the variant with a file has no place for a token")
		} else {
			assert.Equal(t, 1, placeholders, "%s %s: exactly one place for the token", r.variant, r.cmd.Platform)
		}
	}
}

// The text of the code is the same for every request: values are arguments, never part of the code.
func TestCommandsHaveAConstantScript(t *testing.T) {
	var linux, windows = map[string]bool{}, map[string]bool{}
	for _, r := range allRendered(t) {
		switch r.cmd.Bundle.OS {
		case "linux":
			script, _, _ := linuxParts(t, r.cmd.Command)
			linux[script] = true
		case "windows":
			script, _ := windowsParts(t, r.cmd.Command)
			windows[script] = true
		}
	}
	assert.Len(t, linux, 1, "one script for every Linux command")
	assert.Len(t, windows, 1, "one script for every Windows command")
	for s := range linux {
		assert.NotContains(t, s, "'", "the script sits in a single-quoted word and has no quote of its own")
		assert.NotContains(t, s, "fleet.example.com", "no value in the code")
		assert.NotContains(t, s, brand.EnrollmentTokenPlaceholder)
		assert.NotContains(t, s, "{{", "all markers are replaced")
	}
	for s := range windows {
		assert.NotContains(t, s, "fleet.example.com")
		assert.NotContains(t, s, brand.EnrollmentTokenPlaceholder, "a second place for the token would receive a second copy of it")
		assert.NotContains(t, s, "{{")
	}
}

// Every value is its own quoted word, in the order of the installer's options, and the pinned hash is the one of the
// bundle.
func TestCommandsCarryTheirValuesAsQuotedWords(t *testing.T) {
	e := newCommandsEnv(t)
	c := e.mustRender(fullRequest(VariantTokenInline))
	byPlatform := map[string]Command{}
	for _, cmd := range c.Commands {
		byPlatform[cmd.Platform] = cmd
	}
	wantOptions := []string{"--fleet", "linux-hosts", "--collector-version", "0.162.0", "--site-type", "edge", "--site-id", "042",
		"--site-name", "Edge site 42 (north)", "--site-country", "NL", "--site-region", "north", "--site-timezone", "Europe/Amsterdam", "--environment", "prod"}

	for _, plat := range []string{"linux/amd64", "linux/arm64"} {
		cmd := byPlatform[plat]
		_, words, heredoc := linuxParts(t, cmd.Command)
		assert.Equal(t, append([]string{"https://fleet.example.com", "=https", "/etc/ssl/myriarch-ca.pem", "", cmd.Bundle.Filename, cmd.Bundle.SHA256}, wantOptions...), words, plat)
		assert.Equal(t, brand.EnrollmentTokenPlaceholder+"\n"+heredocDelimiter+"\n", heredoc, "the token is the here document and nothing else")
		assert.Equal(t, ShellBash, cmd.Shell)
		assert.Equal(t, plat, cmd.Platform)
	}
	win := byPlatform["windows/amd64"]
	_, data := windowsParts(t, win.Command)
	winOptions := []string{"-Fleet", "linux-hosts", "-CollectorVersion", "0.162.0", "-SiteType", "edge", "-SiteId", "042",
		"-SiteName", "Edge site 42 (north)", "-SiteCountry", "NL", "-SiteRegion", "north", "-SiteTimezone", "Europe/Amsterdam", "-Environment", "prod"}
	var quotedOptions []string
	for _, o := range winOptions {
		quotedOptions = append(quotedOptions, "'"+o+"'")
	}
	assert.Equal(t, fmt.Sprintf("  Server = 'https://fleet.example.com'\n  File = '%s'\n  Sha256 = '%s'\n  Token = '%s'\n  TokenFile = ''\n  InstallerArgs = @(%s)\n}\n",
		win.Bundle.Filename, win.Bundle.SHA256, brand.EnrollmentTokenPlaceholder, strings.Join(quotedOptions, ", ")), data)
	assert.Equal(t, ShellPowerShell, win.Shell)
	assert.Equal(t, 1, strings.Count(win.Command, win.Bundle.SHA256), "the hash is pinned once")
}

func TestCommandsOfTheFileVariantReadTheTokenFromTheHost(t *testing.T) {
	e := newCommandsEnv(t)
	c := e.mustRender(fullRequest(VariantTokenFile))
	assert.Empty(t, c.TokenPlaceholder)
	byPlatform := map[string]Command{}
	for _, cmd := range c.Commands {
		byPlatform[cmd.Platform] = cmd
	}
	lin := byPlatform["linux/amd64"]
	_, words, heredoc := linuxParts(t, lin.Command)
	assert.Equal(t, "/etc/myriarch/enrollment-token", words[3])
	assert.Empty(t, heredoc, "no here document: the token is not part of the command")
	assert.Equal(t, "/etc/myriarch/enrollment-token", lin.TokenFile)
	assert.True(t, strings.HasSuffix(lin.Command, "\n"))

	win := byPlatform["windows/amd64"]
	_, data := windowsParts(t, win.Command)
	assert.Contains(t, data, "  Token = ''\n")
	assert.Contains(t, data, `  TokenFile = 'C:\Secrets\myriarch token.txt'`+"\n")
	assert.Equal(t, `C:\Secrets\myriarch token.txt`, win.TokenFile)
}

func TestCommandsOfTheLabVariantLowerTheChecksOfTheInstaller(t *testing.T) {
	e := devEnv(t)
	c := e.mustRender(site(func(r *CommandsRequest) { r.Variant = VariantLab }))
	assert.True(t, c.Insecure)
	assert.Equal(t, "http://localhost:8080", c.Server)
	assert.Equal(t, brand.EnrollmentTokenPlaceholder, c.TokenPlaceholder)
	for _, cmd := range c.Commands {
		switch cmd.Bundle.OS {
		case "linux":
			_, words, _ := linuxParts(t, cmd.Command)
			assert.Equal(t, "=http,https", words[1])
			assert.Equal(t, "--insecure", words[len(words)-1])
		case "windows":
			_, data := windowsParts(t, cmd.Command)
			assert.Contains(t, data, "'-Insecure')")
		}
	}
}

func TestCommandsWithoutOptionsUseTheDefaults(t *testing.T) {
	e := newCommandsEnv(t)
	c := e.mustRender(site())
	assert.Equal(t, VariantTokenInline, c.Variant)
	assert.False(t, c.Insecure)
	assert.Equal(t, "0.2.0", c.Version)
	assert.Equal(t, "https://fleet.example.com", c.Server)
	require.Len(t, c.Commands, 3, "all of install.platforms")
	assert.Equal(t, []string{"linux/amd64", "linux/arm64", "windows/amd64"}, []string{c.Commands[0].Platform, c.Commands[1].Platform, c.Commands[2].Platform})
	_, words, _ := linuxParts(t, c.Commands[0].Command)
	assert.Equal(t, []string{"https://fleet.example.com", "=https", "", "", c.Commands[0].Bundle.Filename, c.Commands[0].Bundle.SHA256,
		"--site-type", "edge", "--site-id", "042"}, words, "no CA, no fleet, no version: the installer takes the plan")
}

func TestCommandsForSomePlatforms(t *testing.T) {
	e := newCommandsEnv(t)
	c := e.mustRender(site(func(r *CommandsRequest) {
		r.Platforms = []string{"windows/amd64", "linux/x86_64", "windows/amd64", "Linux/AARCH64"}
	}))
	var got []string
	for _, cmd := range c.Commands {
		got = append(got, cmd.Platform)
	}
	assert.Equal(t, []string{"windows/amd64", "linux/amd64", "linux/arm64"}, got, "in the order of the request, without duplicates, with the aliases of uname")

	// a server that offers fewer platforms
	e = newCommandsEnv(t, func(d *Deps, s *stubBundles) { d.Config.Platforms = []string{"linux/amd64"} })
	c = e.mustRender(site())
	require.Len(t, c.Commands, 1)
	assert.Equal(t, "linux/amd64", c.Commands[0].Platform)
}

func TestCommandsCarryTheBundleThatTheyPin(t *testing.T) {
	e := newCommandsEnv(t)
	c := e.mustRender(site())
	for _, cmd := range c.Commands {
		want, ok := e.bundles.Find(Platform{cmd.Bundle.OS, cmd.Bundle.Arch})
		require.True(t, ok)
		assert.Equal(t, want, cmd.Bundle)
		assert.Contains(t, cmd.Command, want.SHA256)
		assert.Contains(t, cmd.Command, want.Filename)
		assert.Equal(t, BundleReady, cmd.Bundle.Status)
	}
}

func TestCommandsAcceptAnIPv6ServerAndAPrefix(t *testing.T) {
	for url, want := range map[string]string{
		"https://[2001:db8::1]:8443":              "https://[2001:db8::1]:8443",
		"https://fleet.example.com/":              "https://fleet.example.com",
		"https://fleet.example.com/myriarch/":     "https://fleet.example.com/myriarch",
		"https://fleet.example.com:8443/a/b-c_d.": "https://fleet.example.com:8443/a/b-c_d.",
		"https://203.0.113.7":                     "https://203.0.113.7",
	} {
		e := newCommandsEnv(t, func(d *Deps, s *stubBundles) { d.PublicURL = url })
		c := e.mustRender(site())
		assert.Equal(t, want, c.Server, url)
		_, words, _ := linuxParts(t, c.Commands[0].Command)
		assert.Equal(t, want, words[0])
	}
}

func TestCommandsAcceptTheTextOfRealSites(t *testing.T) {
	e := newCommandsEnv(t)
	for _, name := range []string{"Data center 1", "Edge site 42 (north)", "Stockholm, SE", "Plant #3 / hall B", "R&D" /* refused below */, "Z\u00fcrich"} {
		_, err := e.render(site(func(r *CommandsRequest) { r.Attributes["site.name"] = name }))
		switch name {
		case "R&D", "Z\u00fcrich":
			require.Error(t, err, name)
		default:
			require.NoError(t, err, name)
		}
	}
	for _, id := range []string{"042", "dc-01", "site:12", "A.1_b", "0"} {
		_, err := e.render(site(func(r *CommandsRequest) { r.Attributes["site.id"] = id }))
		require.NoError(t, err, id)
	}
	for _, tz := range []string{"Europe/Amsterdam", "America/Argentina/Buenos_Aires", "Etc/GMT+1", "UTC"} {
		_, err := e.render(site(func(r *CommandsRequest) { r.Attributes["site.timezone"] = tz }))
		require.NoError(t, err, tz)
	}
}

// -------------------------------------------------------------- refusing input ----

// danger lists values that must never reach a command: quotation marks of every kind, the characters that end or
// change a quoted string in a shell, control characters, everything outside printable ASCII and the look of a token.
var danger = map[string]string{
	"single quote":           "ZXQ'b",
	"double quote":           `ZXQ"b`,
	"backtick":               "ZXQ`b",
	"dollar":                 "ZXQ$b",
	"command substitution":   "ZXQ$(id)b",
	"variable":               "ZXQ${HOME}",
	"semicolon":              "ZXQ;b",
	"newline":                "ZXQ\nb",
	"carriage return":        "ZXQ\rb",
	"nul":                    "ZXQ\x00b",
	"tab":                    "ZXQ\tb",
	"escape":                 "ZXQ\x1bb",
	"delete":                 "ZXQ\x7fb",
	"left single quote":      "ZXQ\u2018b",
	"right single quote":     "ZXQ\u2019b",
	"single low-9 quote":     "ZXQ\u201ab",
	"single high-reversed-9": "ZXQ\u201bb",
	"left double quote":      "ZXQ\u201cb",
	"right double quote":     "ZXQ\u201db",
	"double low-9 quote":     "ZXQ\u201eb",
	"guillemet":              "ZXQ\u00abb",
	"guillemet right":        "ZXQ\u00bbb",
	"umlaut":                 "ZXQ\u00fcrich",
	"emoji":                  "ZXQ\U0001f600",
	"no-break space":         "ZXQ\u00a0b",
	"zero-width space":       "ZXQ\u200bb",
	"right-to-left override": "ZXQ\u202eb",
	"invalid UTF-8":          "ZXQ\xc3\x28b",
	"the placeholder":        brand.EnrollmentTokenPlaceholder,
	"a pair of at signs":     "ZXQ@@b",
	"an enrollment token":    "myre_" + strings.Repeat("a", 32),
	"an api token":           "x myra_" + strings.Repeat("b", 32),
	"a download token":       "myrd_eyJhIjoiMSJ9.c2ln",
	"a token with a prefix":  "see-myre_abc",
	"backslash and pipe":     `ZXQ\|b`,
	"ampersand":              "ZXQ&b",
	"redirect":               "ZXQ>b",
	"a leading dash":         "-ZXQ",
	"a leading space":        " ZXQ",
	"an overlong value":      strings.Repeat("a", 101),
	"an empty value":         "",
}

// attributeCases are the request fields that carry text, each with a mutation that puts a value in.
func attributeCases() map[string]func(r *CommandsRequest, v string) {
	return map[string]func(r *CommandsRequest, v string){
		"attributes.site.type":                   func(r *CommandsRequest, v string) { r.Attributes["site.type"] = v },
		"attributes.site.id":                     func(r *CommandsRequest, v string) { r.Attributes["site.id"] = v },
		"attributes.site.name":                   func(r *CommandsRequest, v string) { r.Attributes["site.name"] = v },
		"attributes.site.country":                func(r *CommandsRequest, v string) { r.Attributes["site.country"] = v },
		"attributes.site.region":                 func(r *CommandsRequest, v string) { r.Attributes["site.region"] = v },
		"attributes.site.timezone":               func(r *CommandsRequest, v string) { r.Attributes["site.timezone"] = v },
		"attributes.deployment.environment.name": func(r *CommandsRequest, v string) { r.Attributes["deployment.environment.name"] = v },
	}
}

func TestCommandsRefuseDangerousInput(t *testing.T) {
	e := newCommandsEnv(t)
	for path, put := range attributeCases() {
		for name, v := range danger {
			t.Run(path+" "+name, func(t *testing.T) {
				req := site()
				put(&req, v)
				c, err := e.render(req)
				require.Nil(t, c, "no command is rendered for this input")
				is := invalid(t, err, CodeInvalidValue, path)
				assert.NotContains(t, is.Message, "ZXQ", "the message does not repeat the value")
				for _, prefix := range []string{"myre_", "myra_", "myrd_"} {
					assert.NotContains(t, err.Error(), prefix, "nor does it repeat a token")
				}
			})
		}
	}
}

func TestCommandsRefuseDangerousFleetVersionAndPaths(t *testing.T) {
	e := newCommandsEnv(t)
	// A path may well start with a dash or contain a space, and it is long enough for more than a hundred characters:
	// these are no dangers in a path.
	notForPaths := map[string]bool{"an overlong value": true, "a leading dash": true, "a leading space": true}
	for name, v := range danger {
		if v == "" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for path, req := range map[string]CommandsRequest{
				"fleet":              site(func(r *CommandsRequest) { r.Fleet = v }),
				"collectorVersion":   site(func(r *CommandsRequest) { r.CollectorVersion = v }),
				"caFile":             site(func(r *CommandsRequest) { r.CAFile = "/etc/" + v }),
				"platforms[0]":       site(func(r *CommandsRequest) { r.Platforms = []string{v} }),
				"tokenFiles.linux":   withVariant(site(func(r *CommandsRequest) { r.Platforms = []string{"linux/amd64"} }), VariantTokenFile),
				"tokenFiles.windows": withVariant(site(func(r *CommandsRequest) { r.Platforms = []string{"windows/amd64"} }), VariantTokenFile),
			} {
				switch path {
				case "tokenFiles.linux":
					req.TokenFiles = &TokenFiles{Linux: "/etc/" + v}
				case "tokenFiles.windows":
					req.TokenFiles = &TokenFiles{Windows: `C:\` + v}
				}
				if notForPaths[name] && (path == "caFile" || strings.HasPrefix(path, "tokenFiles")) {
					continue
				}
				c, err := e.render(req)
				require.Nil(t, c, path)
				var ve *domain.ValidationError
				require.ErrorAs(t, err, &ve, path)
				found := false
				for _, is := range ve.Issues {
					found = found || is.Path == path
					assert.NotContains(t, is.Message, "ZXQ", path)
					for _, prefix := range []string{"myre_", "myra_", "myrd_"} {
						assert.NotContains(t, is.Message, prefix, path)
					}
				}
				assert.True(t, found, "%s: %+v", path, ve.Issues)
			}
		})
	}
}

// Version, fleet and platform have a form of their own.
func TestCommandsCheckFleetVersionAndPlatforms(t *testing.T) {
	e := newCommandsEnv(t)
	for _, f := range []string{"Linux-Hosts", "linux_hosts", "-linux", "linux-", strings.Repeat("a", 64), "linux hosts", "linux.hosts"} {
		_, err := e.render(site(func(r *CommandsRequest) { r.Fleet = f }))
		invalid(t, err, CodeInvalidValue, "fleet")
	}
	for _, v := range []string{"latest", "0.162", "0.162.0.1", "01.2.3", "0.162.0+x y", "1.2.3-", strings.Repeat("1", 70)} {
		_, err := e.render(site(func(r *CommandsRequest) { r.CollectorVersion = v }))
		invalid(t, err, CodeInvalidValue, "collectorVersion")
	}
	c := e.mustRender(site(func(r *CommandsRequest) { r.CollectorVersion = "v0.162.0" }))
	_, words, _ := linuxParts(t, c.Commands[0].Command)
	assert.Contains(t, words, "0.162.0", "a leading v is dropped")

	for _, p := range []string{"linux", "linux/", "/amd64", "darwin/arm64", "windows/arm64", "linux/386", "linux/amd64/x", "freebsd/amd64"} {
		_, err := e.render(site(func(r *CommandsRequest) { r.Platforms = []string{p} }))
		require.Error(t, err, p)
		var ve *domain.ValidationError
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "platforms[0]", ve.Issues[0].Path, p)
	}
	_, err := e.render(site(func(r *CommandsRequest) { r.Platforms = []string{"windows/arm64"} }))
	invalid(t, err, CodePlatformNotSupported, "platforms[0]")
	many := make([]string, maxCommandPlatforms+1)
	for i := range many {
		many[i] = "linux/amd64"
	}
	_, err = e.render(site(func(r *CommandsRequest) { r.Platforms = many }))
	invalid(t, err, CodeInvalidValue, "platforms")
}

func TestCommandsCheckTheAttributes(t *testing.T) {
	e := newCommandsEnv(t)
	// the installers need the type and the ID of the site
	for _, missing := range []string{"site.type", "site.id"} {
		_, err := e.render(site(func(r *CommandsRequest) { delete(r.Attributes, missing) }))
		is := invalid(t, err, CodeInvalidValue, "attributes."+missing)
		assert.Contains(t, is.Message, "option_missing")
	}
	_, err := e.render(CommandsRequest{})
	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Len(t, ve.Issues, 2, "both are reported at once")

	// only the options of the installers: the name of the host and anything else are refused, with the key shortened
	for _, k := range []string{"host.name", "os.type", "labels.channel", "service.name", "x", strings.Repeat("k", 300), "site.type\n"} {
		_, err := e.render(site(func(r *CommandsRequest) { r.Attributes[k] = "v" }))
		require.Error(t, err, k)
		require.ErrorAs(t, err, &ve)
		var is domain.Issue
		for _, i := range ve.Issues {
			if i.Code == CodeUnknownAttribute {
				is = i
			}
		}
		require.Equal(t, CodeUnknownAttribute, is.Code, k)
		assert.LessOrEqual(t, len(is.Path), len("attributes.")+45, "the path is shortened")
	}
}

func TestCommandsCheckTheVariant(t *testing.T) {
	e := newCommandsEnv(t)
	_, err := e.render(site(func(r *CommandsRequest) { r.Variant = "token-inline" }))
	invalid(t, err, CodeInvalidValue, "variant")

	// the lab variant lowers the checks of the installer: a production server does not render it
	_, err = e.render(site(func(r *CommandsRequest) { r.Variant = VariantLab }))
	is := invalid(t, err, CodeVariantNotAvailable, "variant")
	assert.Contains(t, is.Message, "development server")
	// not even on a server whose public URL is plain http
	e2 := newCommandsEnv(t, func(d *Deps, s *stubBundles) { d.PublicURL = "http://localhost:8080" })
	_, err = e2.render(site(func(r *CommandsRequest) { r.Variant = VariantLab }))
	invalid(t, err, CodeVariantNotAvailable, "variant")

	// token files belong to their variant
	_, err = e.render(site(func(r *CommandsRequest) { r.TokenFiles = &TokenFiles{Linux: "/etc/token"} }))
	invalid(t, err, CodeInvalidValue, "tokenFiles")
	_, err = e.render(site(func(r *CommandsRequest) { r.Variant = VariantTokenFile }))
	require.Error(t, err, "the variant with a file needs its paths")
}

func TestCommandsNeedTheTokenFileOfEveryOperatingSystemTheyRender(t *testing.T) {
	e := newCommandsEnv(t)
	req := site(func(r *CommandsRequest) { r.Variant = VariantTokenFile })
	_, err := e.render(req)
	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Len(t, ve.Issues, 2)
	invalid(t, err, CodeInvalidValue, "tokenFiles.linux")
	invalid(t, err, CodeInvalidValue, "tokenFiles.windows")

	// a Linux request needs the Linux path only
	req.Platforms = []string{"linux/amd64"}
	req.TokenFiles = &TokenFiles{Linux: "/etc/myriarch/enrollment-token"}
	c := e.mustRender(req)
	require.Len(t, c.Commands, 1)
	req.Platforms = []string{"windows/amd64"}
	_, err = e.render(req)
	invalid(t, err, CodeInvalidValue, "tokenFiles.windows")
}

func TestCommandsCheckPaths(t *testing.T) {
	e := newCommandsEnv(t)
	good := map[string]string{"linux": "/etc/myriarch/enrollment-token", "windows": `C:\ProgramData\Secrets\token.txt`}
	for os, p := range good {
		req := withVariant(site(func(r *CommandsRequest) { r.Platforms = []string{os + "/amd64"} }), VariantTokenFile)
		req.TokenFiles = &TokenFiles{}
		if os == "linux" {
			req.TokenFiles.Linux = p
		} else {
			req.TokenFiles.Windows = p
		}
		e.mustRender(req)
	}
	linux := func(p string) error {
		req := withVariant(site(func(r *CommandsRequest) { r.Platforms = []string{"linux/amd64"} }), VariantTokenFile)
		req.TokenFiles = &TokenFiles{Linux: p}
		_, err := e.render(req)
		return err
	}
	for _, bad := range []string{"relative/path", "./token", "token", "~/token", "/", "/etc/", "/etc/../shadow", "/etc/./token", "/etc/token ", `/etc\token`, "/etc/to ken\n",
		"/etc/\u00fctoken", "//", "", strings.Repeat("/a", 130), "/etc/token*", "/etc/{a,b}", "/etc/token?", "/tmp/a|b", "/tmp/a<b", "/tmp/a!b", "/tmp/a[1]"} {
		if bad == "" {
			continue
		}
		assert.Error(t, linux(bad), "%q", bad)
	}
	windows := func(p string) error {
		req := withVariant(site(func(r *CommandsRequest) { r.Platforms = []string{"windows/amd64"} }), VariantTokenFile)
		req.TokenFiles = &TokenFiles{Windows: p}
		_, err := e.render(req)
		return err
	}
	for _, bad := range []string{`token.txt`, `\token.txt`, `\\server\share\token.txt`, `C:token.txt`, `C:/token.txt`, `C:\`, `C:\dir\`, `C:\dir\..\token`, `C:\dir\.\token`,
		`C:\dir\to%ken%.txt` + "\n", `C:\dir\"token"`, "C:\\dir\\token ", `C:\dir\token*`, `1:\token`, `C:\dir\tok;en`, `C:\dir\tok$en`, `\\?\C:\token`} {
		assert.Error(t, windows(bad), "%q", bad)
	}
	// the CA file is a path of the Linux host
	for _, bad := range []string{"ca.pem", "C:\\ca.pem", "/etc/../ca.pem", "/etc/ca.pem'"} {
		_, err := e.render(site(func(r *CommandsRequest) { r.CAFile = bad }))
		invalid(t, err, CodeInvalidValue, "caFile")
	}
	// and it applies to Linux only: a Windows command does not carry it
	c := e.mustRender(site(func(r *CommandsRequest) { r.CAFile = "/etc/ssl/ca.pem"; r.Platforms = []string{"windows/amd64"} }))
	assert.NotContains(t, c.Commands[0].Command, "/etc/ssl/ca.pem")
}

func TestCommandsReportEveryProblemAtOnce(t *testing.T) {
	e := newCommandsEnv(t)
	_, err := e.render(CommandsRequest{Variant: "x", Platforms: []string{"plan9/mips"}, Fleet: "Bad Fleet", CollectorVersion: "latest",
		Attributes: map[string]string{"site.type": "a'b", "site.id": "", "nope": "x"}, CAFile: "ca.pem"})
	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	paths := map[string]string{}
	for _, is := range ve.Issues {
		paths[is.Path] = is.Code
	}
	assert.Equal(t, map[string]string{
		"variant": "invalid_value", "platforms[0]": "platform_not_supported", "fleet": "invalid_value", "collectorVersion": "invalid_value",
		"attributes.site.type": "invalid_value", "attributes.site.id": "invalid_value", "attributes.nope": "unknown_attribute", "caFile": "invalid_value",
	}, paths)
}

// ------------------------------------------------------------ what the server owes ----

func TestCommandsRefuseALocalBuild(t *testing.T) {
	none := &stubBundles{list: BundleList{Source: BundleSourceNone, Reason: ReasonLocalBuild, Items: []Bundle{}}}
	e := newCommandsEnv(t, func(d *Deps, s *stubBundles) { d.Bundles = none })
	c, err := e.render(site())
	require.Nil(t, c)
	r := refusal(t, err, domain.ErrPrecondition, CodeLocalBuild)
	assert.Contains(t, r.Detail, "local build")

	// a server without any bundle store says the same
	e = newCommandsEnv(t, func(d *Deps, s *stubBundles) { d.Bundles = nil })
	_, err = e.render(site())
	refusal(t, err, domain.ErrPrecondition, CodeLocalBuild)

	// but a bad request is a bad request first
	_, err = e.render(CommandsRequest{})
	invalid(t, err, CodeInvalidValue, "attributes.site.type")
}

func TestCommandsWaitForTheBundles(t *testing.T) {
	e := newCommandsEnv(t)
	e.bundles.set("linux", "arm64", BundlePreparing)
	c, err := e.render(site())
	require.Nil(t, c, "no command for the platforms that are ready while another is not")
	r := refusal(t, err, domain.ErrUnavailable, CodeBundlePreparing)
	assert.Equal(t, retryBundlePreparing, r.RetryAfter)
	assert.Equal(t, 1, e.bundles.prepared, "the request starts the preparation")

	// a request for the platforms that are ready does not wait for the others
	c = e.mustRender(site(func(r *CommandsRequest) { r.Platforms = []string{"linux/amd64", "windows/amd64"} }))
	assert.Len(t, c.Commands, 2)
	assert.Equal(t, 2, e.bundles.prepared, "every request lets the server look at its bundles")

	e.bundles.set("linux", "arm64", BundlePending)
	_, err = e.render(site())
	refusal(t, err, domain.ErrUnavailable, CodeBundlePreparing)
}

func TestCommandsSayWhenABundleFailed(t *testing.T) {
	e := newCommandsEnv(t)
	e.bundles.set("windows", "amd64", BundleFailed)
	_, err := e.render(site())
	r := refusal(t, err, domain.ErrUnavailable, CodeBundleFailed)
	assert.Equal(t, retryBundleFailed, r.RetryAfter)
	assert.Contains(t, r.Detail, "windows/amd64")
	assert.Contains(t, r.Detail, "the last attempt failed")
	assert.Equal(t, 1, e.bundles.prepared, "and starts the next attempt when it is due")
	// failed wins over preparing: it needs the attention of an operator
	e.bundles.set("linux", "amd64", BundlePreparing)
	_, err = e.render(site())
	refusal(t, err, domain.ErrUnavailable, CodeBundleFailed)
}

func TestCommandsNeedAnHTTPSServer(t *testing.T) {
	for _, url := range []string{"http://fleet.example.com", "http://localhost:8080", "", "fleet.example.com", "ftp://fleet.example.com", "https://", "https://user:pw@fleet.example.com",
		"https://fleet.example.com?x=1", "https://fleet.example.com/#frag", "https://fleet.example.com/a'b", "https://fleet.example.com/a b", "https://fleet.example.com/a$b",
		"https://fleet.example.com/a;b", "https://fleet.exa mple.com", "https://fleet.example.com/%27", "https://fleet.example.com/a%20b", "https://[::1]x/", "https://fleet_example.com"} {
		e := newCommandsEnv(t, func(d *Deps, s *stubBundles) { d.PublicURL = url })
		c, err := e.render(site())
		require.Nil(t, c, url)
		refusal(t, err, domain.ErrPrecondition, CodeServerSettings)
		assert.NotContains(t, err.Error(), "pw", url)
	}
}

// With opamp.requireClientCert the plan and the download of the bundle ask for a verified client certificate, and the
// command cannot present one: it would fail at the download for a reason the message does not name. The server says so
// when the command is rendered, whichever variant and platform is asked for, and after the request itself was checked.
func TestCommandsRefuseAServerThatRequiresAClientCertificate(t *testing.T) {
	e := newCommandsEnv(t, func(d *Deps, s *stubBundles) { d.RequireClientCert = true })
	for _, req := range []CommandsRequest{
		site(), fullRequest(VariantTokenInline), fullRequest(VariantTokenFile),
		site(func(r *CommandsRequest) { r.Platforms = []string{"windows/amd64"} }),
	} {
		c, err := e.render(req)
		require.Nil(t, c)
		r := refusal(t, err, domain.ErrPrecondition, CodeServerSettings)
		assert.Contains(t, r.Detail, "opamp.requireClientCert")
		assert.Contains(t, r.Detail, "client certificate")
		assert.Contains(t, r.Detail, "manual path")
	}
	assert.Zero(t, e.bundles.prepared, "a setting that no wait can change does not start the preparation of the bundles")

	// a bad request is a bad request first
	_, err := e.render(CommandsRequest{})
	invalid(t, err, CodeInvalidValue, "attributes.site.type")

	// the development server is no exception, and without the setting everything works as before
	dev := newCommandsEnv(t, func(d *Deps, s *stubBundles) {
		d.DevMode, d.PublicURL, d.RequireClientCert = true, "http://localhost:8080", true
	})
	_, err = dev.render(site(func(r *CommandsRequest) { r.Variant = VariantLab }))
	refusal(t, err, domain.ErrPrecondition, CodeServerSettings)
	assert.NotEmpty(t, newCommandsEnv(t).mustRender(site()).Commands)
}

func TestCommandServer(t *testing.T) {
	good := map[string][2]string{
		"https://fleet.example.com":       {"https://fleet.example.com", "https://fleet.example.com"},
		"https://fleet.example.com:443/x": {"https://fleet.example.com:443/x", "https://fleet.example.com:443/x"},
		"http://localhost:8080":           {"", "http://localhost:8080"},
		"http://127.0.0.1:18080/":         {"", "http://127.0.0.1:18080"},
	}
	for in, want := range good {
		got, err := commandServer(in, true)
		if want[0] == "" {
			require.Error(t, err, in)
		} else {
			require.NoError(t, err, in)
			assert.Equal(t, want[0], got)
		}
		got, err = commandServer(in, false)
		require.NoError(t, err, in)
		assert.Equal(t, want[1], got)
	}
}

func TestQuote(t *testing.T) {
	q, err := quote("Edge site 42 (north)")
	require.NoError(t, err)
	assert.Equal(t, "'Edge site 42 (north)'", q)
	q, err = quote("")
	require.NoError(t, err)
	assert.Equal(t, "''", q)
	for _, bad := range []string{"it's", "a\nb", "a\x00b", "a\x7fb", "caf\u00e9", "a\u2019b"} {
		_, err := quote(bad)
		assert.Error(t, err, "%q", bad)
	}
	_, err = quoteAll("fine", "not 'fine'")
	assert.Error(t, err)
}

// A bundle that the store hands out with a name or a hash that is not what the server produces is not put into a
// command.
func TestCommandsRefuseABundleWithoutAUsableNameOrHash(t *testing.T) {
	for name, mut := range map[string]func(b *Bundle){
		"name with a quote": func(b *Bundle) { b.Filename = "myriarch-updater_0.2.0_linux_amd64.tar.gz'; id #" },
		"name of a script":  func(b *Bundle) { b.Filename = "install.sh" },
		"short hash":        func(b *Bundle) { b.SHA256 = b.SHA256[:60] },
		"upper case hash":   func(b *Bundle) { b.SHA256 = strings.ToUpper(b.SHA256) },
		"hash with a quote": func(b *Bundle) { b.SHA256 = "'" + b.SHA256[1:] },
		"empty hash":        func(b *Bundle) { b.SHA256 = "" },
	} {
		e := newCommandsEnv(t)
		e.bundles.mu.Lock()
		mut(&e.bundles.list.Items[0])
		e.bundles.mu.Unlock()
		c, err := e.render(site())
		require.Error(t, err, name)
		assert.Nil(t, c, name)
	}
}

func TestCommandsGoThroughThePlannerWithItsPlatforms(t *testing.T) {
	// the planner knows install.platforms and the bundles; a bundle store that lacks a platform is a failure
	e := newCommandsEnv(t, func(d *Deps, s *stubBundles) { s.list.Items = s.list.Items[:2] })
	_, err := e.render(site())
	r := refusal(t, err, domain.ErrUnavailable, CodeBundleFailed)
	assert.Contains(t, r.Detail, "windows/amd64")
	assert.Same(t, e.bundles, e.planner.Bundles())
}

func TestCommandsNeedAnEnabledPlannerOnlyForTheRoutes(t *testing.T) {
	// the planner itself renders also when install.enabled is off: the routes decide whether anybody can ask
	e := newCommandsEnv(t, func(d *Deps, s *stubBundles) { d.Config.Enabled = false })
	assert.False(t, e.planner.Enabled())
	_, err := e.render(site())
	assert.NoError(t, err)
}

func TestCommandsRequestJSON(t *testing.T) {
	var req CommandsRequest
	require.NoError(t, json.Unmarshal([]byte(`{"variant":"token_file","platforms":["linux/amd64"],"fleet":"linux-hosts","attributes":{"site.type":"edge","site.id":"1"},
		"collectorVersion":"0.162.0","tokenFiles":{"linux":"/etc/t"},"caFile":"/etc/ca.pem"}`), &req))
	assert.Equal(t, "/etc/t", req.TokenFiles.Linux)
	js, err := json.Marshal(CommandsRequest{})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(js), "everything is optional on the wire")
}

// The setup scripts have the markers filled in and nothing else is left over.
func TestScriptMarkersAreFilled(t *testing.T) {
	for name, s := range map[string]string{"linux": scriptMarkers.Replace(linuxScript), "windows": scriptMarkers.Replace(windowsScript)} {
		assert.NotRegexp(t, regexp.MustCompile(`\{\{[A-Z_]+\}\}`), s, name)
		assert.Contains(t, s, "[myriarch-install]", name)
		assert.Contains(t, s, "/api/v1/install/bundles/", name)
		assert.Contains(t, s, brand.InstallSetupDir, name)
	}
}
