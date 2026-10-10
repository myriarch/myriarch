// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package apidoc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/docgen"
	"github.com/myriarch/myriarch/internal/domain"
)

// roleFor maps a permission to the lowest role that holds it, from the authorization code of the server.
func roleFor(perm string) (string, bool) {
	for _, r := range []domain.Role{domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		if authz.RoleHas(r, authz.Permission(perm)) {
			return string(r), true
		}
	}
	return "", false
}

// TestAPIReference renders the REST API reference from api/openapi.yaml into docs/reference/api and compares it with the committed
// files. "make docs-gen" runs it with APIDOC_UPDATE=1 to write the files; without the variable it fails when the contract or the
// generator changed and the reference was not regenerated.
func TestAPIReference(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	require.NoError(t, err)
	spec, err := Load(raw)
	require.NoError(t, err)
	files, err := Render(spec, Options{RoleFor: roleFor})
	require.NoError(t, err)
	dir := filepath.Join("..", "..", "docs", "reference", "api")
	if os.Getenv("APIDOC_UPDATE") == "1" {
		require.NoError(t, docgen.Write(dir, files))
		return
	}
	if diff := docgen.Diff(dir, files); len(diff) > 0 {
		t.Fatalf("docs/reference/api is out of date (run make docs-gen and commit):\n  %s", strings.Join(diff, "\n  "))
	}
}

// ---- the generator itself ----------------------------------------------------------------------------------------------------

// miniRoles knows one permission besides the ones of the server; it fails the test when asked for "public" or "custom".
func miniRoles(t *testing.T) func(string) (string, bool) {
	return func(perm string) (string, bool) {
		require.NotContains(t, []string{"public", "custom"}, perm, "the generator handles public and custom itself")
		switch perm {
		case "read":
			return "viewer", true
		case "items:write":
			return "operator", true
		}
		return "", false
	}
}

func renderMini(t *testing.T) map[string][]byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "mini.yaml"))
	require.NoError(t, err)
	spec, err := Load(raw)
	require.NoError(t, err)
	files, err := Render(spec, Options{RoleFor: miniRoles(t)})
	require.NoError(t, err)
	return files
}

// TestGolden compares the pages of testdata/mini.yaml with testdata/golden; APIDOC_UPDATE=1 rewrites them.
func TestGolden(t *testing.T) {
	files := renderMini(t)
	dir := filepath.Join("testdata", "golden")
	if os.Getenv("APIDOC_UPDATE") == "1" {
		require.NoError(t, docgen.Write(dir, files))
		return
	}
	if diff := docgen.Diff(dir, files); len(diff) > 0 {
		t.Fatalf("testdata/golden is out of date (APIDOC_UPDATE=1 go test -run TestGolden ./internal/apidoc):\n  %s", strings.Join(diff, "\n  "))
	}
}

func TestMiniContent(t *testing.T) {
	files := renderMini(t)
	require.ElementsMatch(t, []string{"_index.md", "items.md", "live-events.md", "data-types.md"}, keys(files))
	items := string(files["items.md"])
	require.Contains(t, items, "title: \"Items\"\nweight: 10\n")
	require.Contains(t, items, "## List items\n\n`GET /api/v1/items`\n\nOperation `listItems`. Permission `read` (lowest role: viewer).")
	require.Contains(t, items, "&lt;placeholder>", "an angle bracket outside code is escaped")
	require.Contains(t, items, "`<code>`", "an angle bracket inside code is not")
	require.Contains(t, items, "Filter by kind. Values are ORed: \"a\\|b\".", "a pipe in a table cell is escaped")
	require.Contains(t, items, "Operation `login`. Permission `public`. No login is needed.")
	require.Contains(t, items, "Operation `hook`. Permission `custom`.")
	live := string(files["live-events.md"])
	require.Contains(t, live, "`item.updated` | `Item` |", "the description of the event stream is kept verbatim")
	require.Contains(t, live, "`event: <type>`")
	data := string(files["data-types.md"])
	require.Contains(t, data, "| `parent` | [Part](#part) |", "a self reference is a link")
	require.Contains(t, data, "- `alpha`\n- `beta`", "an enum lists its values")
	idx := string(files["_index.md"])
	require.Contains(t, idx, "## Conventions\n\n- Base path `/api/v1`.")
	require.Contains(t, idx, "## Live updates\n\n`GET /events` (Server-Sent Events).")
	require.Contains(t, idx, "## Non-REST endpoints\n\nNot part of this document: `/healthz`.")
	require.Contains(t, idx, "| `items:write` | operator | 2 |")
}

