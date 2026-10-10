// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
)

// A disabled syncer reads no secret or CA file; the webhook endpoint answers
// 404 even with an inline secret. An enabled one still fails at startup.
func TestNewSyncerDisabledReadsNoFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := config.GitOpsConfig{
		Enabled:           false,
		WebhookSecret:     "inline-secret-ignored-while-disabled",
		WebhookSecretFile: filepath.Join(dir, "missing-webhook-secret"),
		Auth:              config.GitAuthConfig{CAFile: filepath.Join(dir, "missing-ca.pem")},
	}
	// with an HTTP client the CA file would be read for an enabled syncer
	// (the client is not installed globally while disabled)
	s, err := NewSyncer(cfg, Deps{HTTPClient: &http.Client{}})
	require.NoError(t, err)

	w := webhookRequest(t, s, func(r *http.Request, body []byte) {
		r.Header.Set("X-Gitlab-Token", cfg.WebhookSecret)
	})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestNewSyncerEnabledStillFailsOnMissingFiles(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t)

	cfg := f.cfg
	cfg.WebhookSecret = ""
	cfg.WebhookSecretFile = filepath.Join(dir, "missing-webhook-secret")
	_, err := NewSyncer(cfg, Deps{}) // no HTTP client: nothing is installed globally
	require.Error(t, err)
	assert.Contains(t, err.Error(), "webhook secret file")

	cfg = f.cfg
	cfg.Auth.CAFile = filepath.Join(dir, "missing-ca.pem")
	_, err = NewSyncer(cfg, Deps{HTTPClient: &http.Client{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gitops.auth.caFile")
}
