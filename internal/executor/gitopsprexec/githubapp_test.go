// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitopsprexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor/gitopsprexec/gitopsprtest"
)

func TestGitHubAppKeyAndTokenStayOutOfLogs(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.pem"), keyPEM, 0o600))

	d := gitopsprtest.NewGitHub(t)
	clk := newClock()
	d.EnableApp(&key.PublicKey, "12345", clk.Now, time.Hour)
	f := &fixture{name: "github", d: d, file: "values.yaml", content: valuesYAML, clk: clk}
	d.SetFile("main", f.path("edge-1"), valuesYAML)
	def := newDef("github", d.URL(), "values.yaml", "image.tag", "tag")
	def.GitOpsPR.TokenEnv, def.GitOpsPR.AppID, def.GitOpsPR.PrivateKeyFile = "", "12345", "app.pem"
	f.def = def

	var logs bytes.Buffer
	e := f.exec(t, &logs, func(c *config.ExecutorsConfig) { c.SecretDir = dir })
	assert.Empty(t, e.Validate(domain.UpdateSpec{ExecutorRef: "pr"}, def))

	res, err := e.Start(context.Background(), f.req(uuid.New(), target("edge-1")))
	require.NoError(t, err)
	require.True(t, res[0].Started, res[0].Error)
	assert.Equal(t, 1, d.AppTokenRequests())

	_, err = e.Start(context.Background(), f.req(uuid.New(), target("edge-2")))
	require.NoError(t, err)
	assert.Equal(t, 1, d.AppTokenRequests(), "the installation token is reused")

	clk.Advance(56 * time.Minute)
	_, err = e.Start(context.Background(), f.req(uuid.New(), target("edge-1")))
	require.NoError(t, err)
	assert.Equal(t, 2, d.AppTokenRequests(), "the token is renewed shortly before it expires")

	out := logs.String()
	assert.NotEmpty(t, out)
	assert.NotContains(t, out, "ghs_install_token_")
	assert.NotContains(t, out, "PRIVATE KEY")
	assert.NotContains(t, out, string(pem.EncodeToMemory(&pem.Block{Type: "X", Bytes: key.D.Bytes()})))

	// an unusable key file names the setting, not the content
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.pem"), []byte("not a key"), 0o600))
	def.GitOpsPR.PrivateKeyFile = "bad.pem"
	clk.Advance(2 * time.Hour)
	_, err = e.Start(context.Background(), f.req(uuid.New(), target("edge-1")))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not a key")
}
