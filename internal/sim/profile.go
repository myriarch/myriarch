// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
)

// DurRange is a duration or a uniformly distributed range ("3s" or
// "1s-5s").
type DurRange struct {
	Min, Max time.Duration
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *DurRange) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	r, err := ParseDurRange(s)
	if err != nil {
		return err
	}
	*d = r
	return nil
}

// ParseDurRange parses "3s" or "1s-5s" (the dash separates two Go durations).
func ParseDurRange(s string) (DurRange, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DurRange{}, nil
	}
	lo, hi, isRange := strings.Cut(s, "-")
	a, err := time.ParseDuration(strings.TrimSpace(lo))
	if err != nil {
		return DurRange{}, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if !isRange {
		return DurRange{Min: a, Max: a}, nil
	}
	b, err := time.ParseDuration(strings.TrimSpace(hi))
	if err != nil {
		return DurRange{}, fmt.Errorf("invalid duration range %q: %w", s, err)
	}
	if b < a || a < 0 {
		return DurRange{}, fmt.Errorf("invalid duration range %q: max < min", s)
	}
	return DurRange{Min: a, Max: b}, nil
}

// Pick returns a value in [Min, Max] using u in [0,1).
func (d DurRange) Pick(u float64) time.Duration {
	if d.Max <= d.Min {
		return d.Min
	}
	return d.Min + time.Duration(u*float64(d.Max-d.Min))
}

// Duration is a YAML duration ("30s").
type Duration time.Duration

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Behavior controls the simulated agents.
type Behavior struct {
	ConnectRate             float64  `yaml:"connectRate"`
	HeartbeatInterval       Duration `yaml:"heartbeatInterval"`
	ConfigApplyDelay        DurRange `yaml:"configApplyDelay"`
	ConfigFailureRate       float64  `yaml:"configFailureRate"`
	FailConfigsContaining   string   `yaml:"failConfigsContaining"`
	UnhealthyAfterApplyRate float64  `yaml:"unhealthyAfterApplyRate"`
	RestartDuration         DurRange `yaml:"restartDuration"`
	// StartupDuration is the time between the restarted collector's first
	// message and StatusOK. A real supervisor reports the new version with
	// {healthy:false}, then the collector's StatusStarting, then StatusOK.
	StartupDuration DurRange `yaml:"startupDuration"`
	// ReconnectOnUpgrade makes a version change reconnect the agent (the
	// supervisor/pod is restarted by the myriarch-updater, AWX or Kubernetes)
	// instead of restarting in place.
	ReconnectOnUpgrade bool `yaml:"reconnectOnUpgrade"`
	// ValidateConfig models supervisor validate_config: true: an invalid config
	// is reported FAILED and the old config simply keeps running, there is no
	// APPLIED report for the previous hash afterwards.
	ValidateConfig         bool     `yaml:"validateConfig"`
	UpdaterInstallDuration DurRange `yaml:"updaterInstallDuration"`
	UpdateFailureRate      float64  `yaml:"updateFailureRate"`
	// VersionAfterUpdate: "offered" (default) or "unchanged".
	VersionAfterUpdate string  `yaml:"versionAfterUpdate"`
	OfflineChurnRate   float64 `yaml:"offlineChurnRate"`
}

// Group is a class of simulated hosts.
type Group struct {
	Name            string            `yaml:"name"`
	Weight          float64           `yaml:"weight"`
	OS              string            `yaml:"os"`
	Arch            string            `yaml:"arch"`
	Version         string            `yaml:"version"`
	Updater         bool              `yaml:"updater"`
	HostnamePattern string            `yaml:"hostnamePattern"`
	SiteIDPattern   string            `yaml:"siteIdPattern"`
	Attributes      map[string]string `yaml:"attributes"`
	FixedSiteIDs    []string          `yaml:"fixedSiteIds"`
}

// Profile is a simulation profile (examples/sim/mixed-fleet.yaml).
type Profile struct {
	Seed       int64    `yaml:"seed"`
	Components string   `yaml:"components"`
	Groups     []Group  `yaml:"groups"`
	Behavior   Behavior `yaml:"behavior"`

	baseDir string
}

