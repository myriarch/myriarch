// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// Limits protecting the server from oversized or hostile agent reports.
const (
	maxAttributes      = 256
	maxAttributeLen    = 4096
	maxHashLen         = 64      // agent supplied hashes are 32 byte SHA-256 values
	maxComponentMeta   = 16      // metadata entries per component
	maxComponentSetLen = 2 << 20 // serialized size of one component set
	maxHealthDepth     = 16
	maxHealthNodes     = 2000
	maxComponentKinds  = 16
	maxComponentsTotal = 20000
	maxPackages        = 64
)

// cleanString makes agent supplied text safe for storage: valid UTF-8, no NUL
// bytes, bounded length.
func cleanString(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToValidUTF8(s, "�")
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if len(s) > maxAttributeLen {
		s = s[:maxAttributeLen]
		s = strings.ToValidUTF8(s, "")
	}
	return s
}

// anyToNative converts an OpAMP AnyValue into a JSON-friendly Go value.
func anyToNative(v *protobufs.AnyValue) any {
	if v == nil {
		return nil
	}
	switch x := v.Value.(type) {
	case *protobufs.AnyValue_StringValue:
		return cleanString(x.StringValue)
	case *protobufs.AnyValue_BoolValue:
		return x.BoolValue
	case *protobufs.AnyValue_IntValue:
		return x.IntValue
	case *protobufs.AnyValue_DoubleValue:
		return x.DoubleValue
	case *protobufs.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(x.BytesValue)
	case *protobufs.AnyValue_ArrayValue:
		out := make([]any, 0, len(x.ArrayValue.GetValues()))
		for _, e := range x.ArrayValue.GetValues() {
			out = append(out, anyToNative(e))
		}
		return out
	case *protobufs.AnyValue_KvlistValue:
		out := make(map[string]any, len(x.KvlistValue.GetValues()))
		for _, kv := range x.KvlistValue.GetValues() {
			out[cleanString(kv.GetKey())] = anyToNative(kv.GetValue())
		}
		return out
	}
	return nil
}

// anyToString applies the attribute contract of docs/concepts/agents.md ("The
// attribute view"): bool -> "true"/"false", int -> decimal, double -> shortest
// representation, bytes -> Base64, arrays and maps -> JSON.
func anyToString(v *protobufs.AnyValue) string {
	if v == nil {
		return ""
	}
	switch x := v.Value.(type) {
	case *protobufs.AnyValue_StringValue:
		return cleanString(x.StringValue)
	case *protobufs.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	case *protobufs.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *protobufs.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	case *protobufs.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(x.BytesValue)
	case *protobufs.AnyValue_ArrayValue, *protobufs.AnyValue_KvlistValue:
		b, err := json.Marshal(anyToNative(v))
		if err != nil {
			return ""
		}
		return cleanString(string(b))
	}
	return ""
}

// attributesToMap converts key/value attributes to strings (bounded).
// Keys in the server-owned "labels." namespace are dropped: labels come from
// enrollment tokens and the GitOps inventory only, never from the agent.
func attributesToMap(kvs []*protobufs.KeyValue) map[string]string {
	out, _ := attributesToMapReport(kvs)
	return out
}

// attributesToMapReport is attributesToMap plus the reserved keys that were
// dropped.
func attributesToMapReport(kvs []*protobufs.KeyValue) (map[string]string, []string) {
	out := make(map[string]string, len(kvs))
	var dropped []string
	for i, kv := range kvs {
		if i >= maxAttributes {
			break
		}
		k := cleanString(kv.GetKey())
		if k == "" {
			continue
		}
		if strings.HasPrefix(k, domain.LabelPrefix) {
			dropped = append(dropped, k)
			continue
		}
		out[k] = anyToString(kv.GetValue())
	}
	return out, dropped
}

