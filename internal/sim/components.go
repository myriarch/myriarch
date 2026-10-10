// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/open-telemetry/opamp-go/protobufs"
)

// ComponentList is the content of components-otelcol-contrib-<ver>.json:
// kind (receivers, processors, exporters, extensions, connectors) -> name ->
// Go module with version.
type ComponentList struct {
	CollectorVersion string                       `json:"collectorVersion"`
	Components       map[string]map[string]Module `json:"components"`
}

// Module describes one component.
type Module struct {
	Module string `json:"module"`
}

// LoadComponentList reads a component list file.
func LoadComponentList(path string) (*ComponentList, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read components: %w", err)
	}
	var c ComponentList
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse components %s: %w", path, err)
	}
	if len(c.Components) == 0 {
		return nil, fmt.Errorf("components %s: no components", path)
	}
	return &c, nil
}

// Count returns the number of components.
func (c *ComponentList) Count() int {
	n := 0
	for _, m := range c.Components {
		n += len(m)
	}
	return n
}

// componentCache builds and shares AvailableComponents per collector version
// (the messages are immutable and shared by all agents of a version).
type componentCache struct {
	list *ComponentList
	mu   sync.Mutex
	by   map[string]*protobufs.AvailableComponents
}

func newComponentCache(list *ComponentList) *componentCache {
	return &componentCache{list: list, by: map[string]*protobufs.AvailableComponents{}}
}

// forVersion returns the (shared, read-only) component set for a collector
// version; module versions are rewritten to that version.
func (c *componentCache) forVersion(version string) *protobufs.AvailableComponents {
	if c == nil || c.list == nil {
		return &protobufs.AvailableComponents{Hash: sha256Bytes([]byte("none"))}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ac, ok := c.by[version]; ok {
		return ac
	}
	ac := buildAvailableComponents(c.list, version)
	c.by[version] = ac
	return ac
}

func buildAvailableComponents(list *ComponentList, version string) *protobufs.AvailableComponents {
	ac := &protobufs.AvailableComponents{Components: map[string]*protobufs.ComponentDetails{}}
	// canonical form for the hash: kind -> name -> module, keys sorted by encoding/json
	canon := map[string]map[string]string{}
	for kind, comps := range list.Components {
		sub := make(map[string]*protobufs.ComponentDetails, len(comps))
		cm := make(map[string]string, len(comps))
		for name, m := range comps {
			mod := rewriteModuleVersion(m.Module, list.CollectorVersion, version)
			sub[name] = &protobufs.ComponentDetails{Metadata: []*protobufs.KeyValue{strAttr("code.namespace", mod)}}
			cm[name] = mod
		}
		ac.Components[kind] = &protobufs.ComponentDetails{SubComponentMap: sub}
		canon[kind] = cm
	}
	data, _ := json.Marshal(canon) // map keys are sorted -> deterministic
	ac.Hash = sha256Bytes(data)
	return ac
}

// rewriteModuleVersion replaces the trailing " v<from>" with " v<to>".
func rewriteModuleVersion(module, from, to string) string {
	suffix := " v" + from
	if from != "" && strings.HasSuffix(module, suffix) {
		return strings.TrimSuffix(module, suffix) + " v" + to
	}
	return module
}

func sha256Bytes(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// sortedKeys returns the sorted keys of a map (test helper / stable output).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func strAttr(k, v string) *protobufs.KeyValue {
	return &protobufs.KeyValue{Key: k, Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: v}}}
}
