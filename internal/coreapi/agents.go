// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package coreapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/store"
)

func badRequest(format string, a ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrInvalidArgument, fmt.Sprintf(format, a...))
}

func queryBool(r *http.Request, name string) (*bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, badRequest("%s must be true or false", name)
	}
	return &b, nil
}

// parseAgentFilter translates the query parameters shared by listAgents and
// getAgentFacets into a store filter.
func (h *handler) parseAgentFilter(r *http.Request) (store.AgentFilter, error) {
	q := r.URL.Query()
	f := store.AgentFilter{Query: strings.TrimSpace(q.Get("q"))}
	// bounded filter inputs (search length, number of values per filter)
	if len(f.Query) > MaxQueryLength {
		return f, badRequest("q must not be longer than %d characters", MaxQueryLength)
	}
	for _, name := range []string{"kind", "status", "fleet", "version", "os", "configSync"} {
		if len(q[name]) > MaxFilterValues {
			return f, badRequest("%s accepts at most %d values", name, MaxFilterValues)
		}
	}
	if len(q["attr"]) > MaxAttrFilters {
		return f, badRequest("attr accepts at most %d pairs", MaxAttrFilters)
	}

	for _, k := range q["kind"] {
		switch kind := domain.AgentKind(k); kind {
		case domain.AgentKindCollector, domain.AgentKindUpdater, domain.AgentKindBridge, domain.AgentKindUnknown:
			f.Kinds = append(f.Kinds, kind)
		default:
			return f, badRequest("unknown kind %q (collector, updater, bridge, unknown)", k)
		}
	}
	for _, s := range q["status"] {
		switch st := domain.ConnectionStatus(s); st {
		case domain.ConnectionOnline, domain.ConnectionOffline:
			f.Status = append(f.Status, st)
		default:
			return f, badRequest("unknown status %q (online, offline)", s)
		}
	}
	var err error
	if f.Healthy, err = queryBool(r, "healthy"); err != nil {
		return f, err
	}
	if f.Unassigned, err = queryBool(r, "unassigned"); err != nil {
		return f, err
	}
	f.Fleets = nonEmpty(q["fleet"])
	f.Versions = nonEmpty(q["version"])
	f.OSTypes = nonEmpty(q["os"])
	for _, c := range q["configSync"] {
		switch cs := domain.ConfigSyncState(c); cs {
		case domain.ConfigSyncNone, domain.ConfigSyncInSync, domain.ConfigSyncPending, domain.ConfigSyncFailed, domain.ConfigSyncDrift:
			f.ConfigSync = append(f.ConfigSync, cs)
		default:
			return f, badRequest("unknown configSync %q (none, in_sync, pending, failed, drift)", c)
		}
	}
	for _, a := range q["attr"] {
		k, v, ok := strings.Cut(a, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return f, badRequest("attr %q must have the form key=value", a)
		}
		if f.Attributes == nil {
			f.Attributes = map[string][]string{}
		}
		f.Attributes[k] = append(f.Attributes[k], v)
	}
	outdated, err := queryBool(r, "outdated")
	if err != nil {
		return f, err
	}
	if outdated != nil {
		latest := h.latestVersion(r.Context())
		f.Kinds = []domain.AgentKind{domain.AgentKindCollector}
		switch {
		case *outdated && latest == "":
			f.OutdatedBelow = "0.0.0" // no release known: nothing is outdated
		case *outdated:
			f.OutdatedBelow = latest
		case latest != "":
			f.UpToDateFrom = latest
		}
	}
	return f, nil
}

