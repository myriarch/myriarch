// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
)

// readSecretFiles reads a *File setting only while its feature is active. The
// tests call it directly: the surrounding validation has its own rules.

func TestReadSecretFilesOIDCClientSecretOnlyWhileEnabled(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "oidc-client-secret")

	cfg := config.Default()
	cfg.Auth.OIDC.Enabled = false
	cfg.Auth.OIDC.ClientSecret = "inline-value"
	cfg.Auth.OIDC.ClientSecretFile = missing
	require.NoError(t, readSecretFiles(&cfg), "an inactive feature must not need its file")
	assert.Equal(t, "inline-value", cfg.Auth.OIDC.ClientSecret, "the inline value stays untouched")

	cfg.Auth.OIDC.Enabled = true
	err := readSecretFiles(&cfg)
	require.Error(t, err)
	var sf *secretFileError
	require.ErrorAs(t, err, &sf)
	assert.Equal(t, "auth.oidc.clientSecretFile", sf.setting)
	assert.Contains(t, sf.msg, "cannot read "+missing)

	// an existing file wins over the inline value while enabled
	require.NoError(t, os.WriteFile(missing, []byte(" from-file \n"), 0o600))
	require.NoError(t, readSecretFiles(&cfg))
	assert.Equal(t, "from-file", cfg.Auth.OIDC.ClientSecret)
}

func TestReadSecretFilesGitOpsWebhookSecretOnlyWhileEnabled(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gitops-webhook-secret")

	cfg := config.Default()
	cfg.GitOps.Enabled = false
	cfg.GitOps.WebhookSecretFile = missing
	require.NoError(t, readSecretFiles(&cfg))
	assert.Empty(t, cfg.GitOps.WebhookSecret)

	cfg.GitOps.Enabled = true
	err := readSecretFiles(&cfg)
	require.Error(t, err)
	var sf *secretFileError
	require.ErrorAs(t, err, &sf)
	assert.Equal(t, "gitops.webhookSecretFile", sf.setting)

	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(empty, []byte("\n"), 0o600))
	cfg.GitOps.WebhookSecretFile = empty
	err = readSecretFiles(&cfg)
	require.ErrorAs(t, err, &sf)
	assert.Contains(t, sf.msg, "is empty")
}

func TestReadSecretFilesDatabaseDSNFileIsAlwaysRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "database-dsn")

	cfg := config.Default()
	cfg.Auth.OIDC.Enabled = false
	cfg.GitOps.Enabled = false
	cfg.Database.DSNFile = missing
	err := readSecretFiles(&cfg)
	require.Error(t, err)
	var sf *secretFileError
	require.ErrorAs(t, err, &sf)
	assert.Equal(t, "database.dsnFile", sf.setting)

	require.NoError(t, os.WriteFile(missing, []byte("postgres://db.example.test/app\n"), 0o600))
	require.NoError(t, readSecretFiles(&cfg))
	assert.Equal(t, "postgres://db.example.test/app", cfg.Database.DSN)
}
