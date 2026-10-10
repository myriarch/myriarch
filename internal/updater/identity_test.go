// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadSupervisorInstanceID(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "persistent_state.yaml")

	id, err := readSupervisorInstanceID(p)
	require.NoError(t, err, "missing file is not an error (created at first supervisor start)")
	assert.Empty(t, id)

	want := "0198c9a4-1b6f-7c2e-9a41-3d5e6f708192"
	// real supervisor format: instance_id plus last_remote_config_status
	require.NoError(t, os.WriteFile(p, []byte("instance_id: "+want+"\nlast_remote_config_status:\n  status: 1\n  last_remote_config_hash: 0a0b\n"), 0o600))
	id, err = readSupervisorInstanceID(p)
	require.NoError(t, err)
	assert.Equal(t, want, id)

	require.NoError(t, os.WriteFile(p, []byte("instance_id: not-a-uuid\n"), 0o600))
	_, err = readSupervisorInstanceID(p)
	require.Error(t, err)

	require.NoError(t, os.WriteFile(p, []byte(":::not yaml"), 0o600))
	_, err = readSupervisorInstanceID(p)
	require.Error(t, err)

	require.NoError(t, os.WriteFile(p, []byte("last_remote_config_status: {}\n"), 0o600))
	id, err = readSupervisorInstanceID(p)
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestBuildDescription(t *testing.T) {
	cfg := defaultConfigFor("linux")
	self := uuid.MustParse("0198c9a4-1b6f-7c2e-9a41-3d5e6f708192")
	d := buildDescription("1.2.3", self, cfg, "")
	ident := map[string]string{}
	for _, kv := range d.IdentifyingAttributes {
		ident[kv.Key] = kv.Value.GetStringValue()
	}
	assert.Equal(t, map[string]string{
		"service.name": "myriarch-updater", "service.version": "1.2.3", "service.instance.id": self.String(),
	}, ident)
	non := map[string]string{}
	for _, kv := range d.NonIdentifyingAttributes {
		non[kv.Key] = kv.Value.GetStringValue()
	}
	assert.Contains(t, non, "os.type")
	assert.Contains(t, non, "host.arch")
	assert.Contains(t, non, "host.name")
	assert.Equal(t, "otelcol-supervisor", non["dev.myriarch.updater.supervisor_service"])
	assert.Equal(t, "binary", non["dev.myriarch.updater.install_method"])
	assert.NotContains(t, non, "dev.myriarch.collector.instance_uid", "omitted until the supervisor state exists")

	d = buildDescription("1.2.3", self, cfg, "abc")
	non = map[string]string{}
	for _, kv := range d.NonIdentifyingAttributes {
		non[kv.Key] = kv.Value.GetStringValue()
	}
	assert.Equal(t, "abc", non["dev.myriarch.collector.instance_uid"])
}

func TestInstanceUIDPersistence(t *testing.T) {
	dir := t.TempDir()
	a, err := loadOrCreateInstanceUID(dir)
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), a.Version(), "UUIDv7")
	b, err := loadOrCreateInstanceUID(dir)
	require.NoError(t, err)
	assert.Equal(t, a, b, "stable across restarts")
	// corrupt file -> regenerate instead of crash
	require.NoError(t, os.WriteFile(filepath.Join(dir, "instance_uid"), []byte("garbage"), 0o600))
	c, err := loadOrCreateInstanceUID(dir)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, c)
	// server assigned uid is persisted
	n := uuid.New()
	require.NoError(t, saveInstanceUID(dir, n))
	d, _ := loadOrCreateInstanceUID(dir)
	assert.Equal(t, n, d)
}
