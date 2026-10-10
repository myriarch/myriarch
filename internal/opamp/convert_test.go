// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"strings"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/domain"
)

func str(v string) *protobufs.AnyValue {
	return &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: v}}
}

func TestAnyToString(t *testing.T) {
	tests := []struct {
		name string
		in   *protobufs.AnyValue
		want string
	}{
		{"nil", nil, ""},
		{"empty oneof", &protobufs.AnyValue{}, ""},
		{"string", str("1001"), "1001"},
		{"bool", &protobufs.AnyValue{Value: &protobufs.AnyValue_BoolValue{BoolValue: false}}, "false"},
		{"int", &protobufs.AnyValue{Value: &protobufs.AnyValue_IntValue{IntValue: -42}}, "-42"},
		{"double shortest", &protobufs.AnyValue{Value: &protobufs.AnyValue_DoubleValue{DoubleValue: 1.5}}, "1.5"},
		{"double integral", &protobufs.AnyValue{Value: &protobufs.AnyValue_DoubleValue{DoubleValue: 3}}, "3"},
		{"double exponent", &protobufs.AnyValue{Value: &protobufs.AnyValue_DoubleValue{DoubleValue: 1e21}}, "1e+21"},
		{"bytes base64", &protobufs.AnyValue{Value: &protobufs.AnyValue_BytesValue{BytesValue: []byte{0xff, 0x00, 0x10}}}, "/wAQ"},
		{"array", &protobufs.AnyValue{Value: &protobufs.AnyValue_ArrayValue{ArrayValue: &protobufs.ArrayValue{Values: []*protobufs.AnyValue{str("a"), {Value: &protobufs.AnyValue_BoolValue{BoolValue: true}}}}}}, `["a",true]`},
		{"nested map sorted keys", &protobufs.AnyValue{Value: &protobufs.AnyValue_KvlistValue{KvlistValue: &protobufs.KeyValueList{Values: []*protobufs.KeyValue{
			{Key: "z", Value: str("1")}, {Key: "a", Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_ArrayValue{ArrayValue: &protobufs.ArrayValue{Values: []*protobufs.AnyValue{str("x")}}}}}}}}}, `{"a":["x"],"z":"1"}`},
		{"invalid utf8", str("a\xffb"), "a�b"},
		{"nul", str("a\x00b"), "ab"},
	}
	for _, tc := range tests {
		if got := anyToString(tc.in); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := anyToString(str(strings.Repeat("x", 10000))); len(got) != maxAttributeLen {
		t.Errorf("long values are truncated to %d bytes, got %d", maxAttributeLen, len(got))
	}
}

func TestAttributesToMapBounds(t *testing.T) {
	var kvs []*protobufs.KeyValue
	for i := 0; i < maxAttributes+50; i++ {
		kvs = append(kvs, &protobufs.KeyValue{Key: "k" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strings.Repeat("y", i/26), Value: str("v")})
	}
	kvs = append(kvs, &protobufs.KeyValue{Key: "", Value: str("ignored")})
	if got := attributesToMap(kvs); len(got) > maxAttributes {
		t.Fatalf("more than %d attributes accepted: %d", maxAttributes, len(got))
	}
	if _, ok := attributesToMap([]*protobufs.KeyValue{{Key: "", Value: str("x")}})[""]; ok {
		t.Fatal("empty keys are dropped")
	}
}