func TestRenderIsDeterministic(t *testing.T) {
	a, b := renderMini(t), renderMini(t)
	require.Equal(t, a, b)
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

const base = `openapi: 3.1.0
info: { title: T, version: "1" }
servers: [{ url: /api/v1 }]
tags: [{ name: t, description: d }]
paths:
  /x:
    get:
      operationId: getX
      tags: [t]
      summary: X
      x-permission: read
      responses:
        '200': { description: ok }
components:
  schemas:
%s`

func withSchemas(schemas string) []byte {
	var b strings.Builder
	for _, l := range strings.Split(strings.Trim(schemas, "\n"), "\n") {
		b.WriteString("    " + l + "\n")
	}
	return []byte(strings.Replace(base, "%s", b.String(), 1))
}

func TestValidateRefusesWhatItDoesNotRender(t *testing.T) {
	require.NoError(t, Validate(withSchemas("Thing:\n  type: object\n  properties:\n    a: { type: string }\n")))
	cases := []struct{ name, schemas, pointer string }{
		{"allOf", "Thing:\n  allOf: [{ type: string }]", "/components/schemas/Thing/allOf"},
		{"oneOf", "Thing:\n  oneOf: [{ type: string }]", "/components/schemas/Thing/oneOf"},
		{"anyOf", "Thing:\n  anyOf: [{ type: string }]", "/components/schemas/Thing/anyOf"},
		{"not", "Thing:\n  not: { type: string }", "/components/schemas/Thing/not"},
		{"discriminator", "Thing:\n  type: object\n  discriminator: { propertyName: kind }", "/components/schemas/Thing/discriminator"},
		{"type array", "Thing:\n  type: object\n  properties:\n    a: { type: [string, 'null'] }", "/components/schemas/Thing/properties/a/type"},
		{"inline nested object", "Thing:\n  type: object\n  properties:\n    a:\n      type: object\n      properties:\n        b: { type: string }", "/components/schemas/Thing/properties/a/properties"},
		{"nested object in items", "Thing:\n  type: array\n  items:\n    type: object\n    properties:\n      b: { type: string }", "/components/schemas/Thing/items/properties"},
		{"multi paragraph property description", "Thing:\n  type: object\n  properties:\n    a:\n      type: string\n      description: |\n        One.\n\n        Two.", "/components/schemas/Thing/properties/a/description"},
		{"external ref", "Thing:\n  type: object\n  properties:\n    a: { $ref: 'other.yaml#/components/schemas/X' }", "/components/schemas/Thing/properties/a/$ref"},
		{"missing ref", "Thing:\n  type: object\n  properties:\n    a: { $ref: '#/components/schemas/Nope' }", "/components/schemas/Thing/properties/a/$ref"},
		{"const", "Thing:\n  const: x", "/components/schemas/Thing/const"},
		{"title", "Thing:\n  type: string\n  title: Name", "/components/schemas/Thing/title"},
		{"required without properties", "Thing:\n  type: string\n  required: [a]", "/components/schemas/Thing/required/0"},
		{"required name that is no property", "Thing:\n  type: object\n  properties:\n    a: { type: string }\n  required: [a, b]", "/components/schemas/Thing/required/1"},
		{"required in a nested schema", "Thing:\n  type: array\n  items: { type: string, required: [a] }", "/components/schemas/Thing/items/required"},
		{"required in a property", "Thing:\n  type: object\n  properties:\n    a: { type: string, required: [a] }", "/components/schemas/Thing/properties/a/required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(withSchemas(c.schemas))
			require.Error(t, err)
			require.Contains(t, err.Error(), c.pointer+":")
			_, loadErr := Load(withSchemas(c.schemas))
			require.Error(t, loadErr, "Load validates")
		})
	}
	t.Run("operation keyword", func(t *testing.T) {
		err := Validate([]byte(strings.Replace(string(withSchemas("Thing:\n  type: string")), "summary: X", "summary: X\n      callbacks: {}", 1)))
		require.ErrorContains(t, err, "/paths/~1x/get/callbacks:")
	})
	t.Run("description of a parameter schema", func(t *testing.T) {
		err := Validate([]byte(strings.Replace(string(withSchemas("Thing:\n  type: string")), "summary: X", "summary: X\n      parameters:\n        - { name: q, in: query, schema: { type: string, description: D } }", 1)))
		require.ErrorContains(t, err, "/paths/~1x/get/parameters/0/schema/description:")
	})
	t.Run("description of a body schema", func(t *testing.T) {
		raw := strings.Replace(string(withSchemas("Thing:\n  type: string")), "'200': { description: ok }", "'200': { description: ok, content: { application/json: { schema: { type: string, description: D } } } }", 1)
		require.ErrorContains(t, Validate([]byte(raw)), "/paths/~1x/get/responses/200/content/application~1json/schema/description:")
	})
	t.Run("contract info", func(t *testing.T) {
		raw := strings.Replace(string(withSchemas("Thing:\n  type: string")), `info: { title: T, version: "1" }`, "info: { title: T, version: \"1\", contact: { phone: '1' }, license: { text: x } }", 1)
		err := Validate([]byte(raw))
		require.ErrorContains(t, err, "/info/contact/phone:")
		require.ErrorContains(t, err, "/info/license/text:")
	})
	t.Run("servers", func(t *testing.T) {
		for name, servers := range map[string]string{
			"none":      "",
			"two":       "servers: [{ url: /a }, { url: /b }]\n",
			"absolute":  "servers: [{ url: 'https://example.com/api' }]\n",
			"trailing":  "servers: [{ url: /api/ }]\n",
			"variables": "servers: [{ url: /api, variables: {} }]\n",
		} {
			raw := strings.Replace(string(withSchemas("Thing:\n  type: string")), "servers: [{ url: /api/v1 }]\n", servers, 1)
			err := Validate([]byte(raw))
			require.Error(t, err, name)
			require.Contains(t, err.Error(), "/servers", name)
		}
	})
	t.Run("heading in a description", func(t *testing.T) {
		err := Validate([]byte(strings.Replace(string(withSchemas("Thing:\n  type: string")), "summary: X", "summary: X\n      description: |\n        # Title", 1)))
		require.ErrorContains(t, err, "/paths/~1x/get/description:")
	})
}

