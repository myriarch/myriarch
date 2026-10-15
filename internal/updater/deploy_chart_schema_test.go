// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
)

// The values schema of the server chart (deploy/helm/myriarch/values.schema.json) is generated from values.schema.source.yaml and
// internal/config (make docs-gen, internal/refdoc). These tests check the result from the outside: that the schema is strict, that
// it covers every value the chart ships and reads, that its config block says what internal/config says, and that it accepts and
// refuses the values it should. The validator is a stand-in for Helm (deploy_chart_schema_validator_test.go).

const chartSchemaPath = "helm/myriarch/values.schema.json"

func mustJSON(t *testing.T, text string) any {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal([]byte(text), &v), text)
	return v
}

func loadChartSchema(t *testing.T) map[string]any {
	t.Helper()
	schema, ok := mustJSON(t, string(readDeploy(t, chartSchemaPath))).(map[string]any)
	require.True(t, ok, "the schema is a JSON object")
	return schema
}

// yamlValues decodes a values file into the types of decoded JSON, which are the types that Helm's validator sees.
func yamlValues(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var raw any
	require.NoError(t, yaml.Unmarshal(data, &raw))
	if raw == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(raw)
	require.NoError(t, err)
	m, ok := mustJSON(t, string(b)).(map[string]any)
	require.True(t, ok, "values are a mapping")
	return m
}

// repoFile reads a file of the repository, relative to its root.
func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(deployDir, "..", filepath.FromSlash(rel)))
	require.NoError(t, err)
	return b
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepCopy(e)
		}
		return out
	}
	return v
}

// mergeValues merges values files like helm install -f a -f b does: mappings are merged, everything else is replaced, and a null
// stays a null.
func mergeValues(a, b map[string]any) map[string]any {
	out := deepCopy(a).(map[string]any)
	for k, v := range b {
		if bm, ok := v.(map[string]any); ok {
			if am, ok := out[k].(map[string]any); ok {
				out[k] = mergeValues(am, bm)
				continue
			}
		}
		out[k] = deepCopy(v)
	}
	return out
}

// coalesceValues adds the defaults of the chart to the values of the user like Helm does before it validates: a key that the user
// sets to null and that the defaults define is removed, a missing key gets its default, and mappings are merged.
func coalesceValues(user, defaults map[string]any) {
	for key, def := range defaults {
		uv, set := user[key]
		switch {
		case set && uv == nil:
			delete(user, key)
		case !set:
			user[key] = deepCopy(def)
		default:
			um, uok := uv.(map[string]any)
			dm, dok := def.(map[string]any)
			if uok && dok {
				coalesceValues(um, dm)
			}
		}
	}
}

// chartValues returns what Helm validates for the default values and the given values files, in order.
func chartValues(t *testing.T, overlays ...map[string]any) map[string]any {
	t.Helper()
	defaults := yamlValues(t, readDeploy(t, "helm/myriarch/values.yaml"))
	user := map[string]any{}
	for _, o := range overlays {
		user = mergeValues(user, o)
	}
	coalesceValues(user, defaults)
	return user
}

func validateChartValues(t *testing.T, schema map[string]any, overlays ...map[string]any) []string {
	t.Helper()
	return newSchemaValidator(schema).Validate(chartValues(t, overlays...))
}

// ------------------------------------------------------------------ strictness ---

// openObjects are the objects of the schema that accept keys beyond the ones they list. Each holds a Kubernetes API object (or the
// values of a parent chart) that the chart passes through and the API server validates. Every other object is closed, so a key that
// the chart does not read is an error.
var openObjects = map[string]string{
	"/properties/global":                                       "values of a parent chart",
	"/properties/extraVolumes/items":                           "a Kubernetes volume",
	"/properties/extraVolumeMounts/items":                      "a Kubernetes volume mount",
	"/properties/httpRoute/properties/parentRefs/items":        "a Gateway API parent reference",
	"/properties/httpRoute/properties/timeouts":                "the Gateway API timeouts of a rule",
	"/properties/resources/properties/claims/items":            "a Kubernetes resource claim",
	"/properties/podSecurityContext":                           "a Kubernetes pod security context",
	"/properties/podSecurityContext/properties/seccompProfile": "a Kubernetes seccomp profile",
	"/properties/securityContext":                              "a Kubernetes container security context",
	"/properties/securityContext/properties/seccompProfile":    "a Kubernetes seccomp profile",
	"/properties/topologySpreadConstraints/items":              "a Kubernetes topology spread constraint",
	"/properties/affinity/properties/nodeAffinity":             "a Kubernetes node affinity",
	"/properties/affinity/properties/podAffinity":              "a Kubernetes pod affinity",
	"/properties/affinity/properties/podAntiAffinity":          "a Kubernetes pod anti-affinity",
	"/$defs/labelSelector/properties/matchExpressions/items":   "a Kubernetes label selector requirement",
	"/$defs/envVar/properties/valueFrom":                       "a Kubernetes environment variable source",
}

// subschemas lists the keywords whose values are schemas, for the walk through the document.
func subschemas(node map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range node {
		switch k {
		case "properties", "$defs":
			for name, sub := range v.(map[string]any) {
				out[k+"/"+escapePointer(name)] = sub
			}
		case "items", "not":
			out[k] = v
		case "additionalProperties":
			if _, isSchema := v.(map[string]any); isSchema {
				out[k] = v
			}
		case "anyOf", "oneOf", "allOf":
			for i, sub := range v.([]any) {
				out[fmt.Sprintf("%s/%d", k, i)] = sub
			}
		}
	}
	return out
}