// DefaultBehavior returns the defaults for omitted behavior fields.
func DefaultBehavior() Behavior {
	return Behavior{
		ConnectRate:            100,
		HeartbeatInterval:      Duration(30 * time.Second),
		ConfigApplyDelay:       DurRange{Min: time.Second, Max: 5 * time.Second},
		RestartDuration:        DurRange{Min: 3 * time.Second, Max: 3 * time.Second},
		StartupDuration:        DurRange{Min: time.Second, Max: 2 * time.Second},
		UpdaterInstallDuration: DurRange{Min: 10 * time.Second, Max: 40 * time.Second},
		VersionAfterUpdate:     "offered",
	}
}

// LoadProfile reads and validates a profile file.
func LoadProfile(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read profile: %w", err)
	}
	p, err := ParseProfile(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.baseDir = filepath.Dir(path)
	return p, nil
}

// ParseProfile parses and validates profile YAML (strict).
func ParseProfile(data []byte) (*Profile, error) {
	p := &Profile{Behavior: DefaultBehavior()}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(p); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse profile: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// ComponentsPath resolves the components file relative to the profile file.
func (p *Profile) ComponentsPath() string {
	if p.Components == "" {
		return ""
	}
	if filepath.IsAbs(p.Components) {
		return p.Components
	}
	return filepath.Join(p.baseDir, p.Components)
}

// Validate checks the profile.
func (p *Profile) Validate() error {
	var errs []error
	if len(p.Groups) == 0 {
		errs = append(errs, errors.New("profile has no groups"))
	}
	seen := map[string]bool{}
	for i, g := range p.Groups {
		at := fmt.Sprintf("groups[%d] (%s)", i, g.Name)
		switch {
		case g.Name == "":
			errs = append(errs, fmt.Errorf("groups[%d]: name is required", i))
		case seen[g.Name]:
			errs = append(errs, fmt.Errorf("%s: duplicate group name", at))
		}
		seen[g.Name] = true
		if g.Weight <= 0 || math.IsNaN(g.Weight) || math.IsInf(g.Weight, 0) {
			errs = append(errs, fmt.Errorf("%s: weight must be > 0", at))
		}
		if g.OS == "" || g.Arch == "" {
			errs = append(errs, fmt.Errorf("%s: os and arch are required", at))
		}
		if _, err := normalizeVersion(g.Version); err != nil {
			errs = append(errs, fmt.Errorf("%s: version: %w", at, err))
		}
		if g.HostnamePattern == "" || g.SiteIDPattern == "" {
			errs = append(errs, fmt.Errorf("%s: hostnamePattern and siteIdPattern are required", at))
		}
		for _, pat := range []string{g.HostnamePattern, g.SiteIDPattern} {
			if _, err := countVerbs(pat); err != nil {
				errs = append(errs, fmt.Errorf("%s: pattern %q: %w", at, pat, err))
			}
		}
	}
	b := p.Behavior
	for name, v := range map[string]float64{
		"configFailureRate": b.ConfigFailureRate, "unhealthyAfterApplyRate": b.UnhealthyAfterApplyRate,
		"updateFailureRate": b.UpdateFailureRate, "offlineChurnRate": b.OfflineChurnRate,
	} {
		if v < 0 || v > 1 {
			errs = append(errs, fmt.Errorf("behavior.%s must be within [0,1]", name))
		}
	}
	if b.ConnectRate <= 0 {
		errs = append(errs, errors.New("behavior.connectRate must be > 0"))
	}
	if b.HeartbeatInterval < 0 {
		errs = append(errs, errors.New("behavior.heartbeatInterval must be >= 0"))
	}
	if b.VersionAfterUpdate != "offered" && b.VersionAfterUpdate != "unchanged" {
		errs = append(errs, fmt.Errorf("behavior.versionAfterUpdate must be offered or unchanged, got %q", b.VersionAfterUpdate))
	}
	return errors.Join(errs...)
}

// countVerbs returns the number of formatting verbs in a pattern ("%%" is
// a literal percent). Patterns may have 0 or 1 verb.
func countVerbs(pattern string) (int, error) {
	n := 0
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '%' {
			continue
		}
		i++
		if i >= len(pattern) {
			return 0, errors.New("dangling %")
		}
		if pattern[i] == '%' {
			continue
		}
		n++
		// skip flags/width up to the verb letter
		for i < len(pattern) && !isVerb(pattern[i]) {
			i++
		}
	}
	if n > 1 {
		return n, errors.New("at most one formatting verb is supported")
	}
	return n, nil
}

