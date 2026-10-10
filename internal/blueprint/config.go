// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/domain"
)

// ValidateConfig checks a rendered collector config: YAML syntax, top-level
// keys, component ids "type[/name]", pipeline references, extension
// references, deprecated aliases (warning), and - if components is non-nil -
// that every component type is available in the component set (the set may
// contain current names and deprecated aliases; resolve aliases in both
// directions before reporting unknown_component).
func (r *Renderer) ValidateConfig(body []byte, components *domain.ComponentSet) domain.Issues {
	var tree any
	if err := yaml.Unmarshal(body, &tree); err != nil {
		return domain.Issues{{Severity: domain.SeverityError, Code: "yaml_syntax", Message: "config is not valid YAML: " + err.Error()}}
	}
	if tree == nil {
		tree = map[string]any{}
	}
	m, ok := asMap(tree)
	if !ok {
		return domain.Issues{{Severity: domain.SeverityError, Code: "config_invalid_toplevel", Message: "config must be a YAML mapping at the top level"}}
	}
	return r.validateTree(normalizeTree(m).(map[string]any), components)
}

var componentKinds = []string{"receivers", "processors", "exporters", "connectors", "extensions"}

var (
	componentTypeRE = regexp.MustCompile(`^[a-zA-Z][0-9a-zA-Z_]{0,62}$`)
	componentNameRE = regexp.MustCompile(`^[^\s\p{C}]+$`)
	pipelineSignals = map[string]bool{"traces": true, "metrics": true, "logs": true, "profiles": true}
	envSchemeRE     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*$`)
)

var knownSchemes = map[string]bool{"env": true, "file": true, "yaml": true, "http": true, "https": true}

// splitComponentID splits "type/name".
func splitComponentID(id string) (typ, name string, hasName bool) {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		return id[:i], id[i+1:], true
	}
	return id, "", false
}

func (r *Renderer) validateTree(tree map[string]any, components *domain.ComponentSet) domain.Issues {
	issues := domain.Issues{}
	add := func(sev domain.IssueSeverity, code, path, msg string) {
		issues = append(issues, domain.Issue{Severity: sev, Code: code, Message: msg, Path: path})
	}

	for _, k := range sortedKeys(tree) {
		switch k {
		case "receivers", "processors", "exporters", "connectors", "extensions", "service":
		default:
			add(domain.SeverityError, "config_invalid_toplevel", k, fmt.Sprintf("unknown top-level key %q (allowed: receivers, processors, exporters, connectors, extensions, service)", k))
		}
	}

	defined := map[string]map[string]bool{}
	for _, kind := range componentKinds {
		defined[kind] = map[string]bool{}
		raw, ok := tree[kind]
		if !ok || raw == nil {
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			add(domain.SeverityError, "config_invalid_toplevel", kind, fmt.Sprintf("%s must be a mapping of component ids", kind))
			continue
		}
		for _, id := range sortedKeys(m) {
			p := kind + pathSeparator + id
			typ, name, hasName := splitComponentID(id)
			if !componentTypeRE.MatchString(typ) || (hasName && !componentNameRE.MatchString(name)) {
				add(domain.SeverityError, "invalid_component_id", p, fmt.Sprintf("invalid component id %q (expected type[/name], type matches [a-zA-Z][0-9a-zA-Z_]*)", id))
				continue
			}
			if v := m[id]; v != nil {
				if _, ok := v.(map[string]any); !ok {
					add(domain.SeverityError, "invalid_component_id", p, fmt.Sprintf("configuration of %s must be a mapping or null", id))
				}
			}
			defined[kind][id] = true
			if cur, ok := r.aliases[kind][typ]; ok {
				add(domain.SeverityWarning, "deprecated_component", p, fmt.Sprintf("component type %q is a deprecated alias of %q", typ, cur))
			}
		}
	}

	used := map[string]map[string]bool{}
	for _, kind := range componentKinds {
		used[kind] = map[string]bool{}
	}

	// service section.
	if raw, ok := tree["service"]; ok && raw != nil {
		svc, ok := raw.(map[string]any)
		if !ok {
			add(domain.SeverityError, "config_invalid_toplevel", "service", "service must be a mapping")
		} else {
			r.validateService(svc, defined, used, add)
		}
	}

	// unused components.
	for _, kind := range componentKinds {
		for _, id := range sortedKeys(defined[kind]) {
			if !used[kind][id] {
				add(domain.SeverityWarning, "unused_component", kind+pathSeparator+id, fmt.Sprintf("%s %q is defined but not used in any pipeline or in service.extensions", strings.TrimSuffix(kind, "s"), id))
			}
		}
	}

	// ${...} syntax.
	r.checkEnvSyntax(tree, "", add)

	// component availability.
	if components != nil {
		r.checkComponents(defined, components, add)
	}
	return issues
}

func (r *Renderer) validateService(svc map[string]any, defined, used map[string]map[string]bool, add func(domain.IssueSeverity, string, string, string)) {
	if raw, ok := svc["extensions"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			add(domain.SeverityError, "invalid_pipeline", "service::extensions", "service.extensions must be a list")
		} else {
			for _, e := range list {
				id, ok := e.(string)
				if !ok {
					add(domain.SeverityError, "invalid_component_id", "service::extensions", "service.extensions entries must be strings")
					continue
				}
				if !defined["extensions"][id] {
					add(domain.SeverityError, "undefined_component", "service::extensions", fmt.Sprintf("extension %q is referenced in service.extensions but not defined", id))
					continue
				}
				used["extensions"][id] = true
			}
		}
	}
	raw, ok := svc["pipelines"]
	if !ok || raw == nil {
		add(domain.SeverityError, "invalid_pipeline", "service::pipelines", "service.pipelines must define at least one pipeline")
		return
	}
	pipes, ok := raw.(map[string]any)
	if !ok {
		add(domain.SeverityError, "invalid_pipeline", "service::pipelines", "service.pipelines must be a mapping")
		return
	}
	if len(pipes) == 0 {
		add(domain.SeverityError, "invalid_pipeline", "service::pipelines", "service.pipelines must define at least one pipeline")
	}
	for _, pid := range sortedKeys(pipes) {
		p := "service::pipelines::" + pid
		signal, name, hasName := splitComponentID(pid)
		if !pipelineSignals[signal] || (hasName && !componentNameRE.MatchString(name)) {
			add(domain.SeverityError, "invalid_pipeline", p, fmt.Sprintf("invalid pipeline id %q (expected traces|metrics|logs|profiles[/name])", pid))
			continue
		}
		pm, ok := pipes[pid].(map[string]any)
		if !ok {
			add(domain.SeverityError, "invalid_pipeline", p, fmt.Sprintf("pipeline %q must be a mapping with receivers, processors and exporters", pid))
			continue
		}
		for _, k := range sortedKeys(pm) {
			if k != "receivers" && k != "processors" && k != "exporters" {
				add(domain.SeverityError, "invalid_pipeline", p+pathSeparator+k, fmt.Sprintf("pipeline %q has unknown key %q", pid, k))
			}
		}
		for _, sect := range []string{"receivers", "processors", "exporters"} {
			var list []any
			if v, ok := pm[sect]; ok && v != nil {
				l, ok := v.([]any)
				if !ok {
					add(domain.SeverityError, "invalid_pipeline", p+pathSeparator+sect, fmt.Sprintf("pipeline %q: %s must be a list", pid, sect))
					continue
				}
				list = l
			}
			if len(list) == 0 && sect != "processors" {
				add(domain.SeverityError, "invalid_pipeline", p+pathSeparator+sect, fmt.Sprintf("pipeline %q needs at least one %s entry", pid, strings.TrimSuffix(sect, "s")))
				continue
			}
			seen := map[string]bool{}
			for _, e := range list {
				id, ok := e.(string)
				if !ok {
					add(domain.SeverityError, "invalid_component_id", p+pathSeparator+sect, fmt.Sprintf("pipeline %q: %s entries must be strings", pid, sect))
					continue
				}
				if seen[id] {
					add(domain.SeverityError, "invalid_pipeline", p+pathSeparator+sect, fmt.Sprintf("pipeline %q references %s %q more than once", pid, strings.TrimSuffix(sect, "s"), id))
					continue
				}
				seen[id] = true
				switch {
				case sect == "processors":
					if !defined["processors"][id] {
						add(domain.SeverityError, "undefined_component", p+pathSeparator+sect, fmt.Sprintf("processor %q is referenced in pipeline %q but not defined", id, pid))
						continue
					}
					used["processors"][id] = true
				default:
					// receivers and exporters may be connectors.
					switch {
					case defined[sect][id]:
						used[sect][id] = true
					case defined["connectors"][id]:
						used["connectors"][id] = true
					default:
						add(domain.SeverityError, "undefined_component", p+pathSeparator+sect, fmt.Sprintf("%s %q is referenced in pipeline %q but not defined", strings.TrimSuffix(sect, "s"), id, pid))
					}
				}
			}
		}
	}
}

// checkEnvSyntax warns about unbalanced "${" and unknown providers.
func (r *Renderer) checkEnvSyntax(v any, path string, add func(domain.IssueSeverity, string, string, string)) {
	switch x := v.(type) {
	case string:
		for _, msg := range envProblems(x) {
			add(domain.SeverityWarning, "env_syntax", path, msg)
		}
	case map[string]any:
		for _, k := range sortedKeys(x) {
			r.checkEnvSyntax(x[k], joinPath(path, k), add)
		}
	case []any:
		for i, e := range x {
			r.checkEnvSyntax(e, fmt.Sprintf("%s[%d]", path, i), add)
		}
	}
}

func envProblems(s string) []string {
	if !strings.Contains(s, "${") {
		return nil
	}
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' { // $$ escape
			i++
			continue
		}
		if i+1 >= len(s) || s[i+1] != '{' {
			continue
		}
		end := strings.IndexByte(s[i+2:], '}')
		if end < 0 {
			out = append(out, fmt.Sprintf("unbalanced \"${\" in %q", truncate(s, 60)))
			break
		}
		inner := s[i+2 : i+2+end]
		if c := strings.IndexByte(inner, ':'); c > 0 {
			scheme := inner[:c]
			if envSchemeRE.MatchString(scheme) && !knownSchemes[scheme] {
				out = append(out, fmt.Sprintf("unknown provider scheme %q in \"${%s}\" (known: env, file, yaml, http, https)", scheme, truncate(inner, 40)))
			}
		}
		i += 2 + end
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// checkComponents verifies that all defined component types are available in
// the set, resolving aliases in both directions.
func (r *Renderer) checkComponents(defined map[string]map[string]bool, set *domain.ComponentSet, add func(domain.IssueSeverity, string, string, string)) {
	for _, kind := range componentKinds {
		avail, ok := set.Components[kind]
		if !ok {
			continue // the agent reported no data for this kind: cannot judge
		}
		for _, id := range sortedKeys(defined[kind]) {
			typ, _, _ := splitComponentID(id)
			if r.componentAvailable(kind, typ, avail) {
				continue
			}
			add(domain.SeverityError, "unknown_component", kind+pathSeparator+id,
				fmt.Sprintf("%s type %q is not available in this collector build%s", strings.TrimSuffix(kind, "s"), typ, versionHint(set)))
		}
	}
}

func versionHint(set *domain.ComponentSet) string { return "" }

func (r *Renderer) componentAvailable(kind, typ string, avail map[string]domain.ComponentDetails) bool {
	if _, ok := avail[typ]; ok {
		return true
	}
	if cur, ok := r.aliases[kind][typ]; ok {
		if _, ok := avail[cur]; ok {
			return true
		}
	}
	for _, old := range r.reverse[kind][typ] {
		if _, ok := avail[old]; ok {
			return true
		}
	}
	return false
}

// KnownAliases returns a copy of the alias table (kind -> old -> new).
func (r *Renderer) KnownAliases() map[string]map[string]string {
	out := make(map[string]map[string]string, len(r.aliases))
	for k, m := range r.aliases {
		c := make(map[string]string, len(m))
		for a, b := range m {
			c[a] = b
		}
		out[k] = c
	}
	return out
}

// sortIssues orders issues deterministically (file, line, severity, code, ...).
func sortIssues(is domain.Issues) {
	sort.SliceStable(is, func(i, j int) bool {
		a, b := is[i], is[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Object != b.Object {
			return a.Object < b.Object
		}
		if a.Severity != b.Severity {
			return a.Severity == domain.SeverityError
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Message < b.Message
	})
}
