// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package dto_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/install"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/store"
)

// TestOpenAPIContract verifies that the Go wire types and the schemas in
// api/openapi.yaml have exactly the same JSON properties, and that "required"
// in the schema corresponds to Go fields without ",omitempty" (response
// types). This test pins the wire contract: it must stay green whenever a wire
// type or the specification changes.
func TestOpenAPIContract(t *testing.T) {
	spec := loadSpec(t)

	responseTypes := map[string]reflect.Type{
		"Problem":                    tof[api.Problem](),
		"Issue":                      tof[domain.Issue](),
		"ActionAccepted":             tof[dto.ActionAccepted](),
		"FacetValue":                 tof[store.FacetValue](),
		"SourceRef":                  tof[domain.SourceRef](),
		"Agent":                      tof[domain.Agent](),
		"AgentConnection":            tof[domain.AgentConnection](),
		"AgentHealth":                tof[domain.AgentHealth](),
		"ComponentHealth":            tof[domain.ComponentHealth](),
		"RemoteConfigState":          tof[domain.RemoteConfigState](),
		"AgentAssignment":            tof[domain.AgentAssignment](),
		"UpdaterLink":                tof[domain.UpdaterLink](),
		"PackageStatuses":            tof[domain.PackageStatuses](),
		"PackageStatus":              tof[domain.PackageStatus](),
		"AgentSummary":               tof[dto.AgentSummary](),
		"AgentSummaryPage":           tof[api.Page[dto.AgentSummary]](),
		"AgentDetail":                tof[dto.AgentDetail](),
		"ConfigDocument":             tof[dto.ConfigDocument](),
		"ComponentSet":               tof[domain.ComponentSet](),
		"ComponentDetails":           tof[domain.ComponentDetails](),
		"AgentEvent":                 tof[domain.AgentEvent](),
		"AgentEventList":             tof[dto.AgentEventList](),
		"AgentFacets":                tof[store.AgentFacets](),
		"SavedFilter":                tof[domain.SavedFilter](),
		"SavedFilterList":            tof[api.List[domain.SavedFilter]](),
		"OverviewStats":              tof[dto.OverviewStats](),
		"AgentCounts":                tof[dto.AgentCounts](),
		"VersionCount":               tof[dto.VersionCount](),
		"OutdatedSummary":            tof[dto.OutdatedSummary](),
		"FleetCounts":                tof[dto.FleetCounts](),
		"GitOpsBrief":                tof[dto.GitOpsBrief](),
		"SystemInfo":                 tof[dto.SystemInfo](),
		"Replica":                    tof[domain.Replica](),
		"DatabaseInfo":               tof[dto.DatabaseInfo](),
		"FeatureFlags":               tof[dto.FeatureFlags](),
		"SettingsSummary":            tof[dto.SettingsSummary](),
		"AuthConfigInfo":             tof[dto.AuthConfigInfo](),
		"UIConfigInfo":               tof[dto.UIConfigInfo](),
		"Me":                         tof[dto.Me](),
		"User":                       tof[domain.User](),
		"UserList":                   tof[api.List[domain.User]](),
		"APIToken":                   tof[domain.APIToken](),
		"APITokenList":               tof[api.List[domain.APIToken]](),
		"CreatedAPIToken":            tof[dto.CreatedAPIToken](),
		"EnrollmentToken":            tof[domain.EnrollmentToken](),
		"EnrollmentTokenList":        tof[api.List[domain.EnrollmentToken]](),
		"CreatedEnrollmentToken":     tof[dto.CreatedEnrollmentToken](),
		"AuditEvent":                 tof[domain.AuditEvent](),
		"AuditEventPage":             tof[api.Page[domain.AuditEvent]](),
		"Selector":                   tof[domain.Selector](),
		"SelectorRequirement":        tof[domain.SelectorRequirement](),
		"CollectorSpec":              tof[domain.CollectorSpec](),
		"UpdateSpec":                 tof[domain.UpdateSpec](),
		"FleetRolloutSpec":           tof[domain.FleetRolloutSpec](),
		"BridgeSpec":                 tof[domain.BridgeSpec](),
		"Fleet":                      tof[domain.Fleet](),
		"FleetStatus":                tof[dto.FleetStatus](),
		"FleetSummary":               tof[dto.FleetSummary](),
		"FleetSummaryList":           tof[api.List[dto.FleetSummary]](),
		"FleetDetail":                tof[dto.FleetDetail](),
		"MatchFleetsResponse":        tof[dto.MatchFleetsResponse](),
		"ExecutorSummary":            tof[dto.ExecutorSummary](),
		"ExecutorSummaryList":        tof[api.List[dto.ExecutorSummary]](),
		"ParameterSpec":              tof[domain.ParameterSpec](),
		"Fragment":                   tof[domain.Fragment](),
		"Blueprint":                  tof[domain.Blueprint](),
		"BlueprintSummary":           tof[dto.BlueprintSummary](),
		"BlueprintSummaryList":       tof[api.List[dto.BlueprintSummary]](),
		"BlueprintDetail":            tof[dto.BlueprintDetail](),
		"RenderResponse":             tof[dto.RenderResponse](),
		"WaveSpec":                   tof[domain.WaveSpec](),
		"FailureThreshold":           tof[domain.FailureThreshold](),
		"MaintenanceWindow":          tof[domain.MaintenanceWindow](),
		"RolloutPolicy":              tof[domain.RolloutPolicy](),
		"RolloutPolicyList":          tof[api.List[domain.RolloutPolicy]](),
		"RolloutCounts":              tof[domain.RolloutCounts](),
		"RolloutWave":                tof[domain.RolloutWave](),
		"Rollout":                    tof[domain.Rollout](),
		"RolloutSummary":             tof[dto.RolloutSummary](),
		"RolloutSummaryPage":         tof[api.Page[dto.RolloutSummary]](),
		"RolloutTarget":              tof[domain.RolloutTarget](),
		"RolloutTargetPage":          tof[api.Page[domain.RolloutTarget]](),
		"ChangelogEntry":             tof[domain.ChangelogEntry](),
		"ReleaseAsset":               tof[domain.ReleaseAsset](),
		"Release":                    tof[domain.Release](),
		"ReleaseSummary":             tof[dto.ReleaseSummary](),
		"ReleaseSummaryPage":         tof[api.Page[dto.ReleaseSummary]](),
		"FleetBehind":                tof[dto.FleetBehind](),
		"ReleaseDetail":              tof[dto.ReleaseDetail](),
		"VersionComparison":          tof[domain.VersionComparison](),
		"GitCommit":                  tof[domain.GitCommit](),
		"GitOpsSyncStatus":           tof[domain.GitOpsSyncStatus](),
		"GitOpsSync":                 tof[domain.GitOpsSync](),
		"GitOpsSyncPage":             tof[api.Page[domain.GitOpsSync]](),
		"GitOpsObjectRef":            tof[dto.GitOpsObjectRef](),
		"GitOpsObjectRefList":        tof[api.List[dto.GitOpsObjectRef]](),
		"Artifact":                   tof[domain.Artifact](),
		"ArtifactPage":               tof[api.Page[domain.Artifact]](),
		"ArtifactList":               tof[api.List[domain.Artifact]](),
		"ChannelInfo":                tof[notify.ChannelInfo](),
		"ChannelInfoList":            tof[api.List[notify.ChannelInfo]](),
		"LiveAgentRemovedData":       tof[dto.LiveAgentRemovedData](),
		"LiveFleetUpdatedData":       tof[dto.LiveFleetUpdatedData](),
		"VersionSelectionSpec":       tof[domain.VersionSelectionSpec](),
		"FleetVersionSelection":      tof[domain.FleetVersionSelection](),
		"FleetVersionTarget":         tof[dto.FleetVersionTarget](),
		"VersionOption":              tof[dto.VersionOption](),
		"PlatformCount":              tof[dto.PlatformCount](),
		"FleetVersionOptions":        tof[dto.FleetVersionOptions](),
		"SelectFleetVersionResponse": tof[dto.SelectFleetVersionResponse](),
		"ReleaseFleetOption":         tof[dto.ReleaseFleetOption](),
		"TargetProgress":             tof[domain.TargetProgress](),
		"UpdateStep":                 tof[domain.UpdateStep](),
		"InstallPlan":                tof[install.Plan](),
		"InstallPlanVersion":         tof[install.PlanVersion](),
		"InstallPlanOpAMP":           tof[install.PlanOpAMP](),
		"InstallPlanArtifact":        tof[install.PlanArtifact](),
		"InstallPlanUpdater":         tof[install.PlanUpdater](),
	}
	requestTypes := map[string]reflect.Type{
		"CreateSavedFilterRequest":          tof[dto.CreateSavedFilterRequest](),
		"DevLoginRequest":                   tof[dto.DevLoginRequest](),
		"BootstrapLoginRequest":             tof[dto.BootstrapLoginRequest](),
		"UpdateUserRequest":                 tof[dto.UpdateUserRequest](),
		"CreateAPITokenRequest":             tof[dto.CreateAPITokenRequest](),
		"CreateEnrollmentTokenRequest":      tof[dto.CreateEnrollmentTokenRequest](),
		"MatchFleetsRequest":                tof[dto.MatchFleetsRequest](),
		"RenderRequest":                     tof[dto.RenderRequest](),
		"RolloutActionRequest":              tof[dto.RolloutActionRequest](),
		"ApproveWaveRequest":                tof[dto.ApproveWaveRequest](),
		"PrefetchRequest":                   tof[dto.PrefetchRequest](),
		"SelectFleetVersionRequest":         tof[dto.SelectFleetVersionRequest](),
		"ClearFleetVersionSelectionRequest": tof[dto.ClearFleetVersionSelectionRequest](),
		"InstallPlanRequest":                tof[install.PlanRequest](),
	}

	for name, typ := range responseTypes {
		checkSchema(t, spec, name, typ, true)
	}
	for name, typ := range requestTypes {
		checkSchema(t, spec, name, typ, false)
	}

	// Every object schema with properties must be mapped (no forgotten types).
	for name, s := range spec.Components.Schemas {
		if len(s.Properties) == 0 {
			continue
		}
		if _, ok := responseTypes[name]; ok {
			continue
		}
		if _, ok := requestTypes[name]; ok {
			continue
		}
		t.Errorf("schema %s has no Go type mapping in contract_test.go", name)
	}
}