func isObjectSchema(node map[string]any) bool {
	if _, has := node["properties"]; has {
		return true
	}
	switch ty := node["type"].(type) {
	case string:
		return ty == "object"
	case []any:
		for _, e := range ty {
			if e == "object" {
				return true
			}
		}
	}
	return false
}

func TestChartSchemaIsStrict(t *testing.T) {
	schema := loadChartSchema(t)
	assert.Equal(t, "https://json-schema.org/draft/2020-12/schema", schema["$schema"])
	assert.Equal(t, false, schema["additionalProperties"], "the root object is closed")

	open := map[string]bool{}
	var walk func(node any, ptr string)
	walk = func(node any, ptr string) {
		m, ok := node.(map[string]any)
		if !ok {
			return
		}
		if isObjectSchema(m) {
			ap, has := m["additionalProperties"]
			switch {
			case !has:
				t.Errorf("%s: an object schema must say additionalProperties: false to be closed, true to pass a Kubernetes object through, or a schema for the values of a map", ptr)
			case ap == true:
				open[ptr] = true
				assert.Contains(t, openObjects, ptr, "%s accepts any key: add it to openObjects with the reason, or close it", ptr)
			}
		}
		for rel, sub := range subschemas(m) {
			walk(sub, ptr+"/"+rel)
		}
	}
	walk(schema, "")
	for ptr, why := range openObjects {
		assert.True(t, open[ptr], "openObjects lists %s (%s), which is not an open object of the schema", ptr, why)
	}
}

// ------------------------------------------------------------------- coverage ---

// schemaChildren returns the schemas that a key (or "[]" for a list entry) can have below a node: the property, else the schema of
// the values of a map. An open object accepts any key.
func schemaChildren(root map[string]any, node any, token string) []any {
	m, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	var out []any
	if ref, isRef := m["$ref"].(string); isRef {
		target, err := newSchemaValidator(root).resolve(ref)
		if err == nil {
			out = append(out, schemaChildren(root, target, token)...)
		}
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		if list, ok := m[k].([]any); ok {
			for _, sub := range list {
				out = append(out, schemaChildren(root, sub, token)...)
			}
		}
	}
	if token == "[]" {
		if items, ok := m["items"]; ok {
			out = append(out, items)
		}
		return out
	}
	if props, ok := m["properties"].(map[string]any); ok {
		if sub, ok := props[token]; ok {
			return append(out, sub)
		}
	}
	switch ap := m["additionalProperties"].(type) {
	case bool:
		if ap {
			out = append(out, true)
		}
	case map[string]any:
		out = append(out, ap)
	}
	return out
}

// schemaHasPath reports whether the schema accepts a value at the path.
func schemaHasPath(root map[string]any, path []string) bool {
	nodes := []any{root}
	for _, token := range path {
		var next []any
		for _, n := range nodes {
			next = append(next, schemaChildren(root, n, token)...)
		}
		if len(next) == 0 {
			return false
		}
		nodes = next
	}
	return true
}

// valuePaths lists the paths of all keys below v; a list contributes "[]" and the paths of its entries.
func valuePaths(v any, prefix []string, out *[][]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			p := append(append([]string{}, prefix...), k)
			*out = append(*out, p)
			valuePaths(e, p, out)
		}
	case []any:
		for _, e := range x {
			valuePaths(e, append(append([]string{}, prefix...), "[]"), out)
		}
	}
}

func TestChartSchemaCoversTheValuesFile(t *testing.T) {
	schema := loadChartSchema(t)
	var paths [][]string
	valuePaths(yamlValues(t, readDeploy(t, "helm/myriarch/values.yaml")), nil, &paths)
	require.Greater(t, len(paths), 200)
	var missing []string
	for _, p := range paths {
		if !schemaHasPath(schema, p) {
			missing = append(missing, strings.Join(p, "."))
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "values.yaml has keys that values.schema.source.yaml does not list: add them (and run make docs-gen)")
}

var valuesRef = regexp.MustCompile(`\.Values((?:\.[A-Za-z_][A-Za-z0-9_]*)+)`)

// guardedValuesRef finds a read through a default, (.Values.a.b | default dict).c, which the templates use for a map that the values of
// an older release may lack ("helm upgrade --reuse-values"). The read is the path a.b.c.
var guardedValuesRef = regexp.MustCompile(`\(\s*\.Values((?:\.[A-Za-z_][A-Za-z0-9_]*)+)\s*\|\s*default\s+dict\s*\)((?:\.[A-Za-z_][A-Za-z0-9_]*)+)`)

// chartTemplateFiles lists the templates below templates/ and in its subdirectories (templates/tests/ holds the Pod of "helm test").
func chartTemplateFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	require.NoError(t, filepath.WalkDir(filepath.Join(deployDir, "helm/myriarch/templates"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return err
	}))
	require.NotEmpty(t, files)
	return files
}

