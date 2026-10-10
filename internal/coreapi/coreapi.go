// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package coreapi implements the REST endpoints of the tags agents,
// savedFilters, audit, system and stats.
package coreapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/control"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/leader"
	"github.com/myriarch/myriarch/internal/store"
)

// Deps are the dependencies of the core REST modules.
type Deps struct {
	Store     store.Store
	Bus       eventbus.Bus
	Control   control.AgentControl
	Audit     audit.Recorder
	Elector   leader.Elector
	Config    config.Config
	ReplicaID string
	DevMode   bool
	Logger    *slog.Logger

	// SchemaInfo returns the database server version and the applied
	// migration version (GET /system/info); optional. Implemented by
	// postgres.DB.SchemaInfo.
	SchemaInfo func(ctx context.Context) (serverVersion string, migrationVersion int64, err error)
	// FleetCounts returns per-fleet agent counts in one query (dashboard);
	// optional, the handler falls back to counting over the agent list.
	// Implemented by postgres.DB.FleetCounts.
	FleetCounts func(ctx context.Context) ([]dto.FleetCounts, error)
	// LocalConnections returns the live OpAMP connection count of this replica
	// (optional); GET /system/info overlays it on the replica's heartbeat row,
	// which is up to one heartbeat interval old.
	LocalConnections func() int
	// Now overrides the clock (tests).
	Now func() time.Time
}

type handler struct {
	d   Deps
	log *slog.Logger
	now func() time.Time

	latestMu   sync.Mutex
	latestVal  string
	latestAt   time.Time
	overviewMu sync.Mutex
	overview   *dto.OverviewStats
	overviewAt time.Time
}

// New returns the modules: agents (listAgents, getAgentFacets, getAgent,
// deleteAgent, getAgentEffectiveConfig, getAgentDesiredConfig,
// getAgentComponents, listAgentEvents, restartAgent, resyncAgent),
// savedFilters (listSavedFilters, createSavedFilter, deleteSavedFilter),
// audit (listAuditEvents), system (getSystemInfo) and stats
// (getOverviewStats).
func New(deps Deps) []api.Module {
	h := &handler{d: deps, log: deps.Logger, now: deps.Now}
	if h.log == nil {
		h.log = slog.Default()
	}
	if h.now == nil {
		h.now = func() time.Time { return time.Now().UTC() }
	}
	mod := func(name string, routes ...api.Route) api.Module {
		return api.ModuleFunc{ModuleName: name, RoutesFunc: func() []api.Route { return routes }}
	}
	return []api.Module{
		mod("agents",
			api.Route{Method: "GET", Pattern: "/agents", OperationID: "listAgents", Permission: authz.PermRead, Handler: h.listAgents},
			api.Route{Method: "GET", Pattern: "/agents/facets", OperationID: "getAgentFacets", Permission: authz.PermRead, Handler: h.getAgentFacets},
			api.Route{Method: "GET", Pattern: "/agents/{id}", OperationID: "getAgent", Permission: authz.PermRead, Handler: h.getAgent},
			api.Route{Method: "DELETE", Pattern: "/agents/{id}", OperationID: "deleteAgent", Permission: authz.PermAgentsDelete, Handler: h.deleteAgent},
			api.Route{Method: "GET", Pattern: "/agents/{id}/effective-config", OperationID: "getAgentEffectiveConfig", Permission: authz.PermRead, Handler: h.getAgentEffectiveConfig},
			api.Route{Method: "GET", Pattern: "/agents/{id}/desired-config", OperationID: "getAgentDesiredConfig", Permission: authz.PermRead, Handler: h.getAgentDesiredConfig},
			api.Route{Method: "GET", Pattern: "/agents/{id}/components", OperationID: "getAgentComponents", Permission: authz.PermRead, Handler: h.getAgentComponents},
			api.Route{Method: "GET", Pattern: "/agents/{id}/events", OperationID: "listAgentEvents", Permission: authz.PermRead, Handler: h.listAgentEvents},
			api.Route{Method: "POST", Pattern: "/agents/{id}/actions/restart", OperationID: "restartAgent", Permission: authz.PermAgentsOperate, Handler: h.restartAgent},
			api.Route{Method: "POST", Pattern: "/agents/{id}/actions/resync", OperationID: "resyncAgent", Permission: authz.PermAgentsOperate, Handler: h.resyncAgent},
			api.Route{Method: "POST", Pattern: "/agents/{id}/actions/reenroll", OperationID: "reenrollAgent", Permission: authz.PermEnrollmentManage, Handler: h.reenrollAgent},
		),
		mod("savedFilters",
			api.Route{Method: "GET", Pattern: "/saved-filters", OperationID: "listSavedFilters", Permission: authz.PermRead, Handler: h.listSavedFilters},
			api.Route{Method: "POST", Pattern: "/saved-filters", OperationID: "createSavedFilter", Permission: authz.PermSavedFilters, Handler: h.createSavedFilter},
			api.Route{Method: "DELETE", Pattern: "/saved-filters/{id}", OperationID: "deleteSavedFilter", Permission: authz.PermSavedFilters, Handler: h.deleteSavedFilter},
		),
		mod("audit",
			api.Route{Method: "GET", Pattern: "/audit", OperationID: "listAuditEvents", Permission: authz.PermAuditRead, Handler: h.listAuditEvents},
		),
		mod("system",
			api.Route{Method: "GET", Pattern: "/system/info", OperationID: "getSystemInfo", Permission: authz.PermRead, Handler: h.getSystemInfo},
		),
		mod("stats",
			api.Route{Method: "GET", Pattern: "/stats/overview", OperationID: "getOverviewStats", Permission: authz.PermRead, Handler: h.getOverviewStats},
		),
	}
}

// latestVersion returns the newest stable otelcol-contrib release version
// ("" if none is known), cached for 30 seconds.
func (h *handler) latestVersion(ctx context.Context) string {
	h.latestMu.Lock()
	defer h.latestMu.Unlock()
	if !h.latestAt.IsZero() && h.now().Sub(h.latestAt) < 30*time.Second {
		return h.latestVal
	}
	rel, err := h.d.Store.Releases().Latest(ctx, domain.DistributionContrib, false)
	switch {
	case err == nil:
		h.latestVal = rel.Version
	default:
		h.latestVal = ""
	}
	h.latestAt = h.now()
	return h.latestVal
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) { api.WriteError(w, r, err) }