func loadWith(t *testing.T, schemas string) *Spec {
	t.Helper()
	s, err := Load(withSchemas(schemas))
	require.NoError(t, err)
	return s
}

func TestUnknownPermissionIsAnError(t *testing.T) {
	raw := strings.Replace(string(withSchemas("Thing:\n  type: string")), "x-permission: read", "x-permission: bogus:do", 1)
	s, err := Load([]byte(raw))
	require.NoError(t, err)
	_, err = Render(s, Options{RoleFor: miniRoles(t)})
	require.ErrorContains(t, err, `unknown permission "bogus:do"`)
	for _, perm := range []string{"public", "custom"} {
		s, err := Load([]byte(strings.Replace(string(withSchemas("Thing:\n  type: string")), "x-permission: read", "x-permission: "+perm, 1)))
		require.NoError(t, err)
		_, err = Render(s, Options{RoleFor: miniRoles(t)})
		require.NoError(t, err, perm)
	}
}

func TestDuplicateSlugsAreAnError(t *testing.T) {
	raw := strings.Replace(string(withSchemas("Thing:\n  type: string")), "  /x:", `  /y:
    get:
      operationId: getY
      tags: [t]
      summary: "X!"
      x-permission: read
      responses:
        '200': { description: ok }
  /x:`, 1)
	s, err := Load([]byte(raw))
	require.NoError(t, err)
	_, err = Render(s, Options{RoleFor: miniRoles(t)})
	require.ErrorContains(t, err, `same heading anchor "x"`)

	// two schemas whose names differ in case only
	s = loadWith(t, "Thing:\n  type: string\nTHING:\n  type: string")
	_, err = Render(s, Options{RoleFor: miniRoles(t)})
	require.ErrorContains(t, err, "same heading anchor")
}

