// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The values schema of the collector chart (deploy/helm/myriarch-collector/values.schema.json) is converted from
// values.schema.source.yaml by make docs-gen (internal/refdoc). These tests check the result from the outside, as
// deploy_chart_schema_test.go does for the server chart: the schema is strict, it covers every value that the chart ships and reads,
// it offers no secret, it accepts the shipped examples and it refuses what it should. The two upstream images are pinned as in
// ci/versions.env.

const collectorChart = "helm/myriarch-collector"

func loadCollectorSchema(t *testing.T) map[string]any {
	t.Helper()
	schema, ok := mustJSON(t, string(readDeploy(t, collectorChart+"/values.schema.json"))).(map[string]any)
	require.True(t, ok, "the schema is a JSON object")
	return schema
}

// collectorValues returns what Helm validates for the default values and the given values files, in order.
func collectorValues(t *testing.T, overlays ...map[string]any) map[string]any {
	t.Helper()
	defaults := yamlValues(t, readDeploy(t, collectorChart+"/values.yaml"))
	user := map[string]any{}
	for _, o := range overlays {
		user = mergeValues(user, o)
	}
	coalesceValues(user, defaults)
	return user
}

// openCollectorObjects are the objects of the schema that accept keys beyond the ones they list: Kubernetes API objects that the chart
// passes through and the API server validates, and the values of a parent chart.
var openCollectorObjects = map[string]string{
	"/properties/global":                                       "values of a parent chart",
	"/properties/extraEnvFrom/items":                           "a Kubernetes environment source",
	"/properties/extraVolumes/items":                           "a Kubernetes volume",
	"/properties/extraVolumeMounts/items":                      "a Kubernetes volume mount",
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

func TestCollectorChartSchemaIsStrict(t *testing.T) {
	schema := loadCollectorSchema(t)
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
				assert.Contains(t, openCollectorObjects, ptr, "%s accepts any key: add it to openCollectorObjects with the reason, or close it", ptr)
			}
		}
		for rel, sub := range subschemas(m) {
			walk(sub, ptr+"/"+rel)
		}
	}
	walk(schema, "")
	for ptr, why := range openCollectorObjects {
		assert.True(t, open[ptr], "openCollectorObjects lists %s (%s), which is not an open object of the schema", ptr, why)
	}
}