type schema struct {
	Type       any                  `yaml:"type"`
	Required   []string             `yaml:"required"`
	Properties map[string]yaml.Node `yaml:"properties"`
}

type openAPI struct {
	Components struct {
		Schemas map[string]schema `yaml:"schemas"`
	} `yaml:"components"`
}

func loadSpec(t *testing.T) openAPI {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "api", "openapi.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var spec openAPI
	if err := yaml.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

func tof[T any]() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

type field struct {
	name      string
	omitempty bool
}

// jsonFields returns the JSON fields of a struct type, flattening embedded
// structs like encoding/json does.
func jsonFields(t reflect.Type) []field {
	var out []field
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				out = append(out, jsonFields(ft)...)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		// omitzero leaves a zero value out like omitempty does (for structs, which omitempty never does)
		out = append(out, field{name: name, omitempty: strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero")})
	}
	return out
}

func checkSchema(t *testing.T, spec openAPI, name string, typ reflect.Type, response bool) {
	t.Helper()
	s, ok := spec.Components.Schemas[name]
	if !ok {
		t.Errorf("schema %s missing in openapi.yaml", name)
		return
	}
	fields := jsonFields(typ)
	goNames := map[string]field{}
	for _, f := range fields {
		goNames[f.name] = f
	}
	var missingInGo, missingInSpec []string
	for p := range s.Properties {
		if _, ok := goNames[p]; !ok {
			missingInGo = append(missingInGo, p)
		}
	}
	for n := range goNames {
		if _, ok := s.Properties[n]; !ok {
			missingInSpec = append(missingInSpec, n)
		}
	}
	sort.Strings(missingInGo)
	sort.Strings(missingInSpec)
	if len(missingInGo) > 0 {
		t.Errorf("%s: properties in openapi but not in Go %s: %v", name, typ, missingInGo)
	}
	if len(missingInSpec) > 0 {
		t.Errorf("%s: JSON fields in Go %s but not in openapi: %v", name, typ, missingInSpec)
	}
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
		if _, ok := goNames[r]; !ok {
			t.Errorf("%s: required property %q does not exist in Go %s", name, r, typ)
		}
	}
	if !response {
		return
	}
	for n, f := range goNames {
		if _, inSpec := s.Properties[n]; !inSpec {
			continue
		}
		if !f.omitempty && !req[n] {
			t.Errorf("%s.%s: Go field is always present (no omitempty) but not required in openapi", name, n)
		}
		if f.omitempty && req[n] {
			t.Errorf("%s.%s: required in openapi but Go field has omitempty", name, n)
		}
	}
}