func TestChartSchemaCoversTheValuesTheTemplatesRead(t *testing.T) {
	schema := loadChartSchema(t)
	read := map[string][]string{}
	for _, f := range chartTemplateFiles(t) {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, m := range valuesRef.FindAllStringSubmatch(string(b), -1) {
			read[m[1][1:]] = append(read[m[1][1:]], filepath.Base(f))
		}
		for _, m := range guardedValuesRef.FindAllStringSubmatch(string(b), -1) {
			path := m[1][1:] + m[2]
			read[path] = append(read[path], filepath.Base(f))
		}
	}
	require.Greater(t, len(read), 60, "the scan of the templates looks broken")
	var missing []string
	for path, in := range read {
		if !schemaHasPath(schema, strings.Split(path, ".")) {
			missing = append(missing, fmt.Sprintf(".Values.%s (%s)", path, in[0]))
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "a template reads a value that the schema does not list, so the schema would refuse the value")
	assert.Contains(t, read, "global", "the image helper reads global.imageRegistry")
	assert.Contains(t, read, "podDisruptionBudget.maxUnavailable")
	assert.Contains(t, read, "tests.resources", "the scan reaches templates/tests/")
	assert.Contains(t, read, "tests.enabled", "the scan reads (.Values.tests | default dict).enabled in the helper")
	assert.Contains(t, read, "prometheusRule.enabled")
	assert.Contains(t, read, "prometheusRule.labels")
}

// ------------------------------------------------------ the config block ---

// chartExcludedConfigKeys are the keys of internal/config that the chart does not offer. refdoc has the same list with the reasons
// (chartConfigExclusions); it is repeated here on purpose, because a key that is added to or removed from it decides whether a
// secret or the development mode can be set through the values, and that must not change with one edit.
var chartExcludedConfigKeys = []string{
	"auth.bootstrapAdmin", "opamp.enrollmentTokens", "auth.devLogin", "dev",
	"database.dsn", "auth.oidc.clientSecret", "gitops.webhookSecret", "notifications.channels[].url",
}

var durationType = reflect.TypeOf(time.Duration(0))

// expectedConfigKeys lists the settings of internal/config with the JSON type of their schema: "path": "type". A list of
// entries contributes path[] for the entries, and a map path.<name>.
func expectedConfigKeys(t reflect.Type, prefix string, excluded map[string]bool, out map[string]string) {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		key, _, _ := strings.Cut(sf.Tag.Get("yaml"), ",")
		if key == "-" || key == "" || !sf.IsExported() {
			continue
		}
		path := prefix + key
		if excluded[path] {
			continue
		}
		expectedConfigNode(sf.Type, path, excluded, out)
	}
}

func expectedConfigNode(t reflect.Type, path string, excluded map[string]bool, out map[string]string) {
	switch {
	case t == durationType:
		out[path] = "duration"
		return
	case t.Kind() == reflect.Struct:
		out[path] = "object"
		expectedConfigKeys(t, path+".", excluded, out)
	case t.Kind() == reflect.Slice:
		out[path] = "array"
		if t.Elem().Kind() == reflect.Struct {
			expectedConfigKeys(t.Elem(), path+"[].", excluded, out)
		}
	case t.Kind() == reflect.Map:
		out[path] = "object"
		e := t.Elem()
		if e.Kind() == reflect.Struct {
			expectedConfigKeys(e, path+".<name>.", excluded, out)
		}
	case t.Kind() == reflect.String:
		out[path] = "string"
	case t.Kind() == reflect.Bool:
		out[path] = "boolean"
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Uint64:
		out[path] = "integer"
	case t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64:
		out[path] = "number"
	default:
		panic("the test does not know the type " + t.String())
	}
}

// schemaConfigKeys lists the settings of the config block of the schema in the same form.
func schemaConfigKeys(t *testing.T, node map[string]any, path string, out map[string]string) {
	t.Helper()
	// every setting has one type: an entry of a map is not nullable either (a null would be rendered into the configuration file,
	// which the server refuses when it starts)
	kind, isString := node["type"].(string)
	require.True(t, isString, "%s: the type of a setting is a single type name, got %v", path, node["type"])
	if path != "" {
		if _, isDuration := node["pattern"]; isDuration && kind == "string" {
			out[path] = "duration"
		} else {
			out[path] = kind
		}
	}
	if props, ok := node["properties"].(map[string]any); ok {
		assert.Equal(t, false, node["additionalProperties"], "%s: the object is closed", path)
		for name, sub := range props {
			child := name
			if path != "" {
				child = path + "." + name
			}
			schemaConfigKeys(t, sub.(map[string]any), child, out)
		}
	}
	if items, ok := node["items"].(map[string]any); ok && kind == "array" {
		if _, isObject := items["properties"]; isObject {
			schemaConfigKeys(t, items, path+"[]", out)
			// the entries are not a setting of their own
			delete(out, path+"[]")
		}
	}
	if values, ok := node["additionalProperties"].(map[string]any); ok && kind == "object" {
		if _, isObject := values["properties"]; isObject {
			schemaConfigKeys(t, values, path+".<name>", out)
			delete(out, path+".<name>")
		}
	}
}

func TestChartSchemaConfigBlockMatchesInternalConfig(t *testing.T) {
	schema := loadChartSchema(t)
	cfg, ok := schema["properties"].(map[string]any)["config"].(map[string]any)
	require.True(t, ok, "the schema has a config block")

	excluded := map[string]bool{}
	for _, k := range chartExcludedConfigKeys {
		excluded[k] = true
	}
	want := map[string]string{}
	expectedConfigKeys(reflect.TypeOf(config.Config{}), "", excluded, want)
	require.Greater(t, len(want), 150)
	got := map[string]string{}
	schemaConfigKeys(t, cfg, "", got)

	// "duration" is a string with a pattern
	assert.Equal(t, want, got, "the config block of values.schema.json differs from internal/config: run make docs-gen")

	// the keys that the chart does not offer are absent and named in the description
	for _, k := range chartExcludedConfigKeys {
		assert.NotContains(t, got, k)
		assert.Contains(t, cfg["description"], k)
	}
	assert.Equal(t, false, cfg["additionalProperties"])
}

