// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestUpdaterMismatch(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	mk := func(tok *uuid.UUID, cert, host string) *Agent {
		return &Agent{EnrollmentTokenID: tok, ClientCertSubject: cert, Hostname: host}
	}
	cases := []struct {
		name    string
		u, c    *Agent
		wantErr string
	}{
		{"same token and host", mk(&a, "", "h1"), mk(&a, "", "h1"), ""},
		{"host names compare case-insensitively", mk(&a, "", "HOST1"), mk(&a, "", "host1"), ""},
		{"unknown host is not a reason to refuse", mk(&a, "", ""), mk(&a, "", "h1"), ""},
		{"no credentials at all (dev)", mk(nil, "", "h1"), mk(nil, "", "h1"), ""},
		{"same certificate", mk(nil, "cn-1", "h"), mk(nil, "cn-1", "h"), ""},
		{"different tokens", mk(&a, "", "h1"), mk(&b, "", "h1"), "different enrollment tokens"},
		{"token vs no token", mk(&a, "", "h1"), mk(nil, "", "h1"), "different enrollment tokens"},
		{"different certificates", mk(nil, "cn-1", "h"), mk(nil, "cn-2", "h"), "different client certificates"},
		{"different hosts", mk(&a, "", "h1"), mk(&a, "", "h2"), "different host names"},
	}
	for _, tc := range cases {
		got := UpdaterMismatch(tc.u, tc.c)
		if (tc.wantErr == "") != (got == "") || (tc.wantErr != "" && !strings.Contains(got, tc.wantErr)) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.wantErr)
		}
	}
	if UpdaterMismatch(nil, nil) != "" {
		t.Error("nil agents")
	}
}

// AC-04: the labels. namespace of the attribute view belongs to the server.
func TestAttributeViewIgnoresAgentReportedLabels(t *testing.T) {
	a := &Agent{
		IdentifyingAttributes:    map[string]string{"service.name": "otelcol", "labels.stolen": "id"},
		NonIdentifyingAttributes: map[string]string{"labels.channel": "from-agent", "site.id": "1001"},
		Labels:                   map[string]string{"pilot": "true"},
	}
	v := NewAttributeView(a)
	if _, ok := v["labels.channel"]; ok {
		t.Error("agent attribute labels.channel must not appear")
	}
	if _, ok := v["labels.stolen"]; ok {
		t.Error("agent attribute labels.stolen must not appear")
	}
	if v["labels.pilot"] != "true" || v["site.id"] != "1001" || v["service.name"] != "otelcol" {
		t.Errorf("view: %v", v)
	}
	// a server label with the same key as an agent attribute wins (and is the only one)
	a.Labels["channel"] = "stable"
	if got := NewAttributeView(a)["labels.channel"]; got != "stable" {
		t.Errorf("server label: %q", got)
	}
}