func TestCollectorChartSchemaCoversTheValuesFile(t *testing.T) {
	schema := loadCollectorSchema(t)
	var paths [][]string
	valuePaths(yamlValues(t, readDeploy(t, collectorChart+"/values.yaml")), nil, &paths)
	require.Greater(t, len(paths), 80)
	var missing []string
	for _, p := range paths {
		if !schemaHasPath(schema, p) {
			missing = append(missing, strings.Join(p, "."))
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "values.yaml has keys that values.schema.source.yaml does not list: add them (and run make docs-gen)")
}

func TestCollectorChartSchemaCoversTheValuesTheTemplatesRead(t *testing.T) {
	schema := loadCollectorSchema(t)
	read := map[string][]string{}
	var files []string
	require.NoError(t, filepath.WalkDir(filepath.Join(deployDir, collectorChart, "templates"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return err
	}))
	require.NotEmpty(t, files)
	for _, f := range files {
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
	require.Greater(t, len(read), 40, "the scan of the templates looks broken")
	var missing []string
	for path, in := range read {
		if !schemaHasPath(schema, strings.Split(path, ".")) {
			missing = append(missing, fmt.Sprintf(".Values.%s (%s)", path, in[0]))
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "a template reads a value that the schema does not list, so the schema would refuse the value")
	assert.Contains(t, read, "global", "the image helper reads global.imageRegistry")
	assert.Contains(t, read, "server.allowInsecureTransport", "the guard for ws:// reads the opt-in")
	assert.Contains(t, read, "containerMode.trustedRoot.existingConfigMap")
}

// TestCollectorChartOffersNoSecret: the enrollment token is read from a Secret and is never a value. No setting of the schema is named
// like a secret, except the references that name one and the switch for the API token.
func TestCollectorChartOffersNoSecret(t *testing.T) {
	var paths [][]string
	valuePaths(yamlValues(t, readDeploy(t, collectorChart+"/values.yaml")), nil, &paths)
	allowed := map[string]string{
		"enrollment":                                  "the reference to the Secret",
		"enrollment.existingSecret":                   "the name of a Secret",
		"serviceAccount.automountToken":               "a switch, not a value",
		"imagePullSecrets":                            "the names of pull Secrets",
		"server.tls.existingSecret":                   "the name of a Secret",
		"containerMode.trustedRoot":                   "the trust anchor for signatures, public",
		"containerMode.trustedRoot.key":               "the key of a public ConfigMap",
		"containerMode.trustedRoot.existingConfigMap": "the name of a ConfigMap",
	}
	secretWord := regexp.MustCompile(`(?i)token|secret|password|dsn|credential|passphrase|bearer|apikey`)
	for _, p := range paths {
		path := strings.Join(p, ".")
		if secretWord.MatchString(p[len(p)-1]) || (p[len(p)-1] == "key" && len(p) > 1 && secretWord.MatchString(p[len(p)-2])) {
			assert.Contains(t, allowed, path, "%s looks like a secret value: read it from a Secret, or list it with the reason", path)
		}
	}
	assert.NotContains(t, strings.ToLower(string(readDeploy(t, collectorChart+"/values.yaml"))), "myre_", "no enrollment token in values.yaml")
}

// TestCollectorChartPinsTheUpstreamImages: the tag and the digest of the two upstream images are the reviewed ones of ci/versions.env,
// and the chart does not pin the updater (the release writes its digest).
func TestCollectorChartPinsTheUpstreamImages(t *testing.T) {
	env := map[string]string{}
	for _, line := range strings.Split(readRepoFile(t, "ci/versions.env"), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			env[k] = v
		}
	}
	values := yamlValues(t, readDeploy(t, collectorChart+"/values.yaml"))
	images := values["images"].(map[string]any)
	for name, pin := range map[string]string{"collector": "OTELCOL_CONTRIB_IMAGE", "supervisor": "OTELCOL_SUPERVISOR_IMAGE"} {
		img := images[name].(map[string]any)
		repoTag, digest, ok := strings.Cut(env[pin], "@")
		require.True(t, ok, "%s of ci/versions.env has a digest", pin)
		repo, tag, _ := strings.Cut(repoTag, ":")
		assert.Equal(t, repo, img["repository"], "images.%s.repository", name)
		assert.Equal(t, tag, img["tag"], "images.%s.tag is %s of ci/versions.env", name, pin)
		assert.Equal(t, digest, img["digest"], "images.%s.digest is %s of ci/versions.env", name, pin)
		assert.Equal(t, env["OTELCOL_VERSION"], img["tag"], "the tag is OTELCOL_VERSION")
	}
	updater := images["updater"].(map[string]any)
	assert.Equal(t, "", updater["digest"], "the repository keeps the digest of the updater empty; the release writes it")
	assert.Equal(t, "myriarch/myriarch-updater", updater["repository"])

	chartYAML := string(readDeploy(t, collectorChart+"/Chart.yaml"))
	for _, pin := range []string{"OTELCOL_CONTRIB_IMAGE", "OTELCOL_SUPERVISOR_IMAGE"} {
		_, digest, _ := strings.Cut(env[pin], "@")
		assert.Contains(t, chartYAML, "@"+digest, "the artifacthub.io/images annotation names the digest of %s", pin)
	}
	assert.Contains(t, chartYAML, "image: ghcr.io/myriarch/myriarch-updater\n", "the placeholder that the release completes")
}

func TestCollectorChartSchemaAcceptsTheShippedValues(t *testing.T) {
	schema := loadCollectorSchema(t)
	examples, err := filepath.Glob(filepath.Join(deployDir, collectorChart, "examples", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, examples)
	cases := map[string][]map[string]any{"the defaults": {}}
	for _, f := range examples {
		cases[filepath.Base(f)] = []map[string]any{yamlValues(t, mustRead(t, f))}
	}
	for name, overlays := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, newSchemaValidator(schema).Validate(collectorValues(t, overlays...)))
		})
	}
}