// TestChartSchemaDurationPatternIsGoSyntax: a duration of the configuration is a string that time.ParseDuration accepts, and
// nothing else. The loader decodes it before it expands ${VAR}, and it refuses a YAML integer.
func TestChartSchemaDurationPatternIsGoSyntax(t *testing.T) {
	schema := loadChartSchema(t)
	pattern := schema["properties"].(map[string]any)["config"].(map[string]any)["properties"].(map[string]any)["server"].(map[string]any)["properties"].(map[string]any)["shutdownTimeout"].(map[string]any)["pattern"].(string)
	re := regexp.MustCompile(pattern)
	for _, s := range []string{
		"0", "+0", "-0", "1s", "30s", "1m30s", "1.5h", ".5s", "1.s", "5ms", "7ns", "9us", "-5m", "+1h", "720h", "1h1m1s", "0s", "0.5s",
		"", "30", "0.5", "1d", "30 s", "s", ".s", "1h30", "m", "30S", "1h-5m", "${TIMEOUT}", "1 h", "++1s", "1ss", "1w", "00", "0x10s",
	} {
		_, err := time.ParseDuration(s)
		assert.Equal(t, err == nil, re.MatchString(s), "%q: time.ParseDuration says %v", s, err)
	}
}

// ------------------------------------------------------- accepted values ---

