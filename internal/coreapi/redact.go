// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package coreapi

import (
	"bytes"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// redacted replaces secret-bearing values in effective configs.
const redacted = "[REDACTED]"

// secretKeyRE matches keys whose values are credentials. Keys that merely
// point to a credential (…_file, …_path, …_url) are not values themselves.
var (
	secretKeyRE  = regexp.MustCompile(`(?i)(pass(word|wd)?|secret|token|api[_-]?key|apikey|authorization|bearer|private[_-]?key|credential|signing[_-]?key|cookie|dsn|connection[_-]?string|sas[_-]?)`)
	pointerKeyRE = regexp.MustCompile(`(?i)(_?file|_?path|_?url|_?uri|_?endpoint|_?type|_?name|_?enabled|_?ttl|_?expiry)$`)
	headerKeyRE  = regexp.MustCompile(`(?i)^(headers?|http_headers|request_headers|extra_headers)$`)
	// collectionKeyRE names collections that consist of secrets only (every value below is hidden)
	collectionKeyRE = regexp.MustCompile(`(?i)^(secrets?|credentials?|passwords?|api[_-]?keys?|tokens?)$`)
	userinfoRE      = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s:@]+(:[^/\s@]*)?@`)
	lineSecretRE    = regexp.MustCompile(`(?im)^(\s*-?\s*["']?[A-Za-z0-9_.-]*(?:pass(?:word|wd)?|secret|token|api[_-]?key|apikey|authorization|bearer|private[_-]?key|credential|cookie)[A-Za-z0-9_.-]*["']?\s*[:=]\s*)(\S.*)$`)
	valueSecretRE   = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
)

func isSecretKey(k string) bool {
	return secretKeyRE.MatchString(k) && !pointerKeyRE.MatchString(k)
}

// RedactEffectiveConfig hides secret-bearing values of an effective config
// reported by an agent: values of keys that name credentials (password, token,
// secret, api key, authorization, ...), all values below header maps, bearer
// strings and credentials inside URLs. The structure stays readable; the
// result is meant for display and diagnosis, not for re-applying. It returns
// whether anything was replaced.
func RedactEffectiveConfig(body []byte) ([]byte, bool) {
	var root yaml.Node
	if err := yaml.Unmarshal(body, &root); err != nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return redactText(body)
	}
	changed := false
	redactNode(root.Content[0], false, &changed, 0)
	if !changed {
		return body, false
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return redactText(body)
	}
	_ = enc.Close()
	return buf.Bytes(), true
}

func redactNode(n *yaml.Node, all bool, changed *bool, depth int) {
	if n == nil || depth > 64 {
		return
	}
	switch n.Kind {
	case yaml.AliasNode:
		return
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			switch {
			case all || (isSecretKey(k.Value) && v.Kind == yaml.ScalarNode):
				hide(v, changed)
			case v.Kind != yaml.ScalarNode && isSecretKey(k.Value) && collectionKeyRE.MatchString(k.Value):
				redactNode(v, true, changed, depth+1)
			default:
				// component ids such as "bearertokenauth" are keys of mappings: look inside
				redactNode(v, headerKeyRE.MatchString(k.Value), changed, depth+1)
			}
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			redactNode(c, all, changed, depth+1)
		}
	case yaml.ScalarNode:
		if all {
			hide(n, changed)
			return
		}
		if s := redactString(n.Value); s != n.Value {
			n.Value, n.Style, n.Tag = s, 0, "!!str"
			*changed = true
		}
	}
}

// hide replaces a value by the placeholder (collections as a whole). Values
// that reference the environment or a file of the collector (${env:...}) are
// not secrets themselves and stay visible.
func hide(v *yaml.Node, changed *bool) {
	if v.Kind == yaml.ScalarNode && (v.Value == "" || v.Tag == "!!null" || strings.HasPrefix(v.Value, "${") || v.Value == redacted) {
		return
	}
	if v.Kind == yaml.ScalarNode && (v.Tag == "!!bool") {
		return
	}
	*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: redacted, Line: v.Line, Column: v.Column}
	*changed = true
}

func redactString(s string) string {
	s = userinfoRE.ReplaceAllString(s, "${1}"+redacted+"@")
	s = valueSecretRE.ReplaceAllString(s, "Bearer "+redacted)
	return s
}

// redactText is the fallback for bodies that are not valid YAML or JSON.
func redactText(body []byte) ([]byte, bool) {
	out := lineSecretRE.ReplaceAll(body, []byte("${1}"+redacted))
	out = []byte(redactString(string(out)))
	return out, !bytes.Equal(out, body)
}