func TestExampleGenerator(t *testing.T) {
	s := loadWith(t, `Node:
  type: object
  required: [name]
  properties:
    name: { type: string, example: root }
    id: { type: string, format: uuid }
    at: { type: string, format: date-time }
    n: { type: integer }
    f: { type: boolean, default: true }
    mode: { type: string, enum: [fast, slow] }
    secret: { type: string, readOnly: true }
    children:
      type: array
      items: { $ref: '#/components/schemas/Node' }
    parent: { $ref: '#/components/schemas/Node' }
    tags:
      type: object
      additionalProperties: { type: string }
`)
	got, err := s.ExampleJSON(&Schema{Ref: "#/components/schemas/Node"}, true)
	require.NoError(t, err)
	want := `{
  "name": "root",
  "id": "00000000-0000-4000-8000-000000000000",
  "at": "2026-01-01T00:00:00Z",
  "n": 0,
  "f": true,
  "mode": "fast",
  "children": [],
  "tags": {
    "key": "string"
  }
}`
	require.Equal(t, want, got, "a cycle ends the expansion (children are empty, parent is left out), a read-only member is left out of a request")
	resp, err := s.ExampleJSON(&Schema{Ref: "#/components/schemas/Node"}, false)
	require.NoError(t, err)
	require.Contains(t, resp, `"secret": "string"`)
}

func TestOutputShape(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	require.NoError(t, err)
	spec, err := Load(raw)
	require.NoError(t, err)
	files, err := Render(spec, Options{RoleFor: roleFor})
	require.NoError(t, err)
	ops := 0
	for name, b := range files {
		s := string(b)
		require.True(t, strings.HasPrefix(s, "---\ntitle: \""), name)
		require.True(t, strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, "\n\n"), name+": exactly one final newline")
		require.NotContains(t, s, "\n\n\n", name+": no double blank line")
		require.NotRegexp(t, `(?m)^# `, s, name+": pages start at H2")
		require.NotContains(t, s, "{{<")
		ops += strings.Count(s, "\nOperation `")
	}
	require.Equal(t, len(spec.Operations), ops, "every operation has a section")
	require.Equal(t, strings.Count(string(raw), "      operationId:"), len(spec.Operations))
}

// ---- every keyword is rendered or refused ------------------------------------------------------------------------------------

// scalarKeywords returns the flow mapping of a scalar schema that sets every allowlisted scalar keyword to a value that is unique
// for the context (n is its number), and the texts that the page must show for them.
func scalarKeywords(tag string, n int, description bool) (string, []string) {
	kw := []string{
		"type: string",
		"format: fmt" + tag,
		"enum: [en" + tag + "a, en" + tag + "b]",
		"default: df" + tag,
		"example: ex" + tag,
		fmt.Sprintf("minimum: %d", n+1),
		fmt.Sprintf("maximum: %d", n+2),
		fmt.Sprintf("minLength: %d", n+3),
		fmt.Sprintf("maxLength: %d", n+4),
		"pattern: 'p" + tag + "'",
		"deprecated: true",
		"readOnly: true",
		"writeOnly: true",
	}
	want := []string{
		"`fmt" + tag + "`", "`en" + tag + "a`", "`en" + tag + "b`", "`df" + tag + "`", "`ex" + tag + "`",
		fmt.Sprintf("Range %d to %d.", n+1, n+2), fmt.Sprintf("%d to %d characters.", n+3, n+4), "`p" + tag + "`",
		"Deprecated.", "Read-only.", "Write-only.",
	}
	if description {
		kw = append(kw, "description: ds"+tag)
		want = append(want, "ds"+tag)
	}
	return "{ " + strings.Join(kw, ", ") + " }", want
}