func TestChartSchemaAcceptsTheShippedValues(t *testing.T) {
	schema := loadChartSchema(t)
	overlay := func(rel string) map[string]any { return yamlValues(t, repoFile(t, rel)) }
	const (
		examples = "deploy/helm/myriarch/examples/"
		prod     = "deploy/examples/production/myriarch-values.yaml"
	)
	ingressExamples, err := filepath.Glob(filepath.Join(deployDir, "helm/myriarch/examples/ingress-*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, ingressExamples)

	cases := map[string][]map[string]any{
		"the defaults":             {},
		"quickstart with Dex":      {overlay(examples + "quickstart-dex.yaml")},
		"install guide":            {overlay(examples + "install-guide.yaml")},
		"openshift example":        {overlay(examples + "openshift.yaml")},
		"production example":       {overlay(prod)},
		"production and openshift": {overlay(prod), overlay(examples + "openshift.yaml")},
		"kind probe":               {overlay("hack/kind/values/probe.yaml")},
		"kind probe with Dex":      {overlay(examples + "quickstart-dex.yaml"), overlay("hack/kind/values/dex-overlay.yaml")},
	}
	for _, f := range ingressExamples {
		name := filepath.Base(f)
		cases["production and "+name] = []map[string]any{overlay(prod), yamlValues(t, mustRead(t, f))}
	}
	for name, overlays := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, validateChartValues(t, schema, overlays...))
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

// TestChartSchemaAcceptsValidValues: the schema alone accepts these values. Some of them need more settings before the templates
// render them (an Ingress needs trustedProxies and a source in the network policy); that is the business of the templates. The
// values avoid the words y, n, yes, no, on and off as plain scalars, because Helm reads YAML 1.1 and takes them for booleans.
func TestChartSchemaAcceptsValidValues(t *testing.T) {
	schema := loadChartSchema(t)
	d := "sha256:" + strings.Repeat("0123456789abcdef", 4)
	cases := map[string]string{
		"registry and repository": `image: {registry: "registry.example.com:5000", repository: myriarch/server}`,
		"repository with a host":  `image: {repository: registry.example.com/myriarch/myriarch}`,
		"repository with a port":  `image: {repository: "localhost:5001/myriarch/myriarch"}`,
		"one-label host and port": `image: {repository: "registry:5000/myriarch/myriarch"}`,
		"repository on localhost": `image: {repository: localhost/myriarch}`,
		"host in upper case":      `image: {repository: Registry.Example.com/myriarch/myriarch}`,
		"digest":                  `image: {digest: ` + d + `}`,
		"empty registry":          `image: {registry: ""}`,
		"ipv6 registry":           `image: {registry: "[2001:db8::1]:5000"}`,
		"global registry":         `global: {imageRegistry: mirror.example.com}`,
		"global registry null":    `global: {imageRegistry: null}`,
		"other global keys":       `global: {storageClass: fast, anything: [1, 2]}`,
		"dependency condition":    `enabled: false`,
		"values of a parent":      "enabled: true\nglobal: {imageRegistry: mirror.example.com}",
		"pull policy":             `image: {pullPolicy: Always}`,
		"pull secret":             `imagePullSecrets: [{name: registry-pull-secret}]`,
		"openshift as a string":   `openshift: {enabled: auto}`,
		"openshift true string":   `openshift: {enabled: "true"}`,
		"a map entry set to null": `podAnnotations: {legacy: null}`,
		"deleted default key":     `config: {server: {metricsListenAddr: null}}`,
		"quantity forms":          `resources: {requests: {cpu: 1, memory: 536870912, ephemeral-storage: 0.5Gi}, limits: {memory: 1Gi, cpu: 500m}}`,
		"percent budget":          `podDisruptionBudget: {minAvailable: 50%, maxUnavailable: 1}`,
		"declared token":          `enrollmentTokens: [{name: gateway, existingSecret: gateway-token, key: token, labels: {site: hq}, expiresAt: "2030-01-01T00:00:00Z"}]`,
		"generated token":         `enrollmentTokens: [{name: a}, {name: b, expiresAt: ""}]`,
		"bootstrap secret":        `bootstrapAdmin: {existingSecret: my-bootstrap, tokenKey: "", passwordHashKey: password-hash}`,
		"environment variable":    `extraEnv: [{name: HTTPS_PROXY, value: "http://proxy.example.com:3128"}, {name: X, valueFrom: {secretKeyRef: {name: s, key: k}}}]`,
		"secret environment":      `secretEnv: [{name: MYRIARCH_EXEC_AWX_TOKEN, key: awx-token, optional: true}]`,
		"ingress":                 `ingress: {enabled: true, className: nginx, hosts: [{host: fleet.example.com, paths: [{path: /, pathType: Prefix}]}], tls: [{secretName: fleet-tls, hosts: [fleet.example.com]}]}`,
		"httproute":               `httpRoute: {enabled: true, parentRefs: [{name: gw, namespace: gateway-system, sectionName: https, extra: ok}], hostnames: ["*.example.com"], timeouts: {request: 0s}}`,
		"network peers":           `networkPolicy: {ingress: {from: [{namespaceSelector: {matchLabels: {a: b}}}, {podSelector: {}}, {ipBlock: {cidr: 192.0.2.0/24, except: [192.0.2.128/25]}}]}, egress: {exceptCidrs: ["2001:db8::/32"], extraPorts: [5556], extraRules: [{to: [{ipBlock: {cidr: 198.51.100.0/24}}], ports: [{port: 5432, protocol: TCP}, {port: https}]}]}}`,
		"pod security context":    `podSecurityContext: {runAsNonRoot: true, runAsUser: 1000, fsGroup: 1000, seccompProfile: {type: RuntimeDefault}, supplementalGroups: [1]}`,
		"container security":      `securityContext: {capabilities: {drop: [ALL], add: [NET_BIND_SERVICE]}, privileged: false}`,
		"scheduling":              `tolerations: [{key: dedicated, operator: Equal, value: infra, effect: NoSchedule}]` + "\n" + `nodeSelector: {kubernetes.io/os: linux}` + "\n" + `affinity: {podAntiAffinity: {preferredDuringSchedulingIgnoredDuringExecution: []}}`,
		"topology spread":         `topologySpreadConstraints: [{maxSkew: 1, topologyKey: topology.kubernetes.io/zone, whenUnsatisfiable: DoNotSchedule, minDomains: 2}]`,
		"persistence":             `persistence: {enabled: true, storageClass: fast, accessModes: [ReadWriteMany], size: 20Gi}`,
		"service":                 `service: {type: LoadBalancer, port: 8443, annotations: {a: b}}`,
		"service monitor":         `serviceMonitor: {enabled: true, interval: 1m30s, labels: {release: prometheus}}`,
		"test pod":                `tests: {enabled: false, token: {existingSecret: helm-test-token, key: probe-token}, podAnnotations: {sidecar.istio.io/inject: "false"}, resources: {requests: {cpu: 20m}, limits: {memory: 128Mi}}}`,
		"prometheus rule":         `prometheusRule: {enabled: true, labels: {release: kube-prometheus-stack}}`,
		"openshift route":         `openshift: {enabled: true, route: {host: fleet.example.com, timeout: 1h, tls: {insecureEdgeTerminationPolicy: Allow}}}`,
		"config sections":         `config: {server: {shutdownTimeout: 1m, trustedProxies: [10.0.0.0/8]}, opamp: {maxMessageSize: 1048576, connectRateLimit: 2.5}, executors: {secrets: {awx: {env: X, allowedHosts: [awx.example.com]}}, secretEnvHosts: {A: [h.example.com]}}, notifications: {channels: [{name: ops, type: slack, urlFile: /f, events: ["rollout.*"]}]}}`,
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, validateChartValues(t, schema, yamlValues(t, []byte(text))), text)
		})
	}
}

// ------------------------------------------------------- refused values ---