// reservedAttributeKeys lists the reserved ("labels.") keys of a description.
func reservedAttributeKeys(d *protobufs.AgentDescription) []string {
	_, a := attributesToMapReport(d.GetIdentifyingAttributes())
	_, b := attributesToMapReport(d.GetNonIdentifyingAttributes())
	return append(a, b...)
}

// cleanHash accepts only SHA-256 sized hashes (agents choose the value).
func cleanHash(b []byte) domain.Hash {
	if len(b) == 0 || len(b) > maxHashLen {
		return nil
	}
	return domain.Hash(append([]byte(nil), b...))
}

// descriptionUpdate derives the store update from an AgentDescription.
// caps is the capability bitmask to apply (message capabilities or the known
// ones).
func descriptionUpdate(d *protobufs.AgentDescription, caps uint64, cred *authn.AgentCredential) (upd store.AgentDescriptionUpdate) {
	id := attributesToMap(d.GetIdentifyingAttributes())
	non := attributesToMap(d.GetNonIdentifyingAttributes())
	lookup := func(key string) string {
		if v, ok := id[key]; ok {
			return v
		}
		return non[key]
	}
	u := &upd
	u.IdentifyingAttributes, u.NonIdentifyingAttributes = id, non
	u.ServiceName = lookup(domain.AttrServiceName)
	u.Version = domain.NormalizeVersion(lookup(domain.AttrServiceVersion))
	u.Hostname = lookup(domain.AttrHostName)
	u.OSType = lookup(domain.AttrOSType)
	u.OSDescription = lookup(domain.AttrOSDescription)
	u.Arch = lookup(domain.AttrHostArch)
	u.Capabilities = caps
	switch {
	case u.ServiceName == domain.UpdaterServiceName:
		u.Kind = domain.AgentKindUpdater
	case domain.HasCapability(caps, domain.CapAcceptsRemoteConfig):
		u.Kind = domain.AgentKindCollector
	default:
		u.Kind = domain.AgentKindUnknown
	}
	if cred != nil {
		u.EnrollmentTokenID = cred.EnrollmentTokenID
		u.ClientCertSubject = cred.ClientCertSubject
		u.InitialLabels = cred.Labels
	}
	if u.InitialLabels == nil {
		u.InitialLabels = map[string]string{}
	}
	return upd
}

// nanosToTime converts unix nanoseconds (0 = unset) to UTC time.
func nanosToTime(n uint64) *time.Time {
	if n == 0 {
		return nil
	}
	t := time.Unix(0, int64(n)).UTC()
	return &t
}

// healthFromProto converts OpAMP ComponentHealth recursively.
func healthFromProto(h *protobufs.ComponentHealth, now time.Time) *domain.AgentHealth {
	if h == nil {
		return nil
	}
	budget := maxHealthNodes
	out := &domain.AgentHealth{
		Healthy: h.Healthy, Status: cleanString(h.Status), LastError: cleanString(h.LastError),
		StartTime: nanosToTime(h.StartTimeUnixNano), UpdatedAt: now,
	}
	out.Components = componentHealthMap(h.ComponentHealthMap, 1, &budget)
	return out
}

func componentHealthMap(m map[string]*protobufs.ComponentHealth, depth int, budget *int) map[string]*domain.ComponentHealth {
	if len(m) == 0 || depth > maxHealthDepth || *budget <= 0 {
		return nil
	}
	out := make(map[string]*domain.ComponentHealth, len(m))
	for k, c := range m {
		if c == nil || *budget <= 0 {
			continue
		}
		*budget--
		out[cleanString(k)] = &domain.ComponentHealth{
			Healthy: c.Healthy, Status: cleanString(c.Status), LastError: cleanString(c.LastError),
			StartTime: nanosToTime(c.StartTimeUnixNano), StatusTime: nanosToTime(c.StatusTimeUnixNano),
			Components: componentHealthMap(c.ComponentHealthMap, depth+1, budget),
		}
	}
	return out
}