// containerKeywords returns the keywords of an array that are not scalar ones.
func containerKeywords(n int, indent string) (string, []string) {
	return fmt.Sprintf("minItems: %d\n%smaxItems: %d\n%suniqueItems: true", n+5, indent, n+6, indent),
		[]string{fmt.Sprintf("%d to %d items.", n+5, n+6), "The items are unique."}
}

// TestEveryKeywordIsRendered sets every allowlisted keyword in every place of a contract in which Validate accepts it (a component
// schema, a property, the element and value schemas, further fields, and the schemas of parameters and bodies) and checks that the
// value is in the generated pages. A keyword that a place cannot show must be refused by Validate (TestValidateRefusesWhatItDoesNotRender).
func TestEveryKeywordIsRendered(t *testing.T) {
	type spot struct {
		name    string
		schemas func(tag string, n int) (yaml string, want []string)
		op      string // text added to the operation (parameters, body, responses)
	}
	scalar := func(tag string, n int, desc bool) (string, []string) { return scalarKeywords(tag, n, desc) }
	spots := []spot{
		{name: "component", schemas: func(tag string, n int) (string, []string) {
			s, w := scalar(tag, n, true)
			return "Thing: " + s, w
		}},
		{name: "property", schemas: func(tag string, n int) (string, []string) {
			s, w := scalar(tag, n, true)
			return "Thing:\n  type: object\n  properties:\n    a: " + s, w
		}},
		{name: "items", schemas: func(tag string, n int) (string, []string) {
			s, w := scalar(tag, n, true)
			c, cw := containerKeywords(n, "  ")
			return "Thing:\n  type: array\n  " + c + "\n  items: " + s, append(w, cw...)
		}},
		{name: "items of a property", schemas: func(tag string, n int) (string, []string) {
			s, w := scalar(tag, n, true)
			c, cw := containerKeywords(n, "      ")
			return "Thing:\n  type: object\n  properties:\n    a:\n      type: array\n      " + c + "\n      items: " + s, append(w, cw...)
		}},
		{name: "values of a map", schemas: func(tag string, n int) (string, []string) {
			s, w := scalar(tag, n, true)
			return "Thing:\n  type: object\n  additionalProperties: " + s, w
		}},
		{name: "values of a map property", schemas: func(tag string, n int) (string, []string) {
			s, w := scalar(tag, n, true)
			return "Thing:\n  type: object\n  properties:\n    a:\n      type: object\n      additionalProperties: " + s, w
		}},
		{name: "further fields", schemas: func(tag string, n int) (string, []string) {
			s, w := scalar(tag, n, true)
			return "Thing:\n  type: object\n  properties:\n    a: { type: string }\n  additionalProperties: " + s, w
		}},
		{name: "items of items", schemas: func(tag string, n int) (string, []string) {
			s, w := scalar(tag, n, true)
			return "Thing:\n  type: array\n  items:\n    type: array\n    items: " + s, w
		}},
		{name: "values of values", schemas: func(tag string, n int) (string, []string) {
			s, w := scalar(tag, n, true)
			return "Thing:\n  type: object\n  additionalProperties:\n    type: object\n    additionalProperties: " + s, w
		}},
		{name: "items of values", schemas: func(tag string, n int) (string, []string) {
			s, w := scalar(tag, n, true)
			return "Thing:\n  type: object\n  additionalProperties:\n    type: array\n    items: " + s, w
		}},
		{name: "object with an example", schemas: func(tag string, n int) (string, []string) {
			return "Thing:\n  type: object\n  deprecated: true\n  example: { k: ex" + tag + " }\n  properties:\n    a: { type: string }", []string{"Deprecated.", "ex" + tag}
		}},
		{name: "parameter schema", schemas: func(tag string, n int) (string, []string) {
			_, w := scalar(tag, n, false)
			return "Thing: { type: string }\n", w
		}, op: "param"},
		{name: "request body schema", schemas: func(tag string, n int) (string, []string) {
			_, w := scalar(tag, n, false)
			return "Thing: { type: string }\n", w
		}, op: "request"},
		{name: "response schema", schemas: func(tag string, n int) (string, []string) {
			_, w := scalar(tag, n, false)
			return "Thing: { type: string }\n", w
		}, op: "response"},
	}
	for i, sp := range spots {
		t.Run(sp.name, func(t *testing.T) {
			tag := fmt.Sprintf("T%d", i)
			n := 1000 * (i + 1)
			schemas, want := sp.schemas(tag, n)
			raw := string(withSchemas(schemas))
			if sp.op != "" {
				inline, _ := scalar(tag, n, false)
				raw = strings.Replace(raw, "      responses:\n        '200': { description: ok }", inlineOperation(sp.op, inline), 1)
			}
			spec, err := Load([]byte(raw))
			require.NoError(t, err)
			files, err := Render(spec, Options{RoleFor: miniRoles(t)})
			require.NoError(t, err)
			var all strings.Builder
			for _, b := range files {
				all.Write(b)
			}
			for _, w := range want {
				require.Contains(t, all.String(), w, "the keyword value %q is dropped", w)
			}
		})
	}
}

