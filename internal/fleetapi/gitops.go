// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// ------------------------------------------------------------------- gitops ----

func (s *service) gitopsRoutes() []api.Route {
	routes := []api.Route{
		route(http.MethodGet, "/gitops/status", "getGitOpsStatus", "read", s.gitopsStatus),
		route(http.MethodGet, "/gitops/syncs", "listGitOpsSyncs", "read", s.gitopsSyncs),
		route(http.MethodGet, "/gitops/objects", "listGitOpsObjects", "read", s.gitopsObjects),
		route(http.MethodPost, "/gitops/actions/sync", "triggerGitOpsSync", "gitops:sync", s.gitopsTrigger),
	}
	if s.d.Syncer != nil {
		routes = append(routes, s.d.Syncer.WebhookHandler())
	}
	return routes
}

func (s *service) gitopsStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.d.Store.GitOps().GetStatus(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	if st.Issues == nil {
		st.Issues = domain.Issues{}
	}
	if st.ObjectCounts == nil {
		st.ObjectCounts = map[domain.ObjectKind]int{}
	}
	api.WriteJSON(w, http.StatusOK, st)
}

func (s *service) gitopsSyncs(w http.ResponseWriter, r *http.Request) {
	pg, err := api.ParsePagination(r)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	list, total, err := s.d.Store.GitOps().ListSyncs(r.Context(), pg.Limit, pg.Offset)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	for i := range list {
		if list[i].Issues == nil {
			list[i].Issues = domain.Issues{}
		}
	}
	api.WriteJSON(w, http.StatusOK, api.NewPage(list, total, pg))
}

func (s *service) gitopsObjects(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	items := []dto.GitOpsObjectRef{}
	add := func(kind domain.ObjectKind, name string, src domain.SourceRef) {
		items = append(items, dto.GitOpsObjectRef{Kind: kind, Name: name, Path: src.Path, Commit: src.Commit, WebURL: s.webURL(src)})
	}
	for _, o := range snap.Blueprints {
		add(domain.KindBlueprint, o.Name, o.Source)
	}
	for _, o := range snap.RolloutPolicies {
		add(domain.KindRolloutPolicy, o.Name, o.Source)
	}
	for _, o := range snap.Executors {
		add(domain.KindExecutor, o.Name, o.Source)
	}
	for _, o := range snap.Inventories {
		add(domain.KindInventory, o.Name, o.Source)
	}
	for _, o := range snap.Fleets {
		add(domain.KindFleet, o.Name, o.Source)
	}
	order := map[domain.ObjectKind]int{}
	for i, k := range domain.AllObjectKinds {
		order[k] = i
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return order[items[i].Kind] < order[items[j].Kind]
		}
		return items[i].Name < items[j].Name
	})
	api.WriteJSON(w, http.StatusOK, api.NewList(items))
}

func (s *service) gitopsTrigger(w http.ResponseWriter, r *http.Request) {
	if s.d.Syncer == nil {
		api.WriteError(w, r, fmt.Errorf("%w: GitOps syncer is not available", domain.ErrUnavailable))
		return
	}
	by := "system"
	if p := principalOf(r); p != nil {
		by = p.String()
	}
	err := s.d.Syncer.RequestSync(r.Context(), "manual", by)
	s.recordAudit(r, "gitops.sync", "gitops", "", nil, err)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, dto.ActionAccepted{Accepted: true, Message: "sync requested"})
}

// --------------------------------------------------------------- artifacts ----

func (s *service) artifactRoutes() []api.Route {
	routes := []api.Route{
		route(http.MethodGet, "/artifacts", "listArtifacts", "read", s.listArtifacts),
		route(http.MethodPost, "/artifacts/actions/prefetch", "prefetchArtifacts", "artifacts:manage", s.prefetchArtifacts),
		route(http.MethodGet, "/artifacts/{id}", "getArtifact", "read", s.getArtifact),
		route(http.MethodDelete, "/artifacts/{id}", "deleteArtifact", "artifacts:manage", s.deleteArtifact),
	}
	if s.d.Artifacts != nil {
		dl := route(http.MethodGet, "/artifacts/{id}/content/{filename}", "downloadArtifact", "custom", s.d.Artifacts.ServeContent)
		dl.LongTransfer = true
		routes = append(routes, dl)
	}
	return routes
}

func (s *service) listArtifacts(w http.ResponseWriter, r *http.Request) {
	pg, err := api.ParsePagination(r)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	dist := r.URL.Query().Get("distribution")
	if dist != "" && dist != domain.DistributionContrib && dist != domain.DistributionSupervisor {
		api.WriteError(w, r, fmt.Errorf("%w: unknown distribution %q", domain.ErrInvalidArgument, dist))
		return
	}
	f := store.ArtifactFilter{Distribution: dist, Version: domain.NormalizeVersion(r.URL.Query().Get("version")), Limit: pg.Limit, Offset: pg.Offset}
	for _, v := range queryValues(r, "status") {
		st := domain.ArtifactStatus(v)
		switch st {
		case domain.ArtifactPending, domain.ArtifactDownloading, domain.ArtifactReady, domain.ArtifactFailed:
			f.Status = append(f.Status, st)
		default:
			api.WriteError(w, r, fmt.Errorf("%w: unknown artifact status %q", domain.ErrInvalidArgument, v))
			return
		}
	}
	list, total, err := s.d.Store.Artifacts().List(r.Context(), f)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewPage(list, total, pg))
}

func (s *service) getArtifact(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	a, err := s.d.Store.Artifacts().Get(r.Context(), id)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, a)
}

func (s *service) deleteArtifact(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	if s.d.Artifacts != nil {
		err = s.d.Artifacts.Delete(r.Context(), id)
	} else {
		err = s.d.Store.Artifacts().Delete(r.Context(), id)
	}
	s.recordAudit(r, "artifact.delete", "artifact", id.String(), nil, err)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *service) prefetchArtifacts(w http.ResponseWriter, r *http.Request) {
	var req dto.PrefetchRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteError(w, r, err)
		return
	}
	if s.d.Artifacts == nil {
		api.WriteError(w, r, fmt.Errorf("%w: artifact cache is not available", domain.ErrUnavailable))
		return
	}
	list, plan, err := s.d.Artifacts.StartPrefetchResolved(r.Context(), req.Distribution, domain.NormalizeVersion(req.Version), req.Platforms)
	// Record the platforms that were actually resolved (request, configuration or connected collectors),
	// not only what the request carried.
	s.recordAudit(r, "artifact.prefetch", "artifact", req.Distribution+"/"+req.Version,
		map[string]any{"distribution": req.Distribution, "version": req.Version, "platforms": plan.Platforms, "platformsSource": plan.Source}, err)
	if err != nil {
		if isInvalidArgument(err) {
			// The contract documents 422 with details for invalid prefetch requests.
			err = domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: "invalid_request", Message: err.Error(), Path: "body"})
		}
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, api.NewList(list))
}

var _ = artifact.NormalizeArch