func TestDescriptionUpdateClassification(t *testing.T) {
	desc := func(service, version string) *protobufs.AgentDescription {
		return &protobufs.AgentDescription{
			IdentifyingAttributes:    []*protobufs.KeyValue{{Key: "service.name", Value: str(service)}, {Key: "service.version", Value: str(version)}},
			NonIdentifyingAttributes: []*protobufs.KeyValue{{Key: "host.name", Value: str("h")}, {Key: "os.type", Value: str("linux")}, {Key: "host.arch", Value: str("arm64")}, {Key: "os.description", Value: str("Debian")}},
		}
	}
	u := descriptionUpdate(desc(domain.UpdaterServiceName, "v0.1.0"), 0, nil)
	if u.Kind != domain.AgentKindUpdater || u.Version != "0.1.0" || u.Hostname != "h" || u.OSType != "linux" || u.Arch != "arm64" || u.OSDescription != "Debian" || u.InitialLabels == nil {
		t.Fatalf("updater: %+v", u)
	}
	// an updater is an updater even with remote config capability
	if u := descriptionUpdate(desc(domain.UpdaterServiceName, "1"), domain.CapAcceptsRemoteConfig, nil); u.Kind != domain.AgentKindUpdater {
		t.Fatalf("service name wins: %+v", u)
	}
	if u := descriptionUpdate(desc("otelcol-contrib", "0.162.0"), domain.CapAcceptsRemoteConfig, nil); u.Kind != domain.AgentKindCollector {
		t.Fatalf("collector: %+v", u)
	}
	if u := descriptionUpdate(desc("otelcol-contrib", "0.162.0"), domain.CapReportsStatus, nil); u.Kind != domain.AgentKindUnknown {
		t.Fatalf("unknown: %+v", u)
	}
	// identifying attributes win over non-identifying ones for lookups
	d := desc("a", "1")
	d.NonIdentifyingAttributes = append(d.NonIdentifyingAttributes, &protobufs.KeyValue{Key: "service.name", Value: str("shadow")})
	if u := descriptionUpdate(d, 0, nil); u.ServiceName != "a" {
		t.Fatalf("lookup order: %q", u.ServiceName)
	}
}

func TestHealthConversionBounds(t *testing.T) {
	now := time.Now()
	// zero timestamps are nil
	h := healthFromProto(&protobufs.ComponentHealth{Healthy: true, StartTimeUnixNano: 0, ComponentHealthMap: map[string]*protobufs.ComponentHealth{"a": {Healthy: false, StatusTimeUnixNano: 5}}}, now)
	if h.StartTime != nil || h.Components["a"].StatusTime == nil || h.Components["a"].StartTime != nil || !h.UpdatedAt.Equal(now) {
		t.Fatalf("%+v", h)
	}
	// depth and node budget protect against hostile trees
	deep := &protobufs.ComponentHealth{}
	cur := deep
	for i := 0; i < 100; i++ {
		next := &protobufs.ComponentHealth{}
		cur.ComponentHealthMap = map[string]*protobufs.ComponentHealth{"c": next}
		cur = next
	}
	conv := healthFromProto(deep, now)
	depth := 0
	for c := conv.Components; c != nil; {
		depth++
		var next map[string]*domain.ComponentHealth
		for _, v := range c {
			next = v.Components
		}
		c = next
	}
	if depth > maxHealthDepth {
		t.Fatalf("depth %d exceeds the limit %d", depth, maxHealthDepth)
	}
	wide := &protobufs.ComponentHealth{ComponentHealthMap: map[string]*protobufs.ComponentHealth{}}
	for i := 0; i < maxHealthNodes*2; i++ {
		wide.ComponentHealthMap[strings.Repeat("n", 1+i%5)+string(rune('a'+i%26))+strings.Repeat("z", i/26)] = &protobufs.ComponentHealth{}
	}
	if n := len(healthFromProto(wide, now).Components); n > maxHealthNodes {
		t.Fatalf("%d nodes accepted", n)
	}
	if !healthEqual(healthFromProto(deep, now), healthFromProto(deep, now.Add(time.Hour))) {
		t.Fatal("UpdatedAt must not take part in the comparison")
	}
	a, b := healthFromProto(&protobufs.ComponentHealth{Healthy: true}, now), healthFromProto(&protobufs.ComponentHealth{Healthy: false}, now)
	if healthEqual(a, b) || healthEqual(a, nil) || !healthEqual(nil, nil) {
		t.Fatal("healthEqual")
	}
}