// healthEqual compares the semantically relevant parts of two health reports
// (not UpdatedAt).
func healthEqual(a, b *domain.AgentHealth) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Healthy != b.Healthy || a.Status != b.Status || a.LastError != b.LastError || !timePtrEqual(a.StartTime, b.StartTime) {
		return false
	}
	return componentsEqual(a.Components, b.Components)
}

func componentsEqual(a, b map[string]*domain.ComponentHealth) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		y, ok := b[k]
		if !ok || x == nil || y == nil {
			return false
		}
		if x.Healthy != y.Healthy || x.Status != y.Status || x.LastError != y.LastError ||
			!timePtrEqual(x.StartTime, y.StartTime) || !timePtrEqual(x.StatusTime, y.StatusTime) || !componentsEqual(x.Components, y.Components) {
			return false
		}
	}
	return true
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// remoteConfigStatusFromProto converts the reported status.
func remoteConfigStatusFromProto(s *protobufs.RemoteConfigStatus) domain.RemoteConfigState {
	st := domain.RemoteConfigUnset
	switch s.GetStatus() {
	case protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED:
		st = domain.RemoteConfigApplied
	case protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING:
		st = domain.RemoteConfigApplying
	case protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED:
		st = domain.RemoteConfigFailed
	}
	return domain.RemoteConfigState{Status: st, LastHash: cleanHash(s.GetLastRemoteConfigHash()), ErrorMessage: cleanString(s.GetErrorMessage())}
}

// effectiveConfigBody picks the config body of an EffectiveConfig: key "", else
// the only key, else nothing. A missing content type means YAML (the
// supervisor reports its own config with an empty content type).
func effectiveConfigBody(ec *protobufs.EffectiveConfig) (body []byte, contentType string, ok bool) {
	m := ec.GetConfigMap().GetConfigMap()
	if len(m) == 0 {
		return nil, "", false
	}
	obj, found := m[""]
	if !found {
		if len(m) != 1 {
			return nil, "", false
		}
		for _, o := range m {
			obj = o
		}
	}
	if obj == nil {
		return nil, "", false
	}
	ct := obj.GetContentType()
	if ct == "" {
		ct = domain.RemoteConfigContentType
	}
	return obj.GetBody(), ct, true
}

// componentSetFromProto converts AvailableComponents (kind -> sub component
// map type -> details with metadata).
func componentSetFromProto(ac *protobufs.AvailableComponents) domain.ComponentSet {
	cs := domain.ComponentSet{Hash: cleanHash(ac.GetHash()), Components: map[string]map[string]domain.ComponentDetails{}}
	total, size := 0, 0
	for kind, details := range ac.GetComponents() {
		if len(cs.Components) >= maxComponentKinds {
			break
		}
		byType := map[string]domain.ComponentDetails{}
		for typ, d := range details.GetSubComponentMap() {
			if total >= maxComponentsTotal || size >= maxComponentSetLen {
				break
			}
			total++
			meta := limitMetadata(attributesToMap(d.GetMetadata()), maxComponentMeta)
			cd := domain.ComponentDetails{Module: meta["code.namespace"]}
			delete(meta, "code.namespace")
			if len(meta) > 0 {
				cd.Metadata = meta
			}
			size += len(typ) + len(cd.Module)
			for k, v := range meta {
				size += len(k) + len(v)
			}
			byType[cleanString(typ)] = cd
		}
		cs.Components[cleanString(kind)] = byType
	}
	return cs
}

