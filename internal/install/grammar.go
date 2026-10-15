// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/myriarch/myriarch/internal/domain"
)

// The line format of the plan (ADR-0046, docs/design/opamp-server.md, "Installation from the server").
//
// The plan is text/plain, one "key=value" line each, so that bash without jq and Windows PowerShell 5.1 can read
// it with a split at the first "=". It is data, never code: the installer takes the values as they are, checks
// each against a strict pattern, accepts only the keys below and runs nothing that the plan contains. The same
// rules bind the server (Text refuses to write a plan that breaks them) and any reader (ParseText is the
// reference).
//
//   - a value consists of the characters A-Z a-z 0-9 . _ : / @ + = - and nothing else; the three URL values
//     (opamp.endpoint, collector.url, supervisor.url) may also contain "[" and "]" in the authority, so that
//     an IPv6 literal such as wss://[2001:db8::1]:8443/v1/opamp can be written;
//   - lines end in "\n"; there are no empty lines, no comments, no repeated keys and no unknown keys;
//   - at most 64 lines and 8 KiB;
//   - the key "format" comes first, and a reader stops at a format that it does not know.
const (
	// PlanFormat is the value of the key "format".
	PlanFormat = 1
	// MaxPlanLines and MaxPlanBytes bound the text of a plan.
	MaxPlanLines = 64
	MaxPlanBytes = 8 << 10
)

// Keys of the plan, in the order in which they are written.
const (
	KeyFormat            = "format"
	KeyFleet             = "fleet"
	KeyVersionSource     = "version.source"
	KeyOpAMPEndpoint     = "opamp.endpoint"
	KeyCollectorVersion  = "collector.version"
	KeyCollectorURL      = "collector.url"
	KeyCollectorGrant    = "collector.grant"
	KeySupervisorVersion = "supervisor.version"
	KeySupervisorURL     = "supervisor.url"
	KeySupervisorGrant   = "supervisor.grant"
	KeyUpdaterInstall    = "updater.install"
)

// PlanKeys lists the keys of a plan of format 1, in the order in which they are written. All are required.
var PlanKeys = []string{
	KeyFormat, KeyFleet, KeyVersionSource, KeyOpAMPEndpoint,
	KeyCollectorVersion, KeyCollectorURL, KeyCollectorGrant,
	KeySupervisorVersion, KeySupervisorURL, KeySupervisorGrant,
	KeyUpdaterInstall,
}

// Where a version of a plan comes from (the value of "version.source").
const (
	// SourceGit: spec.collector.version of the fleet.
	SourceGit = "git"
	// SourceUI: the version that an operator chose in the UI within the Git frame.
	SourceUI = "ui"
	// SourceRollout: the version the fleet ran before the version rollout that is under way (its fromRef), so that
	// the new host becomes a straggler of the last wave.
	SourceRollout = "rollout"
	// SourceRequest: the version that the host asked for, inside the frame of the fleet.
	SourceRequest = "request"
)

// Plan is what the server tells a host that wants to be installed: the fleet it would join, the versions of
// collector and supervisor with the URLs and install grants of their artifacts, the OpAMP endpoint and whether
// the updater is installed. The JSON form (Accept: application/json) is this struct; the text form is Text.
type Plan struct {
	// Format is PlanFormat.
	Format int `json:"format"`
	// Fleet is the fleet the host would join.
	Fleet      string       `json:"fleet"`
	Version    PlanVersion  `json:"version"`
	OpAMP      PlanOpAMP    `json:"opamp"`
	Collector  PlanArtifact `json:"collector"`
	Supervisor PlanArtifact `json:"supervisor"`
	Updater    PlanUpdater  `json:"updater"`
}

// PlanVersion tells where the versions come from.
type PlanVersion struct {
	// Source is SourceGit, SourceUI, SourceRollout or SourceRequest.
	Source string `json:"source"`
}

// PlanOpAMP is the OpAMP endpoint the host connects to.
type PlanOpAMP struct {
	Endpoint string `json:"endpoint"`
}

// PlanArtifact is an artifact that the host downloads from the server: collector or supervisor. The installer adds
// ".sha256" and ".sigstore.json" to the URL to get the checksum and the Sigstore bundle, and sends the grant as a
// bearer token.
type PlanArtifact struct {
	// Version is the version without "v".
	Version string `json:"version"`
	// URL is the artifact URL on the origin of the server.
	URL string `json:"url"`
	// Grant is the install grant (a "myrd_" token) for this artifact.
	Grant string `json:"grant"`
}

// PlanUpdater tells whether the myriarch-updater is installed with the collector.
type PlanUpdater struct {
	Install bool `json:"install"`
}

// Pairs returns the keys and values of the plan in the order of PlanKeys.
func (p *Plan) Pairs() [][2]string {
	return [][2]string{
		{KeyFormat, fmt.Sprint(p.Format)},
		{KeyFleet, p.Fleet},
		{KeyVersionSource, p.Version.Source},
		{KeyOpAMPEndpoint, p.OpAMP.Endpoint},
		{KeyCollectorVersion, p.Collector.Version},
		{KeyCollectorURL, p.Collector.URL},
		{KeyCollectorGrant, p.Collector.Grant},
		{KeySupervisorVersion, p.Supervisor.Version},
		{KeySupervisorURL, p.Supervisor.URL},
		{KeySupervisorGrant, p.Supervisor.Grant},
		{KeyUpdaterInstall, fmt.Sprint(p.Updater.Install)},
	}
}

