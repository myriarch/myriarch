// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/domain"
)

// ParseSamples reads sample agents from "<root>/samples/agents/*.yaml"
// (used by validation and myrctl render), sorted by name.
func ParseSamples(fsys fs.FS, root string) ([]blueprint.Sample, domain.Issues) {
	return ParseSamplesLimited(fsys, root, DefaultLimits)
}

// ParseSamplesLimited is ParseSamples with explicit limits (file size is
// checked before reading; symbolic links and special files are errors).
func ParseSamplesLimited(fsys fs.FS, root string, lim Limits) ([]blueprint.Sample, domain.Issues) {
	lim = lim.withDefaults()
	root = cleanRoot(root)
	dir := path.Join(root, "samples", "agents")
	issues := domain.Issues{}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []blueprint.Sample{}, issues
		}
		return nil, append(issues, domain.Issue{Severity: domain.SeverityError, Code: "yaml_syntax", Message: fmt.Sprintf("cannot read %s: %v", dir, err), File: dir})
	}
	samples := []blueprint.Sample{}
	seen := map[string]string{}
	for _, e := range entries {
		ext := strings.ToLower(path.Ext(e.Name()))
		if e.IsDir() || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		file := path.Join(dir, e.Name())
		if notRegular(e) {
			issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: "not_regular_file", Message: "symbolic links and special files are not allowed in the repository", File: file})
			continue
		}
		if info, err := e.Info(); err == nil && info.Size() > lim.MaxFileBytes {
			issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: "file_too_large", Message: fmt.Sprintf("file is larger than %d bytes", lim.MaxFileBytes), File: file})
			continue
		}
		data, err := readLimited(fsys, file, lim.MaxFileBytes)
		if err != nil {
			issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: "yaml_syntax", Message: fmt.Sprintf("cannot read file: %v", err), File: file})
			continue
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var doc yaml.Node
			err := dec.Decode(&doc)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: "yaml_syntax", Message: cleanYAMLError(err), File: file, Line: lineFromError(err)})
				break
			}
			if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
				continue
			}
			body := doc.Content[0]
			if body.Kind == yaml.ScalarNode && body.Tag == "!!null" {
				continue
			}
			sm, ok := parseSample(file, body, &issues)
			if !ok {
				continue
			}
			if prev, dup := seen[sm.Name]; dup {
				issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: "duplicate_object", Message: fmt.Sprintf("sample agent %q is defined more than once (first at %s)", sm.Name, prev), File: file, Line: sm.Line, Object: "Sample/" + sm.Name})
				continue
			}
			seen[sm.Name] = fmt.Sprintf("%s:%d", file, sm.Line)
			samples = append(samples, sm)
		}
	}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].Name < samples[j].Name })
	return samples, issues
}

func parseSample(file string, body *yaml.Node, issues *domain.Issues) (blueprint.Sample, bool) {
	add := func(code, msg string, line int, fieldPath string) {
		*issues = append(*issues, domain.Issue{Severity: domain.SeverityError, Code: code, Message: msg, File: file, Line: line, Path: fieldPath})
	}
	sm := blueprint.Sample{File: file, Attributes: map[string]string{}, Labels: map[string]string{}}
	if body.Kind != yaml.MappingNode {
		add("missing_field", "sample agent must be a mapping with name, attributes and optional labels", body.Line, "")
		return sm, false
	}
	ok := true
	for _, e := range mappingEntries(body) {
		switch e.key.Value {
		case "name":
			v := derefAlias(e.val)
			if v.Kind != yaml.ScalarNode || strings.TrimSpace(v.Value) == "" {
				add("missing_field", "sample agent name must be a non-empty string", e.key.Line, "name")
				ok = false
				continue
			}
			sm.Name, sm.Line = v.Value, e.key.Line
		case "attributes":
			ok = readStringMap(e.val, sm.Attributes, add, "attributes") && ok
		case "labels":
			ok = readStringMap(e.val, sm.Labels, add, "labels") && ok
		default:
			add("unknown_field", fmt.Sprintf("unknown field %q in sample agent (allowed: name, attributes, labels)%s", e.key.Value, suggest(e.key.Value, []string{"name", "attributes", "labels"})), e.key.Line, e.key.Value)
			ok = false
		}
	}
	if sm.Name == "" {
		if ok {
			add("missing_field", "sample agent has no name", body.Line, "name")
		}
		return sm, false
	}
	return sm, ok
}

// readStringMap reads a mapping of scalars as raw text (so "0123" and true
// keep their YAML spelling).
func readStringMap(n *yaml.Node, out map[string]string, add func(code, msg string, line int, fieldPath string), fieldPath string) bool {
	n = derefAlias(n)
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return true
	}
	if n.Kind != yaml.MappingNode {
		add("invalid_field", fieldPath+" must be a mapping of strings", n.Line, fieldPath)
		return false
	}
	ok := true
	for _, e := range mappingEntries(n) {
		v := derefAlias(e.val)
		if v.Kind != yaml.ScalarNode {
			add("invalid_field", fmt.Sprintf("%s.%s must be a scalar value", fieldPath, e.key.Value), e.key.Line, fieldPath+"::"+e.key.Value)
			ok = false
			continue
		}
		if v.Tag == "!!null" {
			continue
		}
		out[e.key.Value] = v.Value
	}
	return ok
}