func TestCollectorChartSchemaAcceptsAndRefusesValues(t *testing.T) {
	schema := loadCollectorSchema(t)
	digest := "sha256:" + strings.Repeat("0123456789abcdef", 4)
	accept := map[string]string{
		"a daemonset":              `mode: daemonset`,
		"an insecure endpoint":     `server: {endpoint: "ws://myriarch.myriarch.svc:8080/v1/opamp", allowInsecureTransport: true}`,
		"a mirror":                 `global: {imageRegistry: "mirror.example.com:5000"}`,
		"a null mirror":            `global: {imageRegistry: null}`,
		"an updater digest":        `images: {updater: {digest: "` + digest + `"}}`,
		"a quantity as a number":   `resources: {requests: {cpu: 1}}`,
		"a percentage":             `updateStrategy: {maxUnavailable: 25%}`,
		"another port":             `ports: [{name: syslog, port: 5140, protocol: UDP}]`,
		"openshift auto":           `openshift: {enabled: auto}`,
		"a CA from a Secret":       `server: {tls: {existingSecret: fleet-ca, key: ca.crt}}`,
		"extra environment source": `extraEnvFrom: [{secretRef: {name: backend}}]`,
	}
	for name, values := range accept {
		t.Run("accepts "+name, func(t *testing.T) {
			assert.Empty(t, newSchemaValidator(schema).Validate(collectorValues(t, yamlValues(t, []byte(values)))))
		})
	}
	refuse := []struct{ name, values, want string }{
		{"unknown top-level key", `replicas: 3`, `additional property "replicas"`},
		{"misspelled nested key", `server: {endpont: wss://a.example.com}`, `/server: additional property "endpont"`},
		{"an enrollment token as a key", `enrollment: {token: x}`, `/enrollment: additional property "token"`},
		{"a bad mode", `mode: deployment`, `/mode`},
		{"an endpoint that is not a web socket", `server: {endpoint: "https://fleet.example.com/v1/opamp"}`, `/server/endpoint`},
		{"a bad digest", `images: {updater: {digest: "sha256:abc"}}`, `/images/updater/digest`},
		{"a bad pull policy", `images: {updater: {pullPolicy: Sometimes}}`, `/images/updater/pullPolicy`},
		{"an enrollment key that is a token", `enrollment: {key: myre_abcdef}`, `/enrollment/key`},
		{"an empty Secret name", `enrollment: {existingSecret: ""}`, `/enrollment/existingSecret`},
		{"a relative host path", `persistence: {hostPath: {path: var/lib/x}}`, `/persistence/hostPath/path`},
		{"a port with the name of a health port", `ports: [{name: health, port: 9000}]`, `/ports/0/name`},
		{"a port out of range", `ports: [{name: x, port: 70000}]`, `/ports/0/port`},
		{"fewer than two versions", `containerMode: {keepVersions: 1}`, `/containerMode/keepVersions`},
		{"a bad seed policy", `containerMode: {seedPolicy: always}`, `/containerMode/seedPolicy`},
		{"a duration without a unit", `containerMode: {stopTimeout: 30}`, `/containerMode/stopTimeout`},
		{"an unknown openshift mode", `openshift: {enabled: maybe}`, `/openshift/enabled`},
		{"an unknown key in the network policy", `networkPolicy: {ingress: {from: [{namespaceSelctor: {}}]}}`, `additional property "namespaceSelctor"`},
	}
	for _, c := range refuse {
		t.Run("refuses "+c.name, func(t *testing.T) {
			errs := newSchemaValidator(schema).Validate(collectorValues(t, yamlValues(t, []byte(c.values))))
			require.NotEmpty(t, errs, c.values)
			assert.Contains(t, strings.Join(errs, "\n"), c.want)
		})
	}
}

func TestCollectorChartSchemaStaysOutOfThePackage(t *testing.T) {
	ignore := string(readDeploy(t, collectorChart+"/.helmignore"))
	assert.Contains(t, ignore, "values.schema.source.yaml")
	assert.NotContains(t, ignore, "values.schema.json")
}