// Check reports the first rule that the plan breaks: a key without a value, a value that does not match the
// pattern of its key, a text that is too long.
func (p *Plan) Check() error {
	_, err := p.text()
	return err
}

// Text returns the plan in the line format. It fails, and returns no text, when a value breaks a rule of the
// grammar, so that a plan with a character that a reader would have to interpret never leaves the server.
func (p *Plan) Text() (string, error) { return p.text() }

func (p *Plan) text() (string, error) {
	var b strings.Builder
	pairs := p.Pairs()
	if len(pairs) > MaxPlanLines {
		return "", fmt.Errorf("plan: more than %d lines", MaxPlanLines)
	}
	for _, kv := range pairs {
		if err := checkValue(kv[0], kv[1]); err != nil {
			return "", err
		}
		b.WriteString(kv[0])
		b.WriteByte('=')
		b.WriteString(kv[1])
		b.WriteByte('\n')
	}
	if b.Len() > MaxPlanBytes {
		return "", fmt.Errorf("plan: more than %d bytes", MaxPlanBytes)
	}
	return b.String(), nil
}

// ParseText reads a plan in the line format with the rules that an installer applies: every rule of the grammar,
// only known keys, every key exactly once, "format" first and equal to PlanFormat. It returns the values by key.
// The installers of the project implement the same rules in bash and PowerShell; this is the reference for them
// and for the tools that read a plan.
func ParseText(text string) (map[string]string, error) {
	if len(text) > MaxPlanBytes {
		return nil, fmt.Errorf("plan: more than %d bytes", MaxPlanBytes)
	}
	if text == "" || !strings.HasSuffix(text, "\n") {
		return nil, errors.New("plan: the text does not end in a newline")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) > MaxPlanLines {
		return nil, fmt.Errorf("plan: more than %d lines", MaxPlanLines)
	}
	known := map[string]bool{}
	for _, k := range PlanKeys {
		known[k] = true
	}
	out := make(map[string]string, len(lines))
	for i, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("plan: line %d has no \"=\"", i+1)
		}
		if i == 0 && key != KeyFormat {
			return nil, fmt.Errorf("plan: the first line must be %q", KeyFormat)
		}
		if !known[key] {
			return nil, fmt.Errorf("plan: line %d: unknown key %q", i+1, truncateKey(key))
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("plan: line %d: the key %q is repeated", i+1, key)
		}
		if key == KeyFormat && value != fmt.Sprint(PlanFormat) {
			return nil, fmt.Errorf("plan: format %q is not supported (this reader knows format %d)", truncateKey(value), PlanFormat)
		}
		if err := checkValue(key, value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	for _, k := range PlanKeys {
		if _, ok := out[k]; !ok {
			return nil, fmt.Errorf("plan: the key %q is missing", k)
		}
	}
	return out, nil
}

func truncateKey(k string) string {
	if len(k) > 40 {
		return k[:40] + "..."
	}
	return k
}

// Patterns of the values. A value is one of the characters A-Z a-z 0-9 . _ : / @ + = - (valueChars); a URL value may
// also contain the brackets of an IPv6 literal in its authority.
var (
	valueChars   = regexp.MustCompile(`^[A-Za-z0-9._:/@+=-]+$`)
	urlChars     = regexp.MustCompile(`^[A-Za-z0-9._:/@+=\[\]-]+$`)
	urlValue     = regexp.MustCompile(`^(?:https?|wss?)://(?:[A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(?::[0-9]{1,5})?(?:/[A-Za-z0-9._:/@+=-]*)?$`)
	versionValue = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	grantValue   = regexp.MustCompile(`^myrd_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
)

// checkValue checks the value of one key against the grammar and the pattern of the key.
func checkValue(key, value string) error {
	bad := func(why string) error { return fmt.Errorf("plan: %s: %s", key, why) }
	if value == "" {
		return bad("the value is empty")
	}
	// brackets are allowed in the three URL values only, and urlValue lets them stand only around an IPv6 literal
	chars := valueChars
	if key == KeyOpAMPEndpoint || key == KeyCollectorURL || key == KeySupervisorURL {
		chars = urlChars
	}
	if !chars.MatchString(value) {
		return bad("the value contains a character that is not allowed")
	}
	switch key {
	case KeyFormat:
		if value != fmt.Sprint(PlanFormat) {
			return bad(fmt.Sprintf("must be %d", PlanFormat))
		}
	case KeyFleet:
		if !domain.ValidDNSLabel(value) {
			return bad("is not the name of a fleet")
		}
	case KeyVersionSource:
		switch value {
		case SourceGit, SourceUI, SourceRollout, SourceRequest:
		default:
			return bad("is not a known source")
		}
	case KeyOpAMPEndpoint, KeyCollectorURL, KeySupervisorURL:
		if !urlValue.MatchString(value) {
			return bad("is not an http(s) or ws(s) URL")
		}
	case KeyCollectorVersion, KeySupervisorVersion:
		if !versionValue.MatchString(value) || len(value) > 64 {
			return bad("is not a version without a leading v")
		}
	case KeyCollectorGrant, KeySupervisorGrant:
		if !grantValue.MatchString(value) {
			return bad("is not an install grant")
		}
	case KeyUpdaterInstall:
		if value != "true" && value != "false" {
			return bad("must be true or false")
		}
	default:
		return fmt.Errorf("plan: unknown key %q", truncateKey(key))
	}
	return nil
}
