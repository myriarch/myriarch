// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

func mapFS(files map[string]string) fstest.MapFS {
	fs := fstest.MapFS{}
	for k, v := range files {
		fs[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return fs
}

const hdr = "apiVersion: myriarch.dev/v1alpha1\n"

func codes(is domain.Issues) []string {
	out := []string{}
	for _, i := range is {
		out = append(out, i.Code)
	}
	return out
}

func TestParseExampleRepo(t *testing.T) {
	fsys := osDirFS(t, "../testdata/gitops")
	snap, issues := Parse(fsys, ".", "abc123")
	require.Empty(t, issues)
	assert.Equal(t, "abc123", snap.Commit)
	assert.Len(t, snap.Fleets, 5)
	assert.Len(t, snap.Blueprints, 5)
	assert.Len(t, snap.RolloutPolicies, 3)
	assert.Len(t, snap.Executors, 2)
	assert.Len(t, snap.Inventories, 1)
	// sorted by name
	assert.Equal(t, "datacenter", snap.Fleets[0].Name)
	// source refs
	var edgeNL domain.Fleet
	for _, f := range snap.Fleets {
		if f.Name == "edge-nl" {
			edgeNL = f
		}
	}
	assert.Equal(t, "fleets/edge-nl.yaml", edgeNL.Source.Path)
	assert.Equal(t, "abc123", edgeNL.Source.Commit)
	assert.Len(t, edgeNL.Source.ContentHash, 32)
	assert.Equal(t, "edge-ops", edgeNL.Labels["team"])
	assert.Equal(t, 100, edgeNL.Priority)
	assert.Equal(t, "0.162.0", edgeNL.Collector.Version)
	assert.Equal(t, domain.ExecutorUpdater, edgeNL.Update.Executor)
	// inventory: "1001" stays a string, labels are strings
	inv := snap.Inventories[0]
	assert.Equal(t, "1001", inv.Entries[0].Match["site.id"])
	assert.Equal(t, "true", inv.Entries[0].Labels["pilot"])
	assert.Equal(t, false, inv.Entries[2].Parameters["collectEventLog"])
	// ints stay ints in free-form areas
	for _, b := range snap.Blueprints {
		if b.Name == "base" {
			for _, p := range b.Parameters {
				if p.Name == "memoryLimitPercent" {
					assert.Equal(t, 75, p.Default)
					assert.NotNil(t, p.Min)
				}
			}
		}
	}
	// policies: durations and windows
	for _, p := range snap.RolloutPolicies {
		if p.Name == "edge-careful" {
			assert.Equal(t, "10m0s", p.HealthWait.String())
			assert.Len(t, p.MaintenanceWindows, 2)
			assert.Equal(t, []string{"Sun"}, p.MaintenanceWindows[1].Days)
			assert.NotNil(t, p.Waves[0].Selector)
			assert.Equal(t, 10, p.Waves[1].Percent)
		}
	}
}

func TestParseStableContentHash(t *testing.T) {
	files := map[string]string{"b.yaml": hdr + "kind: Blueprint\nmetadata: {name: b}\nspec:\n  config: {a: 1}\n"}
	s1, i1 := Parse(mapFS(files), ".", "c1")
	require.Empty(t, i1)
	s2, _ := Parse(mapFS(files), ".", "c2")
	assert.Equal(t, s1.Blueprints[0].Source.ContentHash, s2.Blueprints[0].Source.ContentHash, "hash depends on the spec only, not on the commit")
	files["b.yaml"] = hdr + "kind: Blueprint\nmetadata: {name: b}\nspec:\n  config: {a: 2}\n"
	s3, _ := Parse(mapFS(files), ".", "c1")
	assert.NotEqual(t, s1.Blueprints[0].Source.ContentHash, s3.Blueprints[0].Source.ContentHash)
}

func TestParseSkipsSamplesAndDotDirs(t *testing.T) {
	fs := mapFS(map[string]string{
		"fleets/a.yaml":         hdr + "kind: Fleet\nmetadata: {name: a}\nspec: {blueprint: b}\n",
		"samples/agents/x.yaml": "name: x\nattributes: {a: b}\n",
		".git/config.yaml":      "garbage: [",
		".hidden/fleet.yaml":    hdr + "kind: Fleet\nmetadata: {name: hidden}\nspec: {blueprint: b}\n",
		"deep/nested/dir/b.yml": hdr + "kind: Blueprint\nmetadata: {name: b}\nspec: {}\n",
		"notes.txt":             "not yaml",
		"samples.yaml":          hdr + "kind: Fleet\nmetadata: {name: samples-file}\nspec: {blueprint: b}\n",
	})
	snap, issues := Parse(fs, ".", "c")
	require.Empty(t, issues)
	var names []string
	for _, f := range snap.Fleets {
		names = append(names, f.Name)
	}
	assert.Equal(t, []string{"a", "samples-file"}, names)
	assert.Len(t, snap.Blueprints, 1)
}

func TestParseSubdirectoryRoot(t *testing.T) {
	fs := mapFS(map[string]string{
		"outside.yaml":                 hdr + "kind: Fleet\nmetadata: {name: outside}\nspec: {blueprint: b}\n",
		"config/fleets/a.yaml":         hdr + "kind: Fleet\nmetadata: {name: a}\nspec: {blueprint: b}\n",
		"config/samples/agents/s.yaml": "name: s\nattributes: {k: v}\n",
	})
	snap, issues := Parse(fs, "config", "c")
	require.Empty(t, issues)
	require.Len(t, snap.Fleets, 1)
	assert.Equal(t, "config/fleets/a.yaml", snap.Fleets[0].Source.Path, "paths are repository relative")
	samples, sissues := ParseSamples(fs, "config")
	require.Empty(t, sissues)
	require.Len(t, samples, 1)
	assert.Equal(t, "config/samples/agents/s.yaml", samples[0].File)
}

func TestParseMultiDocumentAndLines(t *testing.T) {
	fs := mapFS(map[string]string{"all.yaml": hdr + `kind: Blueprint
metadata:
  name: one
spec: {}
---
# second document
apiVersion: myriarch.dev/v1alpha1
kind: Fleet
metadata:
  name: two
spec:
  blueprint: one
  priorty: 3
---
`})
	snap, issues := Parse(fs, ".", "c")
	require.Len(t, issues, 1)
	assert.Equal(t, "unknown_field", issues[0].Code)
	assert.Equal(t, "all.yaml", issues[0].File)
	assert.Equal(t, 14, issues[0].Line)
	assert.Contains(t, issues[0].Message, `did you mean "priority"`)
	assert.Len(t, snap.Blueprints, 1)
	assert.Len(t, snap.Fleets, 1, "the object is kept, strictness only reports")
}

func TestParseEnvelopeErrors(t *testing.T) {
	cases := []struct {
		name, yaml, code string
		line             int
	}{
		{"missing apiVersion", "kind: Fleet\nmetadata: {name: a}\nspec: {}\n", "unknown_api_version", 1},
		{"wrong apiVersion", "apiVersion: v1\nkind: Fleet\nmetadata: {name: a}\nspec: {}\n", "unknown_api_version", 1},
		{"unknown kind", hdr + "kind: Thing\nmetadata: {name: a}\nspec: {}\n", "unknown_kind", 2},
		{"missing kind", hdr + "metadata: {name: a}\nspec: {}\n", "unknown_kind", 1},
		{"missing metadata", hdr + "kind: Fleet\nspec: {}\n", "missing_field", 1},
		{"missing name", hdr + "kind: Fleet\nmetadata: {description: x}\nspec: {}\n", "missing_field", 3},
		{"invalid name", hdr + "kind: Fleet\nmetadata: {name: Not_Valid}\nspec: {}\n", "invalid_name", 3},
		{"name too long", hdr + "kind: Fleet\nmetadata: {name: " + string(make([]byte, 0)) + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}\nspec: {}\n", "invalid_name", 3},
		{"missing spec", hdr + "kind: Fleet\nmetadata: {name: a}\n", "missing_field", 1},
		{"unknown top-level", hdr + "kind: Fleet\nmetadata: {name: a}\nspec: {}\nextra: 1\n", "unknown_field", 5},
		{"unknown metadata field", hdr + "kind: Fleet\nmetadata: {name: a, owner: x}\nspec: {}\n", "unknown_field", 3},
		{"not a mapping", "- a\n- b\n", "missing_field", 1},
		{"syntax", "kind: [\n", "yaml_syntax", 0},
		{"type error", hdr + "kind: Fleet\nmetadata: {name: a}\nspec:\n  priority: high\n", "invalid_field", 5},
		{"bad duration", hdr + "kind: RolloutPolicy\nmetadata: {name: a}\nspec:\n  healthWait: soon\n", "invalid_field", 5},
		{"unknown nested", hdr + "kind: RolloutPolicy\nmetadata: {name: a}\nspec:\n  failureThreshold:\n    maxFailed: 1\n    maxfailed: 2\n", "unknown_field", 7},
		{"unknown in list item", hdr + "kind: RolloutPolicy\nmetadata: {name: a}\nspec:\n  waves:\n    - name: w\n      percnt: 5\n", "unknown_field", 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, issues := Parse(mapFS(map[string]string{"f.yaml": tc.yaml}), ".", "c")
			require.NotEmpty(t, issues, tc.name)
			assert.Equal(t, tc.code, issues[0].Code, "%+v", issues)
			assert.Equal(t, "f.yaml", issues[0].File)
			if tc.line != 0 {
				assert.Equal(t, tc.line, issues[0].Line, "%+v", issues[0])
			}
			assert.Equal(t, domain.SeverityError, issues[0].Severity)
		})
	}
}

// spec.bridge.manage (docs/reference/gitops-schema.md, Fleet): the opt-in to
// configure OpAMP Bridge agents.
func TestParseFleetBridgeManage(t *testing.T) {
	fleet := func(block string) string {
		return hdr + "kind: Fleet\nmetadata: {name: f}\nspec:\n  blueprint: b\n" + block
	}
	t.Run("the default is false", func(t *testing.T) {
		snap, issues := Parse(mapFS(map[string]string{"f.yaml": fleet("")}), ".", "c")
		require.Empty(t, issues)
		assert.Equal(t, domain.BridgeSpec{}, snap.Fleets[0].Bridge)
	})
	for name, block := range map[string]string{
		"block style": "  bridge:\n    manage: true\n",
		"flow style":  "  bridge: {manage: true}\n",
	} {
		t.Run("accepted, "+name, func(t *testing.T) {
			snap, issues := Parse(mapFS(map[string]string{"f.yaml": fleet(block)}), ".", "c")
			require.Empty(t, issues)
			assert.True(t, snap.Fleets[0].Bridge.Manage)
		})
	}
	t.Run("explicit false", func(t *testing.T) {
		snap, issues := Parse(mapFS(map[string]string{"f.yaml": fleet("  bridge:\n    manage: false\n")}), ".", "c")
		require.Empty(t, issues)
		assert.False(t, snap.Fleets[0].Bridge.Manage)
	})
	t.Run("an empty block", func(t *testing.T) {
		snap, issues := Parse(mapFS(map[string]string{"f.yaml": fleet("  bridge: {}\n")}), ".", "c")
		require.Empty(t, issues)
		assert.False(t, snap.Fleets[0].Bridge.Manage)
	})
	t.Run("an unknown sub-field is unknown_field", func(t *testing.T) {
		snap, issues := Parse(mapFS(map[string]string{"f.yaml": fleet("  bridge:\n    manag: true\n")}), ".", "c")
		require.Len(t, issues, 1)
		assert.Equal(t, "unknown_field", issues[0].Code)
		assert.Equal(t, domain.SeverityError, issues[0].Severity)
		assert.Equal(t, "f.yaml", issues[0].File)
		assert.Equal(t, 7, issues[0].Line)
		assert.Equal(t, "spec.bridge.manag", issues[0].Path)
		assert.Contains(t, issues[0].Message, `did you mean "manage"`)
		assert.Len(t, snap.Fleets, 1, "the object is kept, strictness only reports")
	})
	t.Run("a wrong type is invalid_field", func(t *testing.T) {
		for name, block := range map[string]string{
			"a string for manage": "  bridge:\n    manage: sometimes\n",
			"a list for manage":   "  bridge:\n    manage: [true]\n",
			"a scalar for bridge": "  bridge: yes please\n",
			"a list for bridge":   "  bridge: [manage]\n",
		} {
			_, issues := Parse(mapFS(map[string]string{"f.yaml": fleet(block)}), ".", "c")
			require.NotEmpty(t, issues, name)
			assert.Equal(t, "invalid_field", issues[0].Code, "%s: %+v", name, issues)
			assert.Equal(t, "f.yaml", issues[0].File, name)
		}
	})
	t.Run("the content hash of a fleet without the block does not change", func(t *testing.T) {
		with, _ := Parse(mapFS(map[string]string{"f.yaml": fleet("  bridge: {manage: false}\n")}), ".", "c")
		without, _ := Parse(mapFS(map[string]string{"f.yaml": fleet("")}), ".", "c")
		assert.Equal(t, without.Fleets[0].Source.ContentHash, with.Fleets[0].Source.ContentHash, "an unset block is not serialized")
		opted, _ := Parse(mapFS(map[string]string{"f.yaml": fleet("  bridge: {manage: true}\n")}), ".", "c")
		assert.NotEqual(t, without.Fleets[0].Source.ContentHash, opted.Fleets[0].Source.ContentHash, "the opt-in is part of the object")
	})
}

func TestParseDuplicateObjects(t *testing.T) {
	fs := mapFS(map[string]string{
		"a.yaml": hdr + "kind: Fleet\nmetadata: {name: x}\nspec: {blueprint: b}\n",
		"b.yaml": hdr + "kind: Fleet\nmetadata: {name: x}\nspec: {blueprint: b}\n---\n" + hdr + "kind: Blueprint\nmetadata: {name: x}\nspec: {}\n",
	})
	snap, issues := Parse(fs, ".", "c")
	require.Len(t, issues, 1)
	assert.Equal(t, "duplicate_object", issues[0].Code)
	assert.Equal(t, "b.yaml", issues[0].File)
	assert.Contains(t, issues[0].Message, "a.yaml:3")
	assert.Len(t, snap.Fleets, 1, "same name in a different kind is fine")
	assert.Len(t, snap.Blueprints, 1)
}

func TestParseFreeFormAreasAreNotChecked(t *testing.T) {
	fs := mapFS(map[string]string{"f.yaml": hdr + `kind: Blueprint
metadata: {name: b}
spec:
  parameters:
    - {name: p, type: map, default: {anything: {goes: [1, 2.5, true, null]}}}
  config:
    receivers:
      foo: {unknown_key_is_fine: 1}
  fragments:
    - name: f
      config: {whatever: [1]}
`})
	snap, issues := Parse(fs, ".", "c")
	require.Empty(t, issues)
	assert.Equal(t, 1, snap.Blueprints[0].Config["receivers"].(map[string]any)["foo"].(map[string]any)["unknown_key_is_fine"])
}

func TestParseAnchorsAndMergeKeys(t *testing.T) {
	fs := mapFS(map[string]string{"f.yaml": hdr + `kind: Blueprint
metadata: {name: b}
spec:
  config:
    exporters:
      a: &common {timeout: 5s, retry: true}
      b:
        <<: *common
        timeout: 10s
`})
	snap, issues := Parse(fs, ".", "c")
	require.Empty(t, issues)
	exp := snap.Blueprints[0].Config["exporters"].(map[string]any)
	assert.Equal(t, map[string]any{"timeout": "10s", "retry": true}, exp["b"])
}

func TestParseUnquotedTemplateAndColonKeys(t *testing.T) {
	fs := mapFS(map[string]string{"f.yaml": hdr + `kind: Blueprint
metadata: {name: b}
spec:
  config:
    receivers:
      a:
        include: [[ .params.paths ]]
        ok: [[1, 2], [3]]
        quoted: "[[ .params.x ]]"
        "bad::key": 1
`})
	_, issues := Parse(fs, ".", "c")
	got := map[string]int{}
	for _, i := range issues {
		got[i.Code] = i.Line
	}
	assert.Equal(t, 8, got["unquoted_template"])
	assert.Equal(t, 11, got["invalid_config_key"])
	assert.Len(t, issues, 2, "[[1, 2], [3]] and quoted templates are fine: %+v", issues)
}

func TestParseExcessiveAliasing(t *testing.T) {
	// "billion laughs": must not hang or explode.
	doc := hdr + "kind: Blueprint\nmetadata: {name: b}\nspec:\n  config:\n    a: &a [x, x, x, x, x, x, x, x, x]\n    b: &b [*a, *a, *a, *a, *a, *a, *a, *a, *a]\n    c: &c [*b, *b, *b, *b, *b, *b, *b, *b, *b]\n    d: &d [*c, *c, *c, *c, *c, *c, *c, *c, *c]\n    e: &e [*d, *d, *d, *d, *d, *d, *d, *d, *d]\n    f: &f [*e, *e, *e, *e, *e, *e, *e, *e, *e]\n    g: [*f, *f, *f, *f, *f, *f, *f, *f, *f]\n"
	_, issues := Parse(mapFS(map[string]string{"f.yaml": doc}), ".", "c")
	assert.NotEmpty(t, issues)
}

func TestParseSamples(t *testing.T) {
	fs := mapFS(map[string]string{
		"samples/agents/b.yaml":    "name: b\nattributes:\n  site.id: 0123\n  site.name: Foo\n  flag: true\nlabels:\n  pilot: \"true\"\n",
		"samples/agents/a.yml":     "name: a\nattributes: {os.type: linux}\n",
		"samples/agents/c.yaml":    "name: c\nattributes: {x: y}\nbogus: 1\n",
		"samples/agents/d.yaml":    "name: a\nattributes: {os.type: linux}\n",
		"samples/agents/e.yaml":    "name: e\nattributes:\n  list: [1, 2]\n",
		"samples/agents/readme.md": "x",
	})
	samples, issues := ParseSamples(fs, ".")
	var names []string
	for _, s := range samples {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"a", "b"}, names, "sorted by name; invalid samples dropped")
	assert.Equal(t, "0123", samples[1].Attributes["site.id"], "scalars keep their text, leading zeros included")
	assert.Equal(t, "true", samples[1].Attributes["flag"])
	assert.Equal(t, "true", samples[1].Labels["pilot"])
	assert.Equal(t, 1, samples[1].Line)
	assert.ElementsMatch(t, []string{"unknown_field", "duplicate_object", "invalid_field"}, codes(issues))

	// no samples dir is fine
	s2, i2 := ParseSamples(mapFS(map[string]string{"a.yaml": "x: 1"}), ".")
	assert.Empty(t, s2)
	assert.Empty(t, i2)
}

func TestLineOfPath(t *testing.T) {
	fs := mapFS(map[string]string{"p.yaml": hdr + `kind: RolloutPolicy
metadata:
  name: p
spec:
  waves:
    - name: a
      percent: 5
    - name: b
      percent: 3
  failureThreshold:
    maxFailed: 1
  maintenanceWindows:
    - start: "10:00"
      end: "11:00"
`})
	pr := ParseDetailed(fs, ".", "c")
	require.Empty(t, pr.Issues)
	pos := pr.Objects["RolloutPolicy/p"]
	require.NotNil(t, pos.Node)
	assert.Equal(t, 1, pos.Line)
	for path, want := range map[string]int{
		"spec.waves[1].percent":          10,
		"spec.waves[0]":                  7,
		"spec.failureThreshold":          11,
		"spec.maintenanceWindows[0].end": 15,
		"metadata.name":                  4,
		"spec.waves[7].percent":          6, // deepest resolvable element: the waves list
		"spec.nothing":                   5,
	} {
		assert.Equal(t, want, lineOfPath(pos.Node, path), path)
	}
}
