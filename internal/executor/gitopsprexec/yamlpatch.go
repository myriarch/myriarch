// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitopsprexec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/domain"
)

var (
	versionRE   = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]*$`)
	attrExprRE  = regexp.MustCompile(`\[\[\s*attr\s+"([^"]+)"\s*\]\]`)
	attrValueRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	segmentRE   = regexp.MustCompile(`^([A-Za-z0-9_-]+)((?:\[[0-9]+\])*)$`)
	indexRE     = regexp.MustCompile(`\[([0-9]+)\]`)
)

// parseSingle parses exactly one YAML document.
func parseSingle(content []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the file is empty")
		}
		return nil, fmt.Errorf("the file is not valid YAML: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("the file holds more than one YAML document")
	}
	if len(doc.Content) != 1 {
		return nil, errors.New("the file is empty")
	}
	return doc.Content[0], nil
}

// findScalar walks the field path ("a.b[0].c") to a scalar node.
func findScalar(root *yaml.Node, field string) (*yaml.Node, error) {
	n := root
	for _, seg := range strings.Split(field, ".") {
		m := segmentRE.FindStringSubmatch(seg)
		if m == nil {
			return nil, fmt.Errorf("field segment %q is invalid", seg)
		}
		if n.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("field %s: %q is not below a mapping", field, m[1])
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i]; k.Kind == yaml.ScalarNode && k.Value == m[1] {
				next = n.Content[i+1]
				break
			}
		}
		if next == nil {
			return nil, fmt.Errorf("field %s: key %q not found", field, m[1])
		}
		n = next
		for _, im := range indexRE.FindAllStringSubmatch(m[2], -1) {
			idx, _ := strconv.Atoi(im[1])
			if n.Kind != yaml.SequenceNode || idx >= len(n.Content) {
				return nil, fmt.Errorf("field %s: index [%d] not found", field, idx)
			}
			n = n.Content[idx]
		}
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return nil, fmt.Errorf("field %s is an alias or has an anchor", field)
	}
	if n.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("field %s is not a scalar", field)
	}
	switch n.Style {
	case 0, yaml.SingleQuotedStyle, yaml.DoubleQuotedStyle:
	default:
		return nil, fmt.Errorf("field %s is a block scalar, only plain and quoted scalars are supported", field)
	}
	if strings.Contains(n.Value, "\n") {
		return nil, fmt.Errorf("field %s is a multi-line scalar", field)
	}
	return n, nil
}

// newValue computes the replacement of old for the version.
func newValue(old, format, version string) (string, error) {
	if format == domain.ValueFormatTag {
		return version, nil
	}
	if strings.Contains(old, "@") {
		return "", errors.New("digest references are not supported")
	}
	base := old
	if c := strings.LastIndex(old, ":"); c > strings.LastIndex(old, "/") {
		base = old[:c]
	}
	if base == "" {
		return "", errors.New("the image reference is empty")
	}
	return base + ":" + version, nil
}

// setImageScalar changes the one scalar at field to the version and leaves
// every other byte of content untouched. changed is false when the scalar
// already holds the value.
func setImageScalar(content []byte, field, format, version string) (out []byte, old string, changed bool, err error) {
	if !versionRE.MatchString(version) {
		return nil, "", false, fmt.Errorf("version %q is not usable as an image tag", version)
	}
	root, err := parseSingle(content)
	if err != nil {
		return nil, "", false, err
	}
	node, err := findScalar(root, field)
	if err != nil {
		return nil, "", false, err
	}
	old = node.Value
	nv, err := newValue(old, format, version)
	if err != nil {
		return nil, old, false, err
	}
	if nv == old {
		return content, old, false, nil
	}

	// byte offset of the line, then Column-1 characters forward
	pos := 0
	for l := 1; l < node.Line; l++ {
		i := bytes.IndexByte(content[pos:], '\n')
		if i < 0 {
			return nil, old, false, errors.New("cannot locate the value in the file")
		}
		pos += i + 1
	}
	for c := 1; c < node.Column; c++ {
		if pos >= len(content) {
			return nil, old, false, errors.New("cannot locate the value in the file")
		}
		_, w := utf8.DecodeRune(content[pos:])
		pos += w
	}
	if node.Style != 0 {
		quote := byte('"')
		if node.Style == yaml.SingleQuotedStyle {
			quote = '\''
		}
		if pos >= len(content) || content[pos] != quote {
			return nil, old, false, errors.New("cannot locate the value in the file")
		}
		pos++
	}
	if !bytes.HasPrefix(content[pos:], []byte(old)) {
		return nil, old, false, errors.New("cannot locate the value in the file")
	}
	out = make([]byte, 0, len(content)+len(nv))
	out = append(out, content[:pos]...)
	out = append(out, nv...)
	out = append(out, content[pos+len(old):]...)

	// control: the result must parse and yield the new value
	root2, err := parseSingle(out)
	if err != nil {
		return nil, old, false, fmt.Errorf("the change would break the file: %w", err)
	}
	n2, err := findScalar(root2, field)
	if err != nil || n2.Value != nv {
		return nil, old, false, errors.New("the change did not produce the expected value")
	}
	return out, old, true, nil
}

// renderPath replaces every [[ attr "key" ]] of a repository path with the
// attribute value. A missing attribute or an unsafe value is an error.
func renderPath(tpl string, attrs domain.AttributeView) (string, error) {
	var rerr error
	res := attrExprRE.ReplaceAllStringFunc(tpl, func(m string) string {
		key := attrExprRE.FindStringSubmatch(m)[1]
		v, ok := attrs[key]
		switch {
		case !ok || v == "":
			rerr = fmt.Errorf("attribute %s is missing", key)
		case !attrValueRE.MatchString(v) || v == "." || v == "..":
			rerr = fmt.Errorf("the value of attribute %s is not usable in a path", key)
		}
		return v
	})
	if rerr != nil {
		return "", rerr
	}
	if strings.Contains(res, "[[") {
		return "", errors.New("the path holds an unsupported template expression")
	}
	clean := path.Clean(res)
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || clean != res {
		return "", fmt.Errorf("the rendered path %q is not a clean relative path", res)
	}
	return clean, nil
}