func TestEffectiveConfigBodySelection(t *testing.T) {
	cfg := func(m map[string]*protobufs.AgentConfigObject) *protobufs.EffectiveConfig {
		return &protobufs.EffectiveConfig{ConfigMap: &protobufs.AgentConfigMap{ConfigMap: m}}
	}
	if _, _, ok := effectiveConfigBody(nil); ok {
		t.Fatal("nil")
	}
	if _, _, ok := effectiveConfigBody(cfg(nil)); ok {
		t.Fatal("empty map")
	}
	body, ct, ok := effectiveConfigBody(cfg(map[string]*protobufs.AgentConfigObject{"": {Body: []byte("a"), ContentType: ""}, "raw": {Body: []byte("b"), ContentType: "text/plain"}}))
	if !ok || string(body) != "a" || ct != "text/yaml" {
		t.Fatalf("key \"\" wins, empty content type means YAML: %q %q %v", body, ct, ok)
	}
	body, ct, ok = effectiveConfigBody(cfg(map[string]*protobufs.AgentConfigObject{"only": {Body: []byte("c"), ContentType: "application/json"}}))
	if !ok || string(body) != "c" || ct != "application/json" {
		t.Fatalf("single key: %q %q", body, ct)
	}
	if _, _, ok := effectiveConfigBody(cfg(map[string]*protobufs.AgentConfigObject{"x": {Body: []byte("1")}, "y": {Body: []byte("2")}})); ok {
		t.Fatal("ambiguous keys without \"\" are ignored")
	}
}

func TestPackageHelpers(t *testing.T) {
	o1 := domain.PackageOffer{Name: "", Version: "v1", DownloadURL: "u", ContentHash: domain.SHA256([]byte("a")), Headers: map[string]string{"B": "2", "A": "1"}}
	pa := packagesAvailable([]domain.PackageOffer{o1}, domain.AllPackagesHash([]domain.PackageOffer{o1}))
	hs := pa.Packages[""].File.Headers.Headers
	if len(hs) != 2 || hs[0].Key != "A" || hs[1].Key != "B" {
		t.Fatalf("headers must be deterministic (sorted): %+v", hs)
	}
	if pa.Packages[""].File.Signature != nil {
		t.Fatal("no signature")
	}
	// statuses
	ps := packageStatusesFromProto(&protobufs.PackageStatuses{ServerProvidedAllPackagesHash: []byte{1}, Packages: map[string]*protobufs.PackageStatus{
		"":  {Name: "", Status: protobufs.PackageStatusEnum_PackageStatusEnum_Downloading, DownloadDetails: &protobufs.PackageDownloadDetails{DownloadPercent: 12.5}},
		"x": {Name: "x"}}}, time.Now())
	if ps.Packages[""].Status != domain.PackageDownloading || ps.Packages[""].DownloadPercent != 12.5 || ps.Packages["x"].Status != domain.PackageInstalled {
		t.Fatalf("%+v", ps.Packages)
	}
	if !packagesEqual(ps.Packages, ps.Packages) {
		t.Fatal("equal")
	}
	changed := packageStatusesFromProto(&protobufs.PackageStatuses{Packages: map[string]*protobufs.PackageStatus{
		"": {Name: "", Status: protobufs.PackageStatusEnum_PackageStatusEnum_Downloading, DownloadDetails: &protobufs.PackageDownloadDetails{DownloadPercent: 12.9}}, "x": {Name: "x"}}}, time.Now())
	if !packagesEqual(ps.Packages, changed.Packages) {
		t.Fatal("progress is compared in whole percents (no write per tiny progress change)")
	}
	changed.Packages[""].DownloadPercent = 13.1
	if packagesEqual(ps.Packages, changed.Packages) {
		t.Fatal("a whole percent step is a change")
	}
}

func TestServerCapabilitiesAreAnnounced(t *testing.T) {
	want := []protobufs.ServerCapabilities{
		protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus, protobufs.ServerCapabilities_ServerCapabilities_OffersRemoteConfig,
		protobufs.ServerCapabilities_ServerCapabilities_AcceptsEffectiveConfig, protobufs.ServerCapabilities_ServerCapabilities_OffersPackages,
		protobufs.ServerCapabilities_ServerCapabilities_AcceptsPackagesStatus,
	}
	for _, c := range want {
		if serverCapabilities&uint64(c) == 0 {
			t.Errorf("capability %s missing", c)
		}
	}
	// no connection settings: the supervisor would disable TLS verification
	// (docs/design/upstream-notes.md, Supervisor limits > Remote configuration handling)
	if serverCapabilities&uint64(protobufs.ServerCapabilities_ServerCapabilities_OffersConnectionSettings) != 0 {
		t.Error("OffersConnectionSettings must not be announced")
	}
}
