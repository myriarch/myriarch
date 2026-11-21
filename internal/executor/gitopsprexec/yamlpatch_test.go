// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitopsprexec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

const helmValues = `# values of the gateway
replicaCount: 2   # keep
image:
  repository: registry.example.com/otel/collector
  tag: 0.161.0 # the version
resources:
  limits: {cpu: 1}
`

const manifest = `apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
        - name: sidecar
          image: example.com/side:1
        - name: collector
          image: registry.example.com:5000/otel/col:0.161.0
`

// diffLines returns the indexes of the lines that differ.
func diffLines(t *testing.T, a, b string) []int {
	t.Helper()
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	require.Equal(t, len(la), len(lb))
	var d []int
	for i := range la {
		if la[i] != lb[i] {
			d = append(d, i)
		}
	}
	return d
}

func TestSetImageScalar(t *testing.T) {
	t.Run("helm tag changes one line", func(t *testing.T) {
		out, old, changed, err := setImageScalar([]byte(helmValues), "image.tag", "tag", "0.162.0")
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, "0.161.0", old)
		assert.Equal(t, []int{4}, diffLines(t, helmValues, string(out)))
		assert.Contains(t, string(out), "  tag: 0.162.0 # the version\n")
	})
	t.Run("manifest image with registry port", func(t *testing.T) {
		out, old, changed, err := setImageScalar([]byte(manifest), "spec.template.spec.containers[1].image", "image", "0.162.0")
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, "registry.example.com:5000/otel/col:0.161.0", old)
		assert.Equal(t, []int{9}, diffLines(t, manifest, string(out)))
		assert.Contains(t, string(out), "image: registry.example.com:5000/otel/col:0.162.0\n")
	})
	t.Run("quoted styles are kept", func(t *testing.T) {
		for _, q := range []string{`"`, `'`} {
			src := "a:\n  image: " + q + "example.com/c:1.0.0" + q + " # c\nb: 1\n"
			out, _, changed, err := setImageScalar([]byte(src), "a.image", "image", "1.1.0")
			require.NoError(t, err)
			assert.True(t, changed)
			assert.Equal(t, "a:\n  image: "+q+"example.com/c:1.1.0"+q+" # c\nb: 1\n", string(out))
		}
	})
	t.Run("CRLF stays", func(t *testing.T) {
		src := strings.ReplaceAll(helmValues, "\n", "\r\n")
		out, _, changed, err := setImageScalar([]byte(src), "image.tag", "tag", "0.162.0")
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, strings.ReplaceAll(strings.Replace(helmValues, "0.161.0 #", "0.162.0 #", 1), "\n", "\r\n"), string(out))
	})
	t.Run("image without tag", func(t *testing.T) {
		out, _, changed, err := setImageScalar([]byte("image: example.com/otel/col\n"), "image", "image", "0.162.0")
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, "image: example.com/otel/col:0.162.0\n", string(out))
	})
	t.Run("already at version", func(t *testing.T) {
		in := []byte(helmValues)
		out, _, changed, err := setImageScalar(in, "image.tag", "tag", "0.161.0")
		require.NoError(t, err)
		assert.False(t, changed)
		assert.Equal(t, in, out)
	})
	errs := map[string]struct{ src, field, format, version, want string }{
		"digest":        {"image: example.com/c@sha256:abc\n", "image", "image", "1.0.0", "digest"},
		"missing":       {helmValues, "image.nope", "tag", "1.0.0", "not found"},
		"block scalar":  {"image: |\n  example.com/c:1\n", "image", "image", "1.0.0", "block scalar"},
		"not scalar":    {helmValues, "image", "tag", "1.0.0", "not a scalar"},
		"two documents": {"a: 1\n---\nb: 2\n", "a", "tag", "1.0.0", "more than one"},
		"empty":         {"", "a", "tag", "1.0.0", "empty"},
		"bad version":   {helmValues, "image.tag", "tag", "1.0.0\nx: y", "not usable"},
		"bad index":     {manifest, "spec.template.spec.containers[7].image", "image", "1.0.0", "not found"},
	}
	for name, c := range errs {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := setImageScalar([]byte(c.src), c.field, c.format, c.version)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestRenderPath(t *testing.T) {
	attrs := domain.AttributeView{"k8s.cluster.name": "edge-1", "evil": "..", "slash": "a/b"}
	got, err := renderPath(`clusters/[[ attr "k8s.cluster.name" ]]/values.yaml`, attrs)
	require.NoError(t, err)
	assert.Equal(t, "clusters/edge-1/values.yaml", got)

	_, err = renderPath(`clusters/[[ attr "absent" ]]/values.yaml`, attrs)
	require.ErrorContains(t, err, "is missing")
	_, err = renderPath(`[[ attr "evil" ]]/values.yaml`, attrs)
	require.Error(t, err)
	_, err = renderPath(`[[ attr "slash" ]]/values.yaml`, attrs)
	require.Error(t, err)
	_, err = renderPath(`../values.yaml`, attrs)
	require.Error(t, err)
}