// inlineOperation returns the lines of an operation with an inline schema as the schema of a parameter, a request body or a response.
func inlineOperation(where, schema string) string {
	switch where {
	case "param":
		return "      parameters:\n        - { name: q, in: query, schema: " + schema + " }\n      responses:\n        '200': { description: ok }"
	case "request":
		return "      requestBody:\n        content:\n          application/json:\n            schema: " + schema + "\n      responses:\n        '200': { description: ok }"
	}
	return "      responses:\n        '200':\n          description: ok\n          content:\n            application/json:\n              schema: " + schema
}

func TestContractInfoAndServerAreRendered(t *testing.T) {
	raw := strings.Replace(string(withSchemas("Thing: { type: string }")), `info: { title: T, version: "1" }
servers: [{ url: /api/v1 }]`, `info:
  title: T
  version: "1"
  summary: Sum-marker
  contact: { name: Contact-marker, url: 'https://example.com/c', email: ops@example.com }
  license: { name: Lic-marker, identifier: Apache-2.0, url: 'https://example.com/l' }
servers: [{ url: /edge/v7, description: Server-marker }]`, 1)
	spec, err := Load([]byte(raw))
	require.NoError(t, err)
	files, err := Render(spec, Options{RoleFor: miniRoles(t)})
	require.NoError(t, err)
	idx := string(files["_index.md"])
	for _, w := range []string{"Sum-marker.", "[Contact-marker](https://example.com/c), ops@example.com", "[Lic-marker](https://example.com/l), SPDX `Apache-2.0`"} {
		require.Contains(t, idx, w)
	}
	require.NotContains(t, idx, "make docs-check", "the index addresses readers, not contributors")
	page := string(files["t.md"])
	require.Contains(t, page, "base path `/edge/v7` (Server-marker).")
	require.Contains(t, page, "`GET /edge/v7/x`")
	require.NotContains(t, page, "/api/v1")
}

func TestTagSummaryTable(t *testing.T) {
	page := string(renderMini(t)["items.md"])
	require.Contains(t, page, "| Operation | Request | Permission |\n")
	require.Contains(t, page, "| [List items](#list-items) | `GET /items` | `read` (viewer) |\n")
}
