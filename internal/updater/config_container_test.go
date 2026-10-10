// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const containerYAML = `
server:
  endpoint: ws://myriarch:8080/v1/opamp
  token: myre_x
  allowInsecureTransport: true
supervisor:
  storageDir: /var/lib/otelcol/supervisor
  serviceName: opampsupervisor
collector:
  installDir: /var/lib/myriarch/collector
  activation: symlink
container:
  supervisorCommand: ["/usr/local/bin/opampsupervisor", "--config=/etc/opampsupervisor/supervisor.yaml"]
  seedBinary: /usr/local/bin/otelcol-contrib
stateDir: /var/lib/myriarch/updater
`

// withContainerLine appends a key to the container section of containerYAML.
func withContainerLine(line string) string {
	return strings.Replace(containerYAML, "  seedBinary: /usr/local/bin/otelcol-contrib\n", "  seedBinary: /usr/local/bin/otelcol-contrib\n"+line, 1)
}

func TestContainerConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig([]byte(containerYAML))
	require.NoError(t, err)
	require.NoError(t, cfg.ValidateContainer())
	c := cfg.Container
	assert.Equal(t, []string{"/usr/local/bin/opampsupervisor", "--config=/etc/opampsupervisor/supervisor.yaml"}, c.SupervisorCommand)
	assert.Equal(t, SeedPolicyNewest, c.SeedPolicy)
	assert.Equal(t, 30*time.Second, c.StopTimeout.D())
	assert.Equal(t, 2*time.Second, c.RestartDelay.D())
	assert.Equal(t, 5, c.MaxRestarts)
	assert.Equal(t, 10*time.Minute, c.RestartWindow.D())
	assert.Equal(t, "0.0.0.0:13135", c.HealthListen)
	// default download allowlist: the artifacts of the myriarchd we talk to
	assert.Equal(t, []string{"http://myriarch:8080/api/v1/artifacts/"}, cfg.Update.AllowedURLPrefixes)
}

func TestContainerConfigIsOnlyValidatedForTheContainerCommand(t *testing.T) {
	// the service mode accepts a config without the container section ...
	cfg, err := ParseConfig([]byte("server: {endpoint: 'wss://fleet.example.com/v1/opamp', token: x}\n"))
	require.NoError(t, err)
	// ... and the container command refuses it
	err = cfg.ValidateContainer()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "container.supervisorCommand is required")
	assert.Contains(t, err.Error(), "container.seedBinary is required")
}

func TestContainerConfigStrictAndValidated(t *testing.T) {
	_, err := ParseConfig([]byte(containerYAML + "container2: {}\n"))
	require.Error(t, err, "unknown top-level key")
	_, err = ParseConfig([]byte(withContainerLine("  seedPolcy: image\n")))
	require.Error(t, err, "typo inside the container section")

	for name, mod := range map[string]func(c *Config){
		"seedPolicy": func(c *Config) { c.Container.SeedPolicy = "oldest" },
		"stop":       func(c *Config) { c.Container.StopTimeout = 0 },
		"restarts":   func(c *Config) { c.Container.MaxRestarts = 0 },
		"listen":     func(c *Config) { c.Container.HealthListen = "13135" },
		"activation": func(c *Config) { c.Collector.Activation = "copy" },
	} {
		cfg, err := ParseConfig([]byte(containerYAML))
		require.NoError(t, err, name)
		mod(&cfg)
		assert.Error(t, cfg.ValidateContainer(), name)
	}

	cfg, err := ParseConfig([]byte(withContainerLine("  healthListen: \"\"\n")))
	require.NoError(t, err)
	assert.Empty(t, cfg.Container.HealthListen, "an explicit empty value switches the probe server off")
	assert.NoError(t, cfg.ValidateContainer())
}
