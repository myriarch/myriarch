// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import "github.com/myriarch/myriarch/internal/store"

type storeUpdate = store.AgentDescriptionUpdate

func newUpdate(host string) store.AgentDescriptionUpdate {
	return store.AgentDescriptionUpdate{
		Kind: "collector", ServiceName: "otelcol-contrib", Version: "0.162.0", Hostname: host, OSType: "linux", Arch: "amd64",
		IdentifyingAttributes: map[string]string{"service.name": "otelcol-contrib"}, NonIdentifyingAttributes: map[string]string{"host.name": host},
		Capabilities: capsCollector,
	}
}
