// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	mixedFleetPath = "../../examples/sim/mixed-fleet.yaml"
	e2ePath        = "../../examples/sim/e2e.yaml"
)

func groupNames(p *Profile) []string {
	names := []string{}
	for _, g := range p.Groups {
		names = append(names, g.Name)
	}
	return names
}

func TestMixedFleetProfileParses(t *testing.T) {
	p, err := LoadProfile(mixedFleetPath)
	require.NoError(t, err)
	assert.Equal(t, int64(42), p.Seed)
	require.Len(t, p.Groups, 4)
	assert.Equal(t, []string{"linux-edge", "linux-datacenter", "windows-edge", "k8s-gateway"}, groupNames(p))
	assert.True(t, p.Groups[0].Updater)
	assert.False(t, p.Groups[3].Updater)
	assert.Equal(t, "edge", p.Groups[0].Attributes["site.type"])
	assert.Equal(t, "Europe/Amsterdam", p.Groups[0].Attributes["site.timezone"])
	assert.Equal(t, "cloud", p.Groups[3].Attributes["site.type"])

	b := p.Behavior
	assert.Equal(t, 100.0, b.ConnectRate)
	assert.Equal(t, 30*time.Second, b.HeartbeatInterval.D())
	assert.Equal(t, DurRange{Min: time.Second, Max: 5 * time.Second}, b.ConfigApplyDelay)
	assert.Equal(t, DurRange{Min: 3 * time.Second, Max: 3 * time.Second}, b.RestartDuration)
	assert.Equal(t, DurRange{Min: 10 * time.Second, Max: 40 * time.Second}, b.UpdaterInstallDuration)
	assert.Equal(t, "simulate-config-failure", b.FailConfigsContaining)
	assert.Equal(t, "offered", b.VersionAfterUpdate)

	list, err := LoadComponentList(p.ComponentsPath())
	require.NoError(t, err)
	assert.Equal(t, "0.162.0", list.CollectorVersion)
	assert.Equal(t, 242, list.Count())
}

func TestMixedFleetGroupDistributionIsExact(t *testing.T) {
	p, err := LoadProfile(mixedFleetPath)
	require.NoError(t, err)
	assert.Equal(t, []int{400, 200, 300, 100}, p.Allocate(1000))
	for _, n := range []int{1, 2, 3, 7, 10, 99, 333, 999, 1001, 5000, 12345} {
		sum := 0
		for _, c := range p.Allocate(n) {
			assert.GreaterOrEqual(t, c, 0)
			sum += c
		}
		assert.Equal(t, n, sum, "n=%d", n)
	}
	assert.Equal(t, []int{0, 0, 0, 0}, p.Allocate(0))
	specs := p.BuildSpecs(1000, p.Seed)
	assert.Len(t, specs, 1000)
	updaters := 0
	for _, s := range specs {
		if s.WithUpdater {
			updaters++
		}
	}
	assert.Equal(t, 900, updaters, "every group except k8s-gateway runs a myriarch-updater")
}

// The e2e profile mirrors the fleets of the GitOps test data; scripts/e2e.sh
// (E2E_AGENTS=1000) checks exactly these numbers, so they are pinned here.
func TestE2EProfileDistributionAt1000Agents(t *testing.T) {
	p, err := LoadProfile(e2ePath)
	require.NoError(t, err)
	assert.Equal(t, []string{"edge-nl", "edge-be", "office", "datacenter", "k8s-gateway"}, groupNames(p))
	counts := p.Allocate(1000)
	assert.Equal(t, []int{375, 125, 188, 187, 125}, counts)

	perGroup := map[string]int{}
	updaters := 0
	for _, s := range p.BuildSpecs(1000, p.Seed) {
		perGroup[s.Group]++
		if s.WithUpdater {
			updaters++
		}
	}
	assert.Equal(t, map[string]int{"edge-nl": 375, "edge-be": 125, "office": 188, "datacenter": 187, "k8s-gateway": 125}, perGroup)
	assert.Equal(t, 687, updaters, "edge-nl + edge-be + datacenter run a myriarch-updater")

	// the default 32-agent smoke run keeps the weights as agents
	assert.Equal(t, []int{12, 4, 6, 6, 4}, p.Allocate(32))
}

