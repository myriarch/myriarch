// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package updater

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func captureLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestLooseTokenFileIsReported(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tok, []byte("myre_secret-that-must-not-be-logged-0123456789\n"), 0o600))
	for name, tc := range map[string]struct {
		mode os.FileMode
		warn bool
	}{
		"owner only": {0o600, false}, "group readable": {0o640, false}, "owner and group read only": {0o440, false},
		"group writable": {0o660, true}, "world readable": {0o644, true}, "world writable": {0o666, true},
	} {
		require.NoError(t, os.Chmod(tok, tc.mode))
		cfg := DefaultConfig()
		cfg.Server.TokenFile = tok
		log, buf := captureLog()
		warnLooseSecrets(log, cfg)
		out := buf.String()
		assert.Equal(t, tc.warn, bytes.Contains([]byte(out), []byte("server.tokenFile")), "%s: %s", name, out)
		assert.NotContains(t, out, "must-not-be-logged", name)
	}
}

func TestInlineTokenInAReadableConfigFileIsReported(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "updater.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("server: {endpoint: wss://x/v1/opamp, token: myre_inline-0123456789}\n"), 0o600))
	cfg, err := LoadConfig(cfgFile)
	require.NoError(t, err)

	log, buf := captureLog()
	warnLooseSecrets(log, cfg)
	assert.Empty(t, buf.String(), "a private configuration file is fine")

	require.NoError(t, os.Chmod(cfgFile, 0o644))
	log, buf = captureLog()
	warnLooseSecrets(log, cfg)
	assert.Contains(t, buf.String(), "server.token")
	assert.NotContains(t, buf.String(), "myre_inline")
}