// limitMetadata keeps at most n entries (smallest keys first, so the result
// is deterministic).
func limitMetadata(m map[string]string, n int) map[string]string {
	if len(m) <= n {
		return m
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make(map[string]string, n)
	for _, k := range keys[:n] {
		out[k] = m[k]
	}
	return out
}

// limitStrings returns at most n elements.
func limitStrings(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// packageStatusesFromProto converts the reported package statuses.
func packageStatusesFromProto(ps *protobufs.PackageStatuses, now time.Time) *domain.PackageStatuses {
	out := &domain.PackageStatuses{
		ServerProvidedAllPackagesHash: cleanHash(ps.GetServerProvidedAllPackagesHash()),
		ErrorMessage:                  cleanString(ps.GetErrorMessage()),
		Packages:                      map[string]*domain.PackageStatus{},
		UpdatedAt:                     now,
	}
	for name, p := range ps.GetPackages() {
		if len(out.Packages) >= maxPackages {
			break
		}
		out.Packages[cleanString(name)] = &domain.PackageStatus{
			Name: cleanString(p.GetName()), AgentHasVersion: cleanString(p.GetAgentHasVersion()), AgentHasHash: cleanHash(p.GetAgentHasHash()),
			ServerOfferedVersion: cleanString(p.GetServerOfferedVersion()), ServerOfferedHash: cleanHash(p.GetServerOfferedHash()),
			Status: packageStatusFromProto(p.GetStatus()), ErrorMessage: cleanString(p.GetErrorMessage()),
			DownloadPercent: p.GetDownloadDetails().GetDownloadPercent(),
		}
	}
	return out
}

func packageStatusFromProto(s protobufs.PackageStatusEnum) domain.PackageInstallStatus {
	switch s {
	case protobufs.PackageStatusEnum_PackageStatusEnum_InstallPending:
		return domain.PackageInstallPending
	case protobufs.PackageStatusEnum_PackageStatusEnum_Installing:
		return domain.PackageInstalling
	case protobufs.PackageStatusEnum_PackageStatusEnum_InstallFailed:
		return domain.PackageInstallFailed
	case protobufs.PackageStatusEnum_PackageStatusEnum_Downloading:
		return domain.PackageDownloading
	}
	return domain.PackageInstalled
}

// packageStatusEqual compares the parts of a package status that matter for
// events (status and error).
func packageStatusChanged(old, new *domain.PackageStatus) bool {
	if old == nil || new == nil {
		return old != new
	}
	return old.Status != new.Status || old.ErrorMessage != new.ErrorMessage || old.AgentHasVersion != new.AgentHasVersion
}

// serverCapabilities are the capabilities myriarchd announces in every response
// (opamp-go does not set them).
const serverCapabilities = uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus |
	protobufs.ServerCapabilities_ServerCapabilities_OffersRemoteConfig |
	protobufs.ServerCapabilities_ServerCapabilities_AcceptsEffectiveConfig |
	protobufs.ServerCapabilities_ServerCapabilities_OffersPackages |
	protobufs.ServerCapabilities_ServerCapabilities_AcceptsPackagesStatus)

// packagesAvailable builds the PackagesAvailable message for a (non-empty)
// offer set.
func packagesAvailable(offers []domain.PackageOffer, allHash domain.Hash) *protobufs.PackagesAvailable {
	pa := &protobufs.PackagesAvailable{Packages: make(map[string]*protobufs.PackageAvailable, len(offers)), AllPackagesHash: allHash}
	for _, o := range offers {
		f := &protobufs.DownloadableFile{DownloadUrl: o.DownloadURL, ContentHash: o.ContentHash, Signature: o.Signature}
		if len(o.Headers) > 0 {
			h := &protobufs.Headers{}
			keys := make([]string, 0, len(o.Headers))
			for k := range o.Headers {
				keys = append(keys, k)
			}
			sortStrings(keys)
			for _, k := range keys {
				h.Headers = append(h.Headers, &protobufs.Header{Key: k, Value: o.Headers[k]})
			}
			f.Headers = h
		}
		pa.Packages[o.Name] = &protobufs.PackageAvailable{
			Type: protobufs.PackageType_PackageType_TopLevel, Version: o.Version, File: f, Hash: domain.PackageHash(o),
		}
	}
	return pa
}