func TestE2EProfileFixedSitesAndPatterns(t *testing.T) {
	p, err := LoadProfile(e2ePath)
	require.NoError(t, err)
	assert.Equal(t, []string{"1001", "1002", "1003"}, p.Groups[0].FixedSiteIDs)
	a := p.BuildSpecs(32, p.Seed)
	require.Len(t, a, 32)

	// the first edge-nl agents are the pilot sites; generated ids never collide with them
	assert.Equal(t, "1001", a[0].SiteID)
	assert.Equal(t, "1002", a[1].SiteID)
	assert.Equal(t, "1003", a[2].SiteID)
	assert.Equal(t, "EDGE-NL-0001", a[0].Hostname)
	assert.Equal(t, "0001", a[3].SiteID)
	sites := map[string]int{}
	for _, s := range a[:12] {
		sites[s.SiteID]++
	}
	for id, n := range sites {
		assert.Equal(t, 1, n, "site id %s unique within edge-nl", id)
	}

	// a pattern without a verb is used unchanged
	var office []AgentSpec
	for _, s := range a {
		if s.Group == "office" {
			office = append(office, s)
		}
	}
	require.Len(t, office, 6)
	assert.Equal(t, "OFFICE1", office[0].SiteID)
	assert.Equal(t, "OFFICE1", office[5].SiteID)
	assert.Equal(t, "office-app-001.example.internal", office[0].Hostname)
	assert.Equal(t, "linux", office[0].OS)
	assert.Equal(t, "0.162.0", office[0].Version)
	assert.Equal(t, "Europe/Amsterdam", office[0].Attributes["site.timezone"])

	// verbs with width and prefix
	var dc []AgentSpec
	for _, s := range a {
		if s.Group == "datacenter" {
			dc = append(dc, s)
		}
	}
	require.Len(t, dc, 6)
	assert.Equal(t, "dc-001", dc[0].Hostname)
	assert.Equal(t, "DC-01", dc[0].SiteID)
}

func TestMixedFleetFirstHostsMatchTheQuickstartCanaryEntries(t *testing.T) {
	p, err := LoadProfile(mixedFleetPath)
	require.NoError(t, err)
	first := map[string]AgentSpec{}
	for _, s := range p.BuildSpecs(1000, p.Seed) {
		if _, ok := first[s.Group]; !ok {
			first[s.Group] = s
		}
	}
	assert.Equal(t, "edge-001", first["linux-edge"].SiteID)
	assert.Equal(t, "linux-edge-01.example.com", first["linux-edge"].Hostname)
	assert.Equal(t, "edge-001", first["windows-edge"].SiteID)
	assert.Equal(t, "WIN-EDGE-01", first["windows-edge"].Hostname)
	assert.Equal(t, "windows", first["windows-edge"].OS)
	assert.Equal(t, "dc-01", first["linux-datacenter"].SiteID)
	assert.Equal(t, "linux-dc-01.example.com", first["linux-datacenter"].Hostname)
	assert.Equal(t, "cloud-01", first["k8s-gateway"].SiteID)
	assert.Equal(t, "otel-gateway-1", first["k8s-gateway"].Hostname)
}

func TestBuildSpecsDeterministicAndUnique(t *testing.T) {
	p, err := LoadProfile(mixedFleetPath)
	require.NoError(t, err)
	a := p.BuildSpecs(1000, 42)
	b := p.BuildSpecs(1000, 42)
	require.Equal(t, a, b, "same (profile, seed, n) -> identical agents")
	c := p.BuildSpecs(1000, 43)
	assert.NotEqual(t, a[0].CollectorUID, c[0].CollectorUID, "different seed -> different identities")

	uids := map[string]bool{}
	hosts := map[string]bool{}
	for _, s := range a {
		assert.False(t, uids[s.CollectorUID.String()], "unique collector uid")
		assert.False(t, uids[s.UpdaterUID.String()], "unique updater uid")
		uids[s.CollectorUID.String()] = true
		uids[s.UpdaterUID.String()] = true
		assert.False(t, hosts[s.Hostname], "unique hostnames: %s", s.Hostname)
		hosts[s.Hostname] = true
	}

	// site ids are unique within the edge groups and constant for the gateway
	linuxEdge := map[string]bool{}
	for _, s := range a {
		switch s.Group {
		case "linux-edge":
			assert.False(t, linuxEdge[s.SiteID], "site id %s unique within linux-edge", s.SiteID)
			linuxEdge[s.SiteID] = true
		case "k8s-gateway":
			assert.Equal(t, "cloud-01", s.SiteID)
		}
	}
	assert.Len(t, linuxEdge, 400)

	// UIDs do not depend on n: growing the fleet keeps existing agents
	big := p.BuildSpecs(5000, 42)
	assert.Equal(t, a[0].CollectorUID, big[0].CollectorUID)
}

func TestFixedSiteIDsNeverCollideWithGeneratedOnes(t *testing.T) {
	p, err := ParseProfile([]byte(`
seed: 1
groups:
  - {name: g, weight: 1, os: linux, arch: amd64, version: 0.1.0, hostnamePattern: "h-%d", siteIdPattern: "%04d", fixedSiteIds: ["0002"]}
`))
	require.NoError(t, err)
	specs := p.BuildSpecs(5, 1)
	seen := map[string]bool{}
	for _, s := range specs {
		assert.False(t, seen[s.SiteID], s.SiteID)
		seen[s.SiteID] = true
	}
	assert.Equal(t, "0002", specs[0].SiteID)
	assert.Equal(t, "0001", specs[1].SiteID)
	assert.Equal(t, "0003", specs[2].SiteID, "0002 is skipped")
}

