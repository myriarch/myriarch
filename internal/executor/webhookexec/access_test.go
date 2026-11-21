// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package webhookexec

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
)

func TestAccessFailsClosed(t *testing.T) {
	const secretValue = "super-secret-value-0123456789"
	env := map[string]string{"MYRIARCH_EXEC_TOK": secretValue, "OTHER_TOK": secretValue}
	getenv := func(k string) string { return env[k] }
	api := &url.URL{Scheme: "https", Host: "api.example.com"}
	plain := &url.URL{Scheme: "http", Host: "api.example.com"}

	a := NewAccess(config.ExecutorsConfig{}, getenv)
	err := a.HostAllowed(api)
	require.Error(t, err, "an empty allow list refuses")

	a = NewAccess(config.ExecutorsConfig{AllowedWebhookHosts: []string{"api.example.com"}}, getenv)
	require.NoError(t, a.HostAllowed(api))
	require.Error(t, a.HostAllowed(plain), "http needs the explicit http:// entry")
	require.Error(t, a.HostAllowed(&url.URL{Scheme: "https", Host: "other.example.com"}))

	_, err = a.Secret("OTHER_TOK", "", "gitops-pr token", api)
	require.Error(t, err, "an environment variable without the prefix is refused")
	assert.NotContains(t, err.Error(), secretValue)

	got, err := a.Secret("MYRIARCH_EXEC_TOK", "", "gitops-pr token", api)
	require.NoError(t, err)
	assert.Equal(t, secretValue, string(got))

	strict := NewAccess(config.ExecutorsConfig{AllowedWebhookHosts: []string{"api.example.com"}, RequireSecretHosts: true}, getenv)
	_, err = strict.Secret("MYRIARCH_EXEC_TOK", "", "gitops-pr token", api)
	require.Error(t, err, "an unbound reference is refused when secret hosts are required")
	assert.NotContains(t, err.Error(), secretValue)

	bound := NewAccess(config.ExecutorsConfig{AllowedWebhookHosts: []string{"api.example.com"}, RequireSecretHosts: true,
		SecretEnvHosts: map[string][]string{"MYRIARCH_EXEC_TOK": {"api.example.com"}}}, getenv)
	_, err = bound.Secret("MYRIARCH_EXEC_TOK", "", "gitops-pr token", api)
	require.NoError(t, err)
	_, err = bound.Secret("MYRIARCH_EXEC_TOK", "", "gitops-pr token", &url.URL{Scheme: "https", Host: "evil.example.com"})
	require.Error(t, err)

	_, err = a.Secret("", "../etc/passwd", "gitops-pr token", api)
	require.Error(t, err, "files need executors.secretDir")
}
