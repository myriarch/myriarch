// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestNormalizeServerEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"ws://myriarch:8080/v1/opamp":               "ws://myriarch:8080",
		"wss://fleet.example.com/v1/opamp":          "wss://fleet.example.com:443",
		"ws://fleet.example.com/v1/opamp":           "ws://fleet.example.com:80",
		"https://user:secret@Fleet.Example.com/x?q": "https://fleet.example.com:443",
		"http://127.0.0.1:18090":                    "http://127.0.0.1:18090",
		"ws://[::1]:8080/v1/opamp":                  "ws://[::1]:8080",
		"":                                          "",
		"://":                                       "",
		"ftp://host/x":                              "",
	} {
		assert.Equal(t, want, normalizeServerEndpoint(in), in)
	}
}

func TestBuildDescriptionContainerAttributes(t *testing.T) {
	cfg := defaultConfigFor("linux")
	cfg.Server.Endpoint = "ws://myriarch:8080/v1/opamp"
	d := buildDescriptionFor(installInfo{method: InstallMethodContainer, imageCollectorVersion: "0.161.0"}, "1.0.0", uuid.New(), cfg, "")
	non := attrMap(d.NonIdentifyingAttributes)
	assert.Equal(t, "container", non["dev.myriarch.updater.install_method"])
	assert.Equal(t, "0.161.0", non["dev.myriarch.updater.image_collector_version"])
	assert.Equal(t, "ws://myriarch:8080", non["dev.myriarch.updater.server_endpoint"])
}