// Limits of agent list filters.
const (
	MaxQueryLength  = 200
	MaxFilterValues = 50
	MaxAttrFilters  = 20
)

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (h *handler) listAgents(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseAgentFilter(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	pg, err := api.ParsePagination(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	sort, err := api.ParseSort(r, store.AgentSortFields, "hostname")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	page, err := h.d.Store.Agents().List(r.Context(), store.AgentQuery{Filter: f, Sort: sort, Limit: pg.Limit, Offset: pg.Offset})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	latest := h.latestVersion(r.Context())
	items := make([]dto.AgentSummary, len(page.Items))
	for i := range page.Items {
		items[i] = AgentSummaryOf(&page.Items[i], latest)
	}
	api.WriteJSON(w, http.StatusOK, api.NewPage(items, page.Total, pg))
}

func (h *handler) getAgentFacets(w http.ResponseWriter, r *http.Request) {
	f, err := h.parseAgentFilter(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	facets, err := h.d.Store.Agents().Facets(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, facets)
}

func (h *handler) loadAgent(r *http.Request) (*domain.Agent, error) {
	id, err := api.PathAgentID(r, "id")
	if err != nil {
		return nil, err
	}
	return h.d.Store.Agents().Get(r.Context(), id)
}

func (h *handler) getAgent(w http.ResponseWriter, r *http.Request) {
	a, err := h.loadAgent(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	latest := h.latestVersion(ctx)
	detail := dto.AgentDetail{
		Agent: *a, ConfigSync: a.ConfigSyncState(), CapabilityNames: domain.CapabilityNames(a.Capabilities),
		ActiveRolloutTargets: []domain.RolloutTarget{},
	}
	if a.Kind == domain.AgentKindCollector {
		detail.Outdated = IsOutdated(a.Version, latest)
		detail.LatestVersion = latest
	}
	targets, err := h.d.Store.Rollouts().ActiveTargetsForAgent(ctx, a.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if targets != nil {
		for i := range targets {
			targets[i].Progress = domain.ProgressOf(targets[i]) // consistent with the final status
		}
		detail.ActiveRolloutTargets = targets
	}
	if a.Updater != nil {
		if linked, err := h.d.Store.Agents().Get(ctx, a.Updater.AgentID); err == nil {
			s := AgentSummaryOf(linked, latest)
			detail.LinkedAgent = &s
		} else if !errors.Is(err, domain.ErrNotFound) {
			writeErr(w, r, err)
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, detail)
}

func (h *handler) deleteAgent(w http.ResponseWriter, r *http.Request) {
	a, err := h.loadAgent(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	details := map[string]any{"hostname": a.Hostname, "kind": string(a.Kind), "version": a.Version}
	if a.IsOnline() {
		err := fmt.Errorf("%w: agent %s is online; only offline agents can be removed", domain.ErrPrecondition, a.ID)
		h.record(r, "agent.delete", a.ID.String(), details, err)
		writeErr(w, r, err)
		return
	}
	if err := h.d.Store.Agents().Delete(r.Context(), a.ID); err != nil {
		h.record(r, "agent.delete", a.ID.String(), details, err)
		writeErr(w, r, err)
		return
	}
	h.record(r, "agent.delete", a.ID.String(), details, nil)
	if h.d.Bus != nil {
		if err := h.d.Bus.Publish(r.Context(), eventbus.AgentRemoved, eventbus.AgentIDsPayload{AgentIDs: []domain.AgentID{a.ID}}); err != nil {
			h.log.Warn("cannot publish agent.removed", "agent_id", a.ID, "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// reenrollAgent clears the credential binding of an agent (admin action): the
// next connection that authenticates with a valid credential claims the
// instance uid. Used after an agent was re-installed with another enrollment
// token or certificate.
func (h *handler) reenrollAgent(w http.ResponseWriter, r *http.Request) {
	a, err := h.loadAgent(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	details := map[string]any{"hostname": a.Hostname}
	if a.EnrollmentTokenID != nil {
		details["previousTokenId"] = a.EnrollmentTokenID.String()
	}
	if err := h.d.Store.Agents().SetCredentialBinding(r.Context(), a.ID, nil, ""); err != nil {
		h.record(r, "agent.reenroll", a.ID.String(), details, err)
		writeErr(w, r, err)
		return
	}
	if h.d.Control != nil {
		// sessions of this replica and the others re-read the binding
		if err := h.d.Control.DesiredStateChanged(r.Context(), []domain.AgentID{a.ID}); err != nil {
			h.log.Warn("cannot notify the agent sessions about the cleared binding", "agent_id", a.ID, "error", err)
		}
	}
	h.record(r, "agent.reenroll", a.ID.String(), details, nil)
	api.WriteJSON(w, http.StatusAccepted, dto.ActionAccepted{Accepted: true, Message: "credential binding cleared: the next connection with a valid credential claims this agent"})
}

func (h *handler) record(r *http.Request, action, targetID string, details map[string]any, err error) {
	if h.d.Audit == nil {
		return
	}
	h.d.Audit.Record(r.Context(), audit.FromRequest(r, action, "agent", targetID, details, err))
}

func configDocument(hash domain.Hash, body []byte, contentType, source string, updated *time.Time) dto.ConfigDocument {
	if contentType == "" {
		contentType = domain.RemoteConfigContentType
	}
	return dto.ConfigDocument{Hash: hash, ContentType: contentType, Body: string(body), UpdatedAt: updated, Source: source}
}

func (h *handler) getAgentEffectiveConfig(w http.ResponseWriter, r *http.Request) {
	a, err := h.loadAgent(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if a.Kind == domain.AgentKindBridge {
		writeErr(w, r, fmt.Errorf("%w: agent %s is an OpAMP Bridge; the server keeps no effective config of a Bridge", domain.ErrNotFound, a.ID))
		return
	}
	if a.EffectiveConfigHash.IsZero() {
		writeErr(w, r, fmt.Errorf("%w: agent %s has not reported an effective config", domain.ErrNotFound, a.ID))
		return
	}
	c, err := h.d.Store.Configs().GetEffective(r.Context(), a.EffectiveConfigHash)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Effective configs come from the agent and may contain expanded
	// credentials: they are redacted for every caller.
	body, wasRedacted := RedactEffectiveConfig(c.Body)
	doc := configDocument(c.Hash, body, c.ContentType, "effective", a.EffectiveConfigUpdatedAt)
	doc.Redacted = wasRedacted
	api.WriteJSON(w, http.StatusOK, doc)
}

func (h *handler) getAgentDesiredConfig(w http.ResponseWriter, r *http.Request) {
	which := r.URL.Query().Get("which")
	if which == "" {
		which = "desired"
	}
	if which != "desired" && which != "assigned" {
		writeErr(w, r, badRequest("which must be desired or assigned"))
		return
	}
	a, err := h.loadAgent(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	hash := a.Assignment.DesiredConfigHash
	if which == "assigned" {
		hash = a.Assignment.AssignedConfigHash
	}
	if hash.IsZero() {
		writeErr(w, r, fmt.Errorf("%w: agent %s has no %s config", domain.ErrNotFound, a.ID, which))
		return
	}
	c, err := h.d.Store.Configs().GetRendered(r.Context(), hash)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	updated := &c.CreatedAt
	if which == "assigned" && a.Assignment.AssignedAt != nil {
		updated = a.Assignment.AssignedAt
	}
	api.WriteJSON(w, http.StatusOK, configDocument(c.Hash, c.Body, c.ContentType, which, updated))
}

func (h *handler) getAgentComponents(w http.ResponseWriter, r *http.Request) {
	a, err := h.loadAgent(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if a.AvailableComponentsHash.IsZero() {
		writeErr(w, r, fmt.Errorf("%w: agent %s has not reported its available components", domain.ErrNotFound, a.ID))
		return
	}
	cs, err := h.d.Store.Configs().GetComponentSet(r.Context(), a.AvailableComponentsHash)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, cs)
}

func (h *handler) listAgentEvents(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathAgentID(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	limit, before := 100, int64(0)
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > api.MaxLimit {
			writeErr(w, r, badRequest("limit must be between 1 and %d", api.MaxLimit))
			return
		}
		limit = n
	}
	if v := r.URL.Query().Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, r, badRequest("before must be >= 0"))
			return
		}
		before = n
	}
	if _, err := h.d.Store.Agents().Get(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	events, err := h.d.Store.Agents().ListEvents(r.Context(), id, limit, before)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := dto.AgentEventList{Items: events}
	if out.Items == nil {
		out.Items = []domain.AgentEvent{}
	}
	if len(events) == limit {
		out.NextBefore = events[len(events)-1].ID
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (h *handler) restartAgent(w http.ResponseWriter, r *http.Request) {
	a, err := h.loadAgent(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	details := map[string]any{"hostname": a.Hostname}
	switch {
	case a.Kind == domain.AgentKindBridge:
		err = fmt.Errorf("%w: agent %s is an OpAMP Bridge: %s", domain.ErrPrecondition, a.ID, domain.BridgeRestartRefused)
	case !a.IsOnline():
		err = fmt.Errorf("%w: agent %s is offline", domain.ErrPrecondition, a.ID)
	case !domain.HasCapability(a.Capabilities, domain.CapAcceptsRestartCommand):
		err = fmt.Errorf("%w: agent %s does not accept restart commands (capability AcceptsRestartCommand missing)", domain.ErrPrecondition, a.ID)
	}
	if err != nil {
		h.record(r, "agent.restart", a.ID.String(), details, err)
		writeErr(w, r, err)
		return
	}
	if err := h.d.Control.Restart(r.Context(), a.ID, authz.FromContext(r.Context()).String()); err != nil {
		h.record(r, "agent.restart", a.ID.String(), details, err)
		writeErr(w, r, err)
		return
	}
	h.record(r, "agent.restart", a.ID.String(), details, nil)
	api.WriteJSON(w, http.StatusAccepted, dto.ActionAccepted{Accepted: true, Message: "restart command queued"})
}

func (h *handler) resyncAgent(w http.ResponseWriter, r *http.Request) {
	a, err := h.loadAgent(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	details := map[string]any{"hostname": a.Hostname, "failedHashCleared": !a.Assignment.FailedConfigHash.IsZero()}
	// clear first: this also takes effect for offline agents when they reconnect
	if err := h.d.Store.Agents().ClearFailedConfig(r.Context(), a.ID); err != nil {
		h.record(r, "agent.resync", a.ID.String(), details, err)
		writeErr(w, r, err)
		return
	}
	if err := h.d.Control.Resync(r.Context(), a.ID, authz.FromContext(r.Context()).String()); err != nil {
		h.record(r, "agent.resync", a.ID.String(), details, err)
		writeErr(w, r, err)
		return
	}
	h.record(r, "agent.resync", a.ID.String(), details, nil)
	msg := "resync queued"
	if !a.IsOnline() {
		msg = "agent is offline: the desired config is offered when it reconnects"
	}
	api.WriteJSON(w, http.StatusAccepted, dto.ActionAccepted{Accepted: true, Message: msg})
}