func TestParseDurRange(t *testing.T) {
	d, err := ParseDurRange("1s-5s")
	require.NoError(t, err)
	assert.Equal(t, DurRange{time.Second, 5 * time.Second}, d)
	assert.Equal(t, time.Second, d.Pick(0))
	assert.Equal(t, 3*time.Second, d.Pick(0.5))
	d, err = ParseDurRange("250ms")
	require.NoError(t, err)
	assert.Equal(t, 250*time.Millisecond, d.Pick(0.9))
	d, err = ParseDurRange("")
	require.NoError(t, err)
	assert.Zero(t, d.Max)
	for _, bad := range []string{"5s-1s", "abc", "1s-x"} {
		_, err := ParseDurRange(bad)
		assert.Error(t, err, bad)
	}
}

func TestPatternHandling(t *testing.T) {
	assert.Equal(t, "EDGE-NL-0007", formatPattern("EDGE-NL-%04d", 7))
	assert.Equal(t, "OFFICE1", formatPattern("OFFICE1", 7))
	assert.Equal(t, "100%", formatPattern("100%%", 7))
	assert.Equal(t, "x7", formatPattern("x%d", 7))
	_, err := countVerbs("a%db%d")
	assert.Error(t, err)
	_, err = countVerbs("bad%")
	assert.Error(t, err)
	n, err := countVerbs("%04d")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestProfileValidation(t *testing.T) {
	good := "groups:\n  - {name: a, weight: 1, os: linux, arch: amd64, version: 1.2.3, hostnamePattern: h, siteIdPattern: s}\n"
	_, err := ParseProfile([]byte(good))
	require.NoError(t, err)
	bad := map[string]string{
		"no groups":         "seed: 1\n",
		"zero weight":       "groups:\n  - {name: a, weight: 0, os: linux, arch: amd64, version: 1.2.3, hostnamePattern: h, siteIdPattern: s}\n",
		"bad version":       "groups:\n  - {name: a, weight: 1, os: linux, arch: amd64, version: x, hostnamePattern: h, siteIdPattern: s}\n",
		"dup name":          "groups:\n  - {name: a, weight: 1, os: linux, arch: amd64, version: 1.2.3, hostnamePattern: h, siteIdPattern: s}\n  - {name: a, weight: 1, os: linux, arch: amd64, version: 1.2.3, hostnamePattern: h, siteIdPattern: s}\n",
		"unknown key":       good + "bogus: 1\n",
		"rate range":        good + "behavior: {configFailureRate: 2}\n",
		"bad version-after": good + "behavior: {versionAfterUpdate: maybe}\n",
		"two verbs":         "groups:\n  - {name: a, weight: 1, os: linux, arch: amd64, version: 1.2.3, hostnamePattern: 'h%d%d', siteIdPattern: s}\n",
		"no os":             "groups:\n  - {name: a, weight: 1, arch: amd64, version: 1.2.3, hostnamePattern: h, siteIdPattern: s}\n",
	}
	for name, y := range bad {
		_, err := ParseProfile([]byte(y))
		assert.Error(t, err, name)
	}
}

func TestAvailableComponentsHashAndVersioning(t *testing.T) {
	p, err := LoadProfile(mixedFleetPath)
	require.NoError(t, err)
	list, err := LoadComponentList(p.ComponentsPath())
	require.NoError(t, err)
	cc := newComponentCache(list)
	a := cc.forVersion("0.162.0")
	b := cc.forVersion("0.162.0")
	assert.Same(t, a, b, "shared per version")
	old := cc.forVersion("0.161.0")
	assert.NotEqual(t, a.Hash, old.Hash, "module versions are part of the hash")
	assert.Len(t, a.Hash, 32)
	assert.ElementsMatch(t, []string{"receivers", "processors", "exporters", "extensions", "connectors"}, sortedKeys(a.Components))
	r := a.Components["receivers"].SubComponentMap["otlp_grpc"]
	if r == nil {
		r = a.Components["receivers"].SubComponentMap["otlp"]
	}
	require.NotNil(t, r)
	ns := attrs(r.Metadata)["code.namespace"]
	assert.Contains(t, ns, " v0.162.0")
	ns = attrs(old.Components["receivers"].SubComponentMap[sortedKeys(old.Components["receivers"].SubComponentMap)[0]].Metadata)["code.namespace"]
	assert.Contains(t, ns, " v0.161.0")
	// deterministic
	assert.Equal(t, a.Hash, buildAvailableComponents(list, "0.162.0").Hash)
}

func TestAllExampleProfilesParse(t *testing.T) {
	for _, f := range []string{"mixed-fleet.yaml", "e2e.yaml", "small.yaml", "chaos.yaml"} {
		p, err := LoadProfile("../../examples/sim/" + f)
		require.NoError(t, err, f)
		list, err := LoadComponentList(p.ComponentsPath())
		require.NoError(t, err, f)
		assert.Equal(t, 242, list.Count())
		for _, n := range []int{20, 1000} {
			sum := 0
			for _, c := range p.Allocate(n) {
				sum += c
			}
			assert.Equal(t, n, sum, f)
		}
	}
}