func TestChartSchemaRefusesInvalidValues(t *testing.T) {
	schema := loadChartSchema(t)
	token := brand.EnrollmentTokenPrefix + strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("0123456789abcdef", 4)
	cases := []struct {
		name, values, want string
	}{
		// unknown keys
		{"unknown top-level key", `replicas: 3`, `: additional property "replicas"`},
		{"misspelled key", `imag: {tag: x}`, `: additional property "imag"`},
		{"unknown nested key", `image: {tagg: x}`, `/image: additional property "tagg"`},
		{"unknown key in a list entry", `ingress: {hosts: [{host: a.example.com, hostt: b}]}`, `/ingress/hosts/0: additional property "hostt"`},
		{"unknown config section", `config: {serverr: {}}`, `/config: additional property "serverr"`},
		{"misspelled config key", `config: {server: {listenAdr: ":80"}}`, `/config/server: additional property "listenAdr"`},
		{"unknown config key in a list entry", `config: {notifications: {channels: [{name: a, type: slack, urlfile: /f}]}}`, `/config/notifications/channels/0: additional property "urlfile"`},
		{"unknown config key in a map entry", `config: {executors: {secrets: {a: {envv: X}}}}`, `/config/executors/secrets/a: additional property "envv"`},
		{"unknown probe key", `probes: {liveness: {initialDelaySeconds: 5}}`, `/probes/liveness: additional property "initialDelaySeconds"`},
		{"unknown key in the network policy", `networkPolicy: {ingress: {from: [{namespaceSelctor: {}}]}}`, `additional property "namespaceSelctor"`},
		{"unknown key in an environment variable", `extraEnv: [{name: A, valu: b}]`, `/extraEnv/0: additional property "valu"`},

		// secrets are not values
		{"enrollment token as a value", `enrollmentTokens: [{name: a, token: ` + token + `}]`, `/enrollmentTokens/0: additional property "token"`},
		{"enrollment token as the key", `enrollmentTokens: [{name: a, key: ` + token + `}]`, `/enrollmentTokens/0/key: matches a schema that it must not match`},
		{"enrollment token as the secret", `enrollmentTokens: [{name: a, existingSecret: ` + token + `}]`, `/enrollmentTokens/0/existingSecret`},
		{"enrollment token as the bootstrap token key", `bootstrapAdmin: {existingSecret: s, tokenKey: ` + token + `}`, `/bootstrapAdmin/tokenKey`},
		{"enrollment token as the hash key", `bootstrapAdmin: {existingSecret: s, passwordHashKey: ` + token + `}`, `/bootstrapAdmin/passwordHashKey`},
		{"enrollment token as the secret environment key", `secretEnv: [{name: A, key: ` + token + `}]`, `/secretEnv/0/key`},
		{"enrollment tokens in the config block", `config: {opamp: {enrollmentTokens: []}}`, `/config/opamp: additional property "enrollmentTokens"`},
		{"database DSN", `config: {database: {dsn: "postgres://u:p@db/x"}}`, `/config/database: additional property "dsn"`},
		{"OIDC client secret", `config: {auth: {oidc: {clientSecret: s}}}`, `/config/auth/oidc: additional property "clientSecret"`},
		{"webhook secret", `config: {gitops: {webhookSecret: s}}`, `/config/gitops: additional property "webhookSecret"`},
		{"notification URL", `config: {notifications: {channels: [{name: a, type: slack, url: "https://hooks.example.com/x"}]}}`, `/config/notifications/channels/0: additional property "url"`},
		{"bootstrap admin in the config block", `config: {auth: {bootstrapAdmin: {enabled: true}}}`, `/config/auth: additional property "bootstrapAdmin"`},
		{"development login", `config: {auth: {devLogin: {enabled: true}}}`, `/config/auth: additional property "devLogin"`},
		{"development mode", `config: {dev: {seedEnrollmentToken: ` + token + `}}`, `/config: additional property "dev"`},

		// types
		{"replica count as a word", `replicaCount: two`, `/replicaCount: is string, want integer`},
		{"dependency condition as a word", `enabled: maybe`, `/enabled: is string, want boolean`},
		{"negative replica count", `replicaCount: -1`, `/replicaCount: -1 is below 0`},
		{"fractional replica count", `replicaCount: 1.5`, `/replicaCount: is number, want integer`},
		{"boolean as a word", `ingress: {enabled: "yes"}`, `/ingress/enabled: is string, want boolean`},
		// a template counts the string "false" as true, which switches the TLS check off: the switches are booleans
		{"TLS switch of the Ingress as a string", `ingress: {allowWithoutTls: "false"}`, `/ingress/allowWithoutTls: is string, want boolean`},
		{"TLS switch of the Gateway as a string", `httpRoute: {tlsTerminatedAtGateway: "false"}`, `/httpRoute/tlsTerminatedAtGateway: is string, want boolean`},
		{"list as a string", `config: {server: {trustedProxies: "10.0.0.0/8"}}`, `/config/server/trustedProxies: is string, want array`},
		{"number as a string", `config: {opamp: {maxConnectionsPerReplica: many}}`, `/config/opamp/maxConnectionsPerReplica: is string, want integer`},
		{"fractional integer setting", `config: {opamp: {maxConnectionsPerReplica: 1.5}}`, `/config/opamp/maxConnectionsPerReplica: is number, want integer`},
		{"object as a list", `podAnnotations: [a]`, `/podAnnotations: is array, want object`},
		{"annotation value as a number", `podAnnotations: {a: 1}`, `/podAnnotations/a: is integer`},
		{"environment value as a number", `extraEnv: [{name: A, value: 1}]`, `/extraEnv/0/value: is integer, want string`},
		{"port as a string", `networkPolicy: {egress: {extraPorts: [abc]}}`, `/networkPolicy/egress/extraPorts/0: is string, want integer`},
		{"port out of range", `service: {port: 70000}`, `/service/port: 70000 is above 65535`},
		{"port zero", `service: {metricsPort: 0}`, `/service/metricsPort: 0 is below 1`},
		{"null without a default", `config: {opamp: {maxMessageSize: null}}`, `/config/opamp/maxMessageSize: is null, want integer`},
		{"null in a list entry", `enrollmentTokens: [{name: a, existingSecret: null}]`, `/enrollmentTokens/0/existingSecret: is null, want string`},
		// Helm keeps a null for a key that values.yaml does not define, the chart renders it into myriarchd.yaml, and the server
		// refuses it when it starts: no entry of a map of the configuration may be null (the maps of the chart itself may)
		{"null in a list map of the configuration", `config: {executors: {secretEnvHosts: {MYRIARCH_EXEC_OLD: null}}}`, `/config/executors/secretEnvHosts/MYRIARCH_EXEC_OLD: is null, want array`},
		{"null in the file hosts of the configuration", `config: {executors: {secretFileHosts: {old.token: null}}}`, `/config/executors/secretFileHosts/old.token: is null, want array`},
		{"null for an executor secret", `config: {executors: {secrets: {awx: null}}}`, `/config/executors/secrets/awx: is null, want object`},

		// durations
		{"duration as an integer", `config: {server: {shutdownTimeout: 30}}`, `/config/server/shutdownTimeout: is integer, want string`},
		{"duration with a wrong unit", `config: {server: {shutdownTimeout: 30sec}}`, `/config/server/shutdownTimeout: "30sec" does not match`},
		{"duration in days", `config: {retention: {audit: 30d}}`, `/config/retention/audit: "30d" does not match`},
		{"duration from the environment", `config: {rollouts: {tickInterval: "${TICK}"}}`, `/config/rollouts/tickInterval: "${TICK}" does not match`},

		// formats
		{"openshift mode", `openshift: {enabled: maybe}`, `/openshift/enabled: matches none of the alternatives`},
		{"digest", `image: {digest: bogus}`, `/image/digest: "bogus" does not match`},
		{"digest of another algorithm", `image: {digest: "sha512:` + strings.Repeat("a", 64) + `"}`, `/image/digest`},
		{"tag with a space", `image: {tag: "1 2"}`, `/image/tag`},
		{"pull policy", `image: {pullPolicy: Sometimes}`, `/image/pullPolicy: Sometimes is not one of`},
		{"registry with a scheme", `image: {registry: "https://ghcr.io"}`, `/image/registry`},
		{"registry with a path", `image: {registry: ghcr.io/myriarch}`, `/image/registry`},
		{"registry with a trailing slash", `image: {registry: "ghcr.io/"}`, `/image/registry`},
		{"global registry with a scheme", `global: {imageRegistry: "https://mirror.example.com"}`, `/global/imageRegistry: matches none of the alternatives`},
		{"empty repository", `image: {repository: ""}`, `/image/repository`},
		{"repository in upper case", `image: {repository: Myriarch/Myriarch}`, `/image/repository`},
		{"first segment in upper case that is no host", `image: {repository: Myriarch/myriarch}`, `/image/repository`},
		{"localhost in upper case", `image: {repository: LOCALHOST/myriarch}`, `/image/repository`},
		{"repository with a tag", `image: {repository: "ghcr.io/myriarch/myriarch:1.0"}`, `/image/repository`},
		{"repository with a digest", `image: {repository: "ghcr.io/myriarch/myriarch@` + digest + `"}`, `/image/repository`},
		{"repository with a scheme", `image: {repository: "https://ghcr.io/myriarch/myriarch"}`, `/image/repository`},
		{"image pull secret without a name", `imagePullSecrets: [{}]`, `/imagePullSecrets/0: is missing the property name`},
		{"service type", `service: {type: ExternalName}`, `/service/type: ExternalName is not one of`},
		{"probe path", `probes: {readiness: {path: readyz}}`, `/probes/readiness/path: "readyz" does not match`},
		{"probe count", `probes: {liveness: {periodSeconds: 0}}`, `/probes/liveness/periodSeconds: 0 is below 1`},
		{"quantity", `resources: {requests: {cpu: lots}}`, `/resources/requests/cpu: matches none of the alternatives`},
		{"negative quantity", `resources: {limits: {memory: -1}}`, `/resources/limits/memory: matches none of the alternatives`},
		{"volume size", `emptyDir: {sizeLimit: big}`, `/emptyDir/sizeLimit: matches none of the alternatives`},
		{"access mode", `persistence: {accessModes: [ReadWriteAlways]}`, `/persistence/accessModes/0`},
		{"no access mode", `persistence: {accessModes: []}`, `/persistence/accessModes: has fewer than 1 items`},
		{"budget", `podDisruptionBudget: {minAvailable: half}`, `/podDisruptionBudget/minAvailable: matches none of the alternatives`},
		{"path type", `ingress: {hosts: [{host: a.example.com, paths: [{path: /, pathType: Starts}]}]}`, `/ingress/hosts/0/paths/0/pathType`},
		{"path without a slash", `ingress: {hosts: [{host: a.example.com, paths: [{path: x}]}]}`, `/ingress/hosts/0/paths/0/path`},
		{"host in upper case", `ingress: {hosts: [{host: Fleet.Example.com}]}`, `/ingress/hosts/0/host`},
		{"route without a gateway name", `httpRoute: {parentRefs: [{namespace: x}]}`, `/httpRoute/parentRefs/0: is missing the property name`},
		{"host name with an underscore", `httpRoute: {hostnames: [fleet_example.com]}`, `/httpRoute/hostnames/0`},
		{"route timeout", `httpRoute: {timeouts: {request: soon}}`, `/httpRoute/timeouts/request`},
		{"router timeout without a unit", `openshift: {route: {timeout: 3600}}`, `/openshift/route/timeout`},
		{"termination policy", `openshift: {route: {tls: {insecureEdgeTerminationPolicy: Never}}}`, `/openshift/route/tls/insecureEdgeTerminationPolicy`},
		{"scrape interval", `serviceMonitor: {interval: 30 seconds}`, `/serviceMonitor/interval`},
		{"unknown key of the test Pod", `tests: {tokenSecret: helm-test-token}`, `/tests: additional property "tokenSecret"`},
		{"unknown key of the test token", `tests: {token: {secret: helm-test-token}}`, `/tests/token: additional property "secret"`},
		{"test switch as a string", `tests: {enabled: "false"}`, `/tests/enabled: is string, want boolean`},
		{"enrollment token as the Secret of the test", `tests: {token: {existingSecret: ` + token + `}}`, `/tests/token/existingSecret`},
		{"enrollment token as the key of the test", `tests: {token: {existingSecret: helm-test-token, key: ` + token + `}}`, `/tests/token/key: matches a schema that it must not match`},
		{"empty key of the test token", `tests: {token: {key: ""}}`, `/tests/token/key`},
		{"quantity of the test Pod", `tests: {resources: {limits: {memory: lots}}}`, `/tests/resources/limits/memory: matches none of the alternatives`},
		{"annotation of the test Pod as a number", `tests: {podAnnotations: {a: 1}}`, `/tests/podAnnotations/a: is integer`},
		{"unknown key of the PrometheusRule", `prometheusRule: {rules: []}`, `/prometheusRule: additional property "rules"`},
		{"switch of the PrometheusRule as a string", `prometheusRule: {enabled: "true"}`, `/prometheusRule/enabled: is string, want boolean`},
		{"label of the PrometheusRule as a number", `prometheusRule: {labels: {release: 1}}`, `/prometheusRule/labels/release: is integer`},
		{"cidr", `networkPolicy: {egress: {exceptCidrs: [metadata]}}`, `/networkPolicy/egress/exceptCidrs/0`},
		{"peer without a cidr", `networkPolicy: {ingress: {from: [{ipBlock: {except: []}}]}}`, `: is missing the property cidr`},
		{"toleration operator", `tolerations: [{operator: Maybe}]`, `/tolerations/0/operator`},
		{"toleration effect", `tolerations: [{effect: Sometimes}]`, `/tolerations/0/effect`},
		{"seccomp profile", `podSecurityContext: {seccompProfile: {type: None}}`, `/podSecurityContext/seccompProfile/type`},
		{"negative user", `podSecurityContext: {runAsUser: -1}`, `/podSecurityContext/runAsUser`},
		{"topology skew", `topologySpreadConstraints: [{maxSkew: 0}]`, `/topologySpreadConstraints/0/maxSkew`},
		{"volume without a name", `extraVolumes: [{emptyDir: {}}]`, `/extraVolumes/0: is missing the property name`},
		{"volume mount without a path", `extraVolumeMounts: [{name: a}]`, `/extraVolumeMounts/0: is missing the property mountPath`},
		{"environment variable without a name", `extraEnv: [{value: a}]`, `/extraEnv/0: is missing the property name`},
		{"secret environment without a key", `secretEnv: [{name: A}]`, `/secretEnv/0: is missing the property key`},
		{"secret environment name", `secretEnv: [{name: "1A", key: k}]`, `/secretEnv/0/name`},
		{"secret name with an underscore", `existingSecret: my_secrets`, `/existingSecret`},
		{"empty secret name", `existingSecret: ""`, `/existingSecret`},
		{"secret key with a space", `bootstrapAdmin: {existingSecret: s, tokenKey: "a b"}`, `/bootstrapAdmin/tokenKey`},
		{"bootstrap user name", `bootstrapAdmin: {username: "not valid!"}`, `/bootstrapAdmin/username`},
		{"unknown bootstrap key", `bootstrapAdmin: {password: secret}`, `/bootstrapAdmin: additional property "password"`},
		{"token name in upper case", `enrollmentTokens: [{name: Gateway}]`, `/enrollmentTokens/0/name`},
		{"token name with an underscore", `enrollmentTokens: [{name: Not_A_Label}]`, `/enrollmentTokens/0/name`},
		{"token without a name", `enrollmentTokens: [{labels: {a: b}}]`, `/enrollmentTokens/0: is missing the property name`},
		{"token expiry", `enrollmentTokens: [{name: a, expiresAt: tomorrow}]`, `/enrollmentTokens/0/expiresAt`},
		{"token label as a number", `enrollmentTokens: [{name: a, labels: {site: 1}}]`, `/enrollmentTokens/0/labels/site`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := validateChartValues(t, schema, yamlValues(t, []byte(c.values)))
			require.NotEmpty(t, problems, "the schema accepts: %s", c.values)
			assert.Contains(t, strings.Join(problems, "\n"), c.want)
		})
	}
	assert.GreaterOrEqual(t, len(cases), 90)
}

// TestChartSchemaTokenPatternIsTheBrandPrefix: the schema refuses a Secret reference that has the form of an enrollment token, and
// the prefix in the pattern is the one of internal/brand.
func TestChartSchemaTokenPatternIsTheBrandPrefix(t *testing.T) {
	defs := loadChartSchema(t)["$defs"].(map[string]any)
	for _, name := range []string{"secretKey", "secretKeyOrEmpty"} {
		not := defs[name].(map[string]any)["not"].(map[string]any)
		assert.Equal(t, "^"+brand.EnrollmentTokenPrefix, not["pattern"], name)
	}
}

// TestChartSchemaStaysOutOfThePackage: only values.schema.json belongs in the packaged chart. The hand-written source is read by
// make docs-gen in the repository and is of no use to Helm.
func TestChartSchemaStaysOutOfThePackage(t *testing.T) {
	ignore := string(readDeploy(t, "helm/myriarch/.helmignore"))
	assert.Contains(t, strings.Split(ignore, "\n"), "values.schema.source.yaml")
	assert.FileExists(t, filepath.Join(deployDir, "helm/myriarch/values.schema.source.yaml"))
	assert.FileExists(t, filepath.Join(deployDir, chartSchemaPath))
}