func isVerb(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// formatPattern formats pattern with n if it contains a verb; otherwise the
// pattern is used unchanged (e.g. "HQ1").
func formatPattern(pattern string, n int) string {
	if v, _ := countVerbs(pattern); v == 0 {
		return strings.ReplaceAll(pattern, "%%", "%")
	}
	return fmt.Sprintf(pattern, n)
}

// normalizeVersion strips a leading "v" and checks X.Y.Z.
func normalizeVersion(v string) (string, error) {
	n := strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(n, "-", 2)
	nums := strings.Split(parts[0], ".")
	if len(nums) != 3 {
		return "", fmt.Errorf("invalid version %q (want X.Y.Z)", v)
	}
	for _, x := range nums {
		if _, err := strconv.ParseUint(x, 10, 32); err != nil {
			return "", fmt.Errorf("invalid version %q (want X.Y.Z)", v)
		}
	}
	return n, nil
}

// AgentSpec is the static identity of one simulated host.
type AgentSpec struct {
	Group         string
	Index         int // 1-based within the group
	Hostname      string
	SiteID        string
	OS            string
	Arch          string
	Version       string // without "v"
	WithUpdater   bool
	Attributes    map[string]string // profile attributes (without site.id)
	CollectorUID  uuid.UUID
	UpdaterUID    uuid.UUID
	OSDescription string
}

var uidNamespace = uuid.MustParse("5b0c47f0-3f4a-4c6e-9d65-0e1e7c6a1f10")

// Allocate distributes n agents over the groups proportionally to their
// weights (largest remainder method, so the sum is exactly n).
func (p *Profile) Allocate(n int) []int {
	counts := make([]int, len(p.Groups))
	var total float64
	for _, g := range p.Groups {
		total += g.Weight
	}
	if n <= 0 || total <= 0 {
		return counts
	}
	type rem struct {
		idx  int
		frac float64
	}
	rems := make([]rem, len(p.Groups))
	assigned := 0
	for i, g := range p.Groups {
		exact := float64(n) * g.Weight / total
		counts[i] = int(math.Floor(exact + 1e-9))
		rems[i] = rem{i, exact - float64(counts[i])}
		assigned += counts[i]
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for k := 0; assigned < n; k++ {
		counts[rems[k%len(rems)].idx]++
		assigned++
	}
	return counts
}

// BuildSpecs creates the deterministic agent list for n agents. The same
// (profile, seed, n) always yields the same hostnames, site ids and UIDs;
// UIDs depend only on (seed, group, index), so growing n keeps existing
// agents unchanged.
func (p *Profile) BuildSpecs(n int, seed int64) []AgentSpec {
	counts := p.Allocate(n)
	specs := make([]AgentSpec, 0, n)
	for gi, g := range p.Groups {
		version, _ := normalizeVersion(g.Version)
		fixed := map[string]bool{}
		for _, f := range g.FixedSiteIDs {
			fixed[f] = true
		}
		siteNum := 0
		for i := 1; i <= counts[gi]; i++ {
			var site string
			if i <= len(g.FixedSiteIDs) {
				site = g.FixedSiteIDs[i-1]
			} else if v, _ := countVerbs(g.SiteIDPattern); v == 0 {
				site = formatPattern(g.SiteIDPattern, 0) // constant, e.g. "HQ1"
			} else {
				for { // next generated id that does not collide with a fixed one
					siteNum++
					site = formatPattern(g.SiteIDPattern, siteNum)
					if !fixed[site] {
						break
					}
				}
			}
			attrs := make(map[string]string, len(g.Attributes))
			for k, v := range g.Attributes {
				attrs[k] = v
			}
			base := fmt.Sprintf("%d/%s/%d", seed, g.Name, i)
			specs = append(specs, AgentSpec{
				Group: g.Name, Index: i,
				Hostname: formatPattern(g.HostnamePattern, i), SiteID: site,
				OS: g.OS, Arch: g.Arch, Version: version, WithUpdater: g.Updater,
				Attributes:    attrs,
				CollectorUID:  uuid.NewSHA1(uidNamespace, []byte(base)),
				UpdaterUID:    uuid.NewSHA1(uidNamespace, []byte(base+"/updater")),
				OSDescription: osDescription(g.OS),
			})
		}
	}
	return specs
}

func osDescription(os string) string {
	switch os {
	case "windows":
		return "Microsoft Windows 11 IoT Enterprise LTSC (10.0.26100)"
	case "linux":
		return "Debian GNU/Linux 12 (bookworm)"
	case "darwin":
		return "macOS 15.6"
	default:
		return os
	}
}
