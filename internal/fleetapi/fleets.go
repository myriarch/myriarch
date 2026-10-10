// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/domain"
)

// ------------------------------------------------------------------ fleets ----

func (s *service) fleetRoutes() []api.Route {
	return []api.Route{
		route(http.MethodGet, "/fleets", "listFleets", "read", s.listFleets),
		route(http.MethodPost, "/fleets/match", "matchFleets", "read", s.matchFleets),
		route(http.MethodGet, "/fleets/{name}", "getFleet", "read", s.getFleet),
		route(http.MethodGet, "/fleets/{name}/version-options", "getFleetVersionOptions", "read", s.getFleetVersionOptions),
		route(http.MethodPost, "/fleets/{name}/actions/select-version", "selectFleetVersion", string(authz.PermFleetVersionSet), s.selectFleetVersion),
		route(http.MethodPost, "/fleets/{name}/actions/clear-version-selection", "clearFleetVersionSelection", string(authz.PermFleetVersionSet), s.clearFleetVersionSelection),
	}
}

func (s *service) listFleets(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	st, err := s.fleetStatistics(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	names := s.actors(r.Context())
	items := make([]dto.FleetSummary, 0, len(snap.Fleets))
	for i := range snap.Fleets {
		f := snap.Fleets[i]
		items = append(items, dto.FleetSummary{Fleet: normalizeFleet(f), Status: s.fleetStatus(st, &snap.Fleets[i], names)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Fleet.Name < items[j].Fleet.Name })
	api.WriteJSON(w, http.StatusOK, api.NewList(items))
}

// normalizeFleet makes optional maps non-nil for the wire format.
func normalizeFleet(f domain.Fleet) domain.Fleet {
	if f.Parameters == nil {
		f.Parameters = map[string]any{}
	}
	if f.Selector.MatchAttributes == nil {
		f.Selector.MatchAttributes = map[string]string{}
	}
	if f.Selector.MatchExpressions == nil {
		f.Selector.MatchExpressions = []domain.SelectorRequirement{}
	}
	if f.Update.Params == nil {
		f.Update.Params = map[string]any{}
	}
	return f
}

func findFleet(snap *domain.GitOpsSnapshot, name string) *domain.Fleet {
	for i := range snap.Fleets {
		if snap.Fleets[i].Name == name {
			return &snap.Fleets[i]
		}
	}
	return nil
}

func findBlueprint(snap *domain.GitOpsSnapshot, name string) *domain.Blueprint {
	for i := range snap.Blueprints {
		if snap.Blueprints[i].Name == name {
			return &snap.Blueprints[i]
		}
	}
	return nil
}

// resolvePolicy returns the policy a fleet reference points to (the built-in
// default if none is referenced or the "default" object does not exist) with
// all defaults applied.
func (s *service) resolvePolicy(snap *domain.GitOpsSnapshot, name string, kind domain.RolloutKind) domain.RolloutPolicy {
	if name == "" {
		name = domain.DefaultRolloutPolicyName
	}
	var p domain.RolloutPolicy
	found := false
	for _, x := range snap.RolloutPolicies {
		if x.Name == name {
			p, found = x, true
			break
		}
	}
	if !found {
		p = domain.DefaultRolloutPolicy()
		p.Name = name
	}
	p.RolloutPolicySpec = p.RolloutPolicySpec.WithDefaults(kind, s.d.Config.Rollouts.DefaultTimezone)
	return normalizePolicy(p)
}

func normalizePolicy(p domain.RolloutPolicy) domain.RolloutPolicy {
	if p.Waves == nil {
		p.Waves = []domain.WaveSpec{}
	}
	if p.MaintenanceWindows == nil {
		p.MaintenanceWindows = []domain.MaintenanceWindow{}
	}
	return p
}

func (s *service) getFleet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	snap, err := s.snapshot(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	f := findFleet(snap, name)
	if f == nil {
		api.WriteError(w, r, fmt.Errorf("%w: fleet %q", domain.ErrNotFound, name))
		return
	}
	st, err := s.fleetStatistics(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	chainNames := []string{}
	if chain, is := blueprint.Chain(snap, f.Blueprint); len(is) == 0 {
		for _, bp := range chain {
			chainNames = append(chainNames, bp.Name)
		}
	}
	detail := dto.FleetDetail{
		Fleet: normalizeFleet(*f), Status: s.fleetStatus(st, f, s.actors(r.Context())), BlueprintChain: chainNames,
		ConfigPolicy:  s.resolvePolicy(snap, f.Rollout.ConfigPolicy, domain.RolloutKindConfig),
		VersionPolicy: s.resolvePolicy(snap, f.Rollout.VersionPolicy, domain.RolloutKindVersion),
		Issues:        s.issuesFor(snap, "Fleet/"+f.Name), WebURL: s.webURL(f.Source),
	}
	if ref := f.Update.ExecutorRef; ref != "" {
		for i := range snap.Executors {
			if snap.Executors[i].Name == ref {
				sum := s.executorSummary(snap, &snap.Executors[i])
				detail.Executor = &sum
			}
		}
	}
	api.WriteJSON(w, http.StatusOK, detail)
}

func (s *service) matchFleets(w http.ResponseWriter, r *http.Request) {
	var req dto.MatchFleetsRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteError(w, r, err)
		return
	}
	var view domain.AttributeView
	switch {
	case req.AgentID != nil:
		ag, err := s.d.Store.Agents().Get(r.Context(), *req.AgentID)
		if err != nil {
			api.WriteError(w, r, err)
			return
		}
		view = domain.NewAttributeView(ag)
	case req.Attributes != nil:
		view = domain.AttributeView(req.Attributes)
	default:
		api.WriteError(w, r, fmt.Errorf("%w: agentId or attributes is required", domain.ErrInvalidArgument))
		return
	}
	if s.d.Assigner == nil {
		api.WriteError(w, r, fmt.Errorf("%w: assignment service is not available", domain.ErrUnavailable))
		return
	}
	m, err := s.d.Assigner.Match(r.Context(), view)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	resp := dto.MatchFleetsResponse{Fleet: m.Fleet, Candidates: m.Candidates, Error: m.Error, InventoryLabels: m.InventoryLabels}
	if resp.Candidates == nil {
		resp.Candidates = []string{}
	}
	if resp.InventoryLabels == nil {
		resp.InventoryLabels = map[string]string{}
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

// -------------------------------------------------------------- blueprints ----

func (s *service) blueprintRoutes() []api.Route {
	return []api.Route{
		route(http.MethodGet, "/blueprints", "listBlueprints", "read", s.listBlueprints),
		route(http.MethodGet, "/blueprints/{name}", "getBlueprint", "read", s.getBlueprint),
		route(http.MethodPost, "/blueprints/{name}/render", "renderBlueprint", "read", s.renderBlueprint),
	}
}

// usedBy lists the fleets whose blueprint chain contains the blueprint.
func usedBy(snap *domain.GitOpsSnapshot, name string) []string {
	out := []string{}
	for _, f := range snap.Fleets {
		chain, is := blueprint.Chain(snap, f.Blueprint)
		if len(is) > 0 {
			if f.Blueprint == name {
				out = append(out, f.Name)
			}
			continue
		}
		for _, bp := range chain {
			if bp.Name == name {
				out = append(out, f.Name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *service) listBlueprints(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	items := make([]dto.BlueprintSummary, 0, len(snap.Blueprints))
	for _, bp := range snap.Blueprints {
		items = append(items, dto.BlueprintSummary{
			Name: bp.Name, Description: bp.Description, Extends: bp.Extends,
			ParameterCount: len(bp.Parameters), FragmentCount: len(bp.Fragments),
			UsedBy: usedBy(snap, bp.Name), Source: bp.Source,
			IssueCount: len(s.issuesFor(snap, "Blueprint/"+bp.Name)),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	api.WriteJSON(w, http.StatusOK, api.NewList(items))
}

func normalizeBlueprint(bp domain.Blueprint) domain.Blueprint {
	if bp.Parameters == nil {
		bp.Parameters = []domain.ParameterSpec{}
	}
	if bp.Config == nil {
		bp.Config = map[string]any{}
	}
	if bp.Fragments == nil {
		bp.Fragments = []domain.Fragment{}
	}
	if bp.Remove == nil {
		bp.Remove = []string{}
	}
	return bp
}

func (s *service) getBlueprint(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	snap, err := s.snapshot(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	bp := findBlueprint(snap, name)
	if bp == nil {
		api.WriteError(w, r, fmt.Errorf("%w: blueprint %q", domain.ErrNotFound, name))
		return
	}
	chainNames := []string{}
	params := []domain.ParameterSpec{}
	if chain, is := blueprint.Chain(snap, name); len(is) == 0 {
		for _, c := range chain {
			chainNames = append(chainNames, c.Name)
		}
		params, _ = blueprint.MergedParameters(chain)
	} else {
		chainNames = []string{name}
		params = bp.Parameters
		if params == nil {
			params = []domain.ParameterSpec{}
		}
	}
	api.WriteJSON(w, http.StatusOK, dto.BlueprintDetail{
		Blueprint: normalizeBlueprint(*bp), Chain: chainNames, Parameters: params, UsedBy: usedBy(snap, name),
		Issues: s.issuesFor(snap, "Blueprint/"+name), WebURL: s.webURL(bp.Source),
	})
}

func (s *service) renderBlueprint(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req dto.RenderRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteError(w, r, err)
		return
	}
	snap, err := s.snapshot(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	if findBlueprint(snap, name) == nil {
		api.WriteError(w, r, fmt.Errorf("%w: blueprint %q", domain.ErrNotFound, name))
		return
	}

	var (
		attrs      map[string]string
		labels     map[string]string
		agentID    *domain.AgentID
		components *domain.ComponentSet
		fleetName  = req.Fleet
	)
	validate := req.Validate == nil || *req.Validate
	if req.AgentID != nil {
		ag, err := s.d.Store.Agents().Get(r.Context(), *req.AgentID)
		if err != nil {
			api.WriteError(w, r, err)
			return
		}
		plain := *ag
		plain.Labels = nil
		attrs = map[string]string(domain.NewAttributeView(&plain))
		labels = ag.Labels
		id := ag.ID
		agentID = &id
		if fleetName == "" {
			fleetName = ag.Assignment.FleetName
		}
		if validate && !ag.AvailableComponentsHash.IsZero() {
			if cs, err := s.d.Store.Configs().GetComponentSet(r.Context(), ag.AvailableComponentsHash); err == nil {
				components = cs
			}
		}
	} else {
		attrs, labels = blueprint.SplitView(req.Attributes)
	}
	inv := blueprint.ApplyInventory(snap, attrs)
	merged := map[string]string{}
	for k, v := range labels {
		merged[k] = v
	}
	for k, v := range inv.Labels {
		merged[k] = v
	}

	var fleet *domain.Fleet
	if fleetName != "" {
		fleet = findFleet(snap, fleetName)
		if fleet == nil {
			api.WriteError(w, r, fmt.Errorf("%w: fleet %q", domain.ErrNotFound, fleetName))
			return
		}
	}
	res, err := s.d.Renderer.Render(blueprint.RenderInput{
		Snapshot: snap, Fleet: fleet, Blueprint: name,
		Agent:               blueprint.AgentInput{ID: agentID, Attributes: blueprint.ViewFrom(attrs, merged)},
		InventoryParameters: inv.Parameters, Overrides: req.Parameters, Components: components,
	})
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	resp := dto.RenderResponse{
		Blueprint: res.Blueprint, Fleet: res.Fleet, Layers: res.Layers, Parameters: res.Parameters,
		ParameterSources: res.ParameterSources, Hash: res.Hash, Body: string(res.Body), Issues: res.Issues,
	}
	if resp.Layers == nil {
		resp.Layers = []string{}
	}
	if resp.Parameters == nil {
		resp.Parameters = map[string]any{}
	}
	if resp.ParameterSources == nil {
		resp.ParameterSources = map[string]string{}
	}
	if resp.Issues == nil {
		resp.Issues = domain.Issues{}
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------- policies ----

func (s *service) policyRoutes() []api.Route {
	return []api.Route{
		route(http.MethodGet, "/rollout-policies", "listRolloutPolicies", "read", s.listPolicies),
		route(http.MethodGet, "/rollout-policies/{name}", "getRolloutPolicy", "read", s.getPolicy),
	}
}

// allPolicies lists the policies of the snapshot plus the built-in default.
func allPolicies(snap *domain.GitOpsSnapshot) []domain.RolloutPolicy {
	out := make([]domain.RolloutPolicy, 0, len(snap.RolloutPolicies)+1)
	hasDefault := false
	for _, p := range snap.RolloutPolicies {
		if p.Name == domain.DefaultRolloutPolicyName {
			hasDefault = true
		}
		out = append(out, normalizePolicy(p))
	}
	if !hasDefault {
		out = append(out, normalizePolicy(domain.DefaultRolloutPolicy()))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *service) listPolicies(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewList(allPolicies(snap)))
}

func (s *service) getPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	snap, err := s.snapshot(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	for _, p := range allPolicies(snap) {
		if p.Name == name {
			api.WriteJSON(w, http.StatusOK, p)
			return
		}
	}
	api.WriteError(w, r, fmt.Errorf("%w: rollout policy %q", domain.ErrNotFound, name))
}

// --------------------------------------------------------------- executors ----

func (s *service) executorRoutes() []api.Route {
	return []api.Route{route(http.MethodGet, "/executors", "listExecutors", "read", s.listExecutors)}
}

func (s *service) executorSummary(snap *domain.GitOpsSnapshot, e *domain.Executor) dto.ExecutorSummary {
	target := ""
	raw := ""
	switch {
	case e.Webhook != nil:
		raw = e.Webhook.URL
	case e.AWX != nil:
		raw = e.AWX.URL
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		target = u.Scheme + "://" + u.Host + u.Path
		if e.AWX != nil {
			target = strings.TrimRight(target, "/") + fmt.Sprintf("/api/v2/job_templates/%d", e.AWX.JobTemplateID)
		}
	}
	users := []string{}
	for _, f := range snap.Fleets {
		if f.Update.ExecutorRef == e.Name {
			users = append(users, f.Name)
		}
	}
	sort.Strings(users)
	return dto.ExecutorSummary{Name: e.Name, Type: e.Type, Description: e.Description, Target: target, Source: e.Source, UsedBy: users}
}

func (s *service) listExecutors(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	items := make([]dto.ExecutorSummary, 0, len(snap.Executors))
	for i := range snap.Executors {
		items = append(items, s.executorSummary(snap, &snap.Executors[i]))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	api.WriteJSON(w, http.StatusOK, api.NewList(items))
}
