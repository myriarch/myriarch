// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

func (s *service) rolloutRoutes() []api.Route {
	op := "rollouts:operate"
	return []api.Route{
		route(http.MethodGet, "/rollouts", "listRollouts", "read", s.listRollouts),
		route(http.MethodGet, "/rollouts/{id}", "getRollout", "read", s.getRollout),
		route(http.MethodGet, "/rollouts/{id}/targets", "listRolloutTargets", "read", s.listTargets),
		route(http.MethodPost, "/rollouts/{id}/actions/pause", "pauseRollout", op, s.action(domain.ActionPause)),
		route(http.MethodPost, "/rollouts/{id}/actions/resume", "resumeRollout", op, s.action(domain.ActionResume)),
		route(http.MethodPost, "/rollouts/{id}/actions/abort", "abortRollout", op, s.action(domain.ActionAbort)),
		route(http.MethodPost, "/rollouts/{id}/actions/approve", "approveRolloutWave", op, s.approve),
		route(http.MethodPost, "/rollouts/{id}/actions/rollback", "rollbackRollout", op, s.action(domain.ActionRollback)),
		route(http.MethodPost, "/rollouts/{id}/actions/retry-failed", "retryFailedRolloutTargets", op, s.action(domain.ActionRetryFailed)),
	}
}

func parseRolloutStatuses(vals []string) ([]domain.RolloutStatus, error) {
	valid := map[domain.RolloutStatus]bool{
		domain.RolloutPending: true, domain.RolloutRunning: true, domain.RolloutWaitingApproval: true, domain.RolloutPaused: true,
		domain.RolloutCompleted: true, domain.RolloutFailed: true, domain.RolloutAborted: true, domain.RolloutRollingBack: true,
		domain.RolloutRolledBack: true, domain.RolloutSuperseded: true,
	}
	var out []domain.RolloutStatus
	for _, v := range vals {
		st := domain.RolloutStatus(v)
		if !valid[st] {
			return nil, fmt.Errorf("%w: unknown rollout status %q", domain.ErrInvalidArgument, v)
		}
		out = append(out, st)
	}
	return out, nil
}

func parseTargetStatuses(vals []string) ([]domain.TargetStatus, error) {
	valid := map[domain.TargetStatus]bool{
		domain.TargetPending: true, domain.TargetWaitingWindow: true, domain.TargetInProgress: true, domain.TargetVerifying: true,
		domain.TargetSucceeded: true, domain.TargetFailed: true, domain.TargetSkipped: true, domain.TargetCancelled: true, domain.TargetRolledBack: true,
	}
	var out []domain.TargetStatus
	for _, v := range vals {
		st := domain.TargetStatus(v)
		if !valid[st] {
			return nil, fmt.Errorf("%w: unknown target status %q", domain.ErrInvalidArgument, v)
		}
		out = append(out, st)
	}
	return out, nil
}

// parseTargetOrder maps the sort query parameter of the targets list.
func parseTargetOrder(v string) (store.TargetOrder, error) {
	switch v {
	case "", "wave":
		return store.TargetOrderWave, nil
	case "activity":
		return store.TargetOrderActivity, nil
	}
	return "", fmt.Errorf("%w: unsupported sort %q (allowed: wave, activity)", domain.ErrInvalidArgument, v)
}

func (s *service) listRollouts(w http.ResponseWriter, r *http.Request) {
	pg, err := api.ParsePagination(r)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	f := store.RolloutFilter{Limit: pg.Limit, Offset: pg.Offset}
	if fleet := r.URL.Query().Get("fleet"); fleet != "" {
		f.Fleets = []string{fleet}
	}
	if k := r.URL.Query().Get("kind"); k != "" {
		kind := domain.RolloutKind(k)
		if kind != domain.RolloutKindConfig && kind != domain.RolloutKindVersion {
			api.WriteError(w, r, fmt.Errorf("%w: kind must be config or version", domain.ErrInvalidArgument))
			return
		}
		f.Kinds = []domain.RolloutKind{kind}
	}
	if f.Status, err = parseRolloutStatuses(queryValues(r, "status")); err != nil {
		api.WriteError(w, r, err)
		return
	}
	if active, err := queryBool(r, "active"); err != nil {
		api.WriteError(w, r, err)
		return
	} else if active != nil {
		f.ActiveOnly = *active
	}
	list, total, err := s.d.Store.Rollouts().List(r.Context(), f)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	names := s.actors(r.Context())
	items := make([]dto.RolloutSummary, 0, len(list))
	for _, ro := range list {
		items = append(items, rolloutSummaryNamed(ro, names))
	}
	api.WriteJSON(w, http.StatusOK, api.NewPage(items, total, pg))
}

func normalizeRollout(ro *domain.Rollout) *domain.Rollout {
	if ro.Waves == nil {
		ro.Waves = []domain.RolloutWave{}
	}
	if ro.Policy.Waves == nil {
		ro.Policy.Waves = []domain.WaveSpec{}
	}
	if ro.Policy.MaintenanceWindows == nil {
		ro.Policy.MaintenanceWindows = []domain.MaintenanceWindow{}
	}
	return ro
}

func (s *service) getRollout(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	ro, err := s.d.Store.Rollouts().Get(r.Context(), id)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s.presentRollout(r.Context(), ro))
}

func (s *service) listTargets(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	pg, err := api.ParsePagination(r)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	f := store.TargetFilter{Limit: pg.Limit, Offset: pg.Offset}
	if v := r.URL.Query().Get("wave"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 0 {
			api.WriteError(w, r, fmt.Errorf("%w: wave must be a non-negative integer", domain.ErrInvalidArgument))
			return
		}
		f.Wave = &n
	}
	if v := r.URL.Query().Get("agent"); v != "" {
		agent, err := uuid.Parse(v)
		if err != nil {
			api.WriteError(w, r, fmt.Errorf("%w: agent must be a UUID", domain.ErrInvalidArgument))
			return
		}
		f.Agent = &agent
	}
	if f.Status, err = parseTargetStatuses(queryValues(r, "status")); err != nil {
		api.WriteError(w, r, err)
		return
	}
	if f.Order, err = parseTargetOrder(r.URL.Query().Get("sort")); err != nil {
		api.WriteError(w, r, err)
		return
	}
	if _, err := s.d.Store.Rollouts().Get(r.Context(), id); err != nil {
		api.WriteError(w, r, err)
		return
	}
	list, total, err := s.d.Store.Rollouts().ListTargets(r.Context(), id, f)
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	for i := range list {
		// a target that was ended from outside the engine's evaluation (abort,
		// supersede, rollback) shows a phase that agrees with its status
		list[i].Progress = domain.ProgressOf(list[i])
	}
	api.WriteJSON(w, http.StatusOK, api.NewPage(list, total, pg))
}

// action serves the rollout actions with an optional {"reason": ...} body.
func (s *service) action(act domain.RolloutAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathUUID(r, "id")
		if err != nil {
			api.WriteError(w, r, err)
			return
		}
		var body dto.RolloutActionRequest
		if err := api.DecodeJSONOptional(r, &body); err != nil {
			api.WriteError(w, r, err)
			return
		}
		s.runAction(w, r, id, act, 0, body.Reason)
	}
}

func (s *service) approve(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		api.WriteError(w, r, err)
		return
	}
	var body dto.ApproveWaveRequest
	if err := api.DecodeJSON(r, &body); err != nil {
		api.WriteError(w, r, err)
		return
	}
	s.runAction(w, r, id, domain.ActionApprove, body.Wave, body.Reason)
}

func (s *service) runAction(w http.ResponseWriter, r *http.Request, id uuid.UUID, act domain.RolloutAction, wave int, reason string) {
	if s.d.Engine == nil {
		api.WriteError(w, r, fmt.Errorf("%w: rollout engine is not available", domain.ErrUnavailable))
		return
	}
	p := authz.FromContext(r.Context())
	ro, err := s.d.Engine.Action(audit.WithRequest(r.Context(), r), id, act, wave, reason, p)
	if err != nil {
		// rejected and failed operator actions are audited as well
		details := map[string]any{}
		if reason != "" {
			details["reason"] = reason
		}
		if wave != 0 {
			details["wave"] = wave
		}
		s.recordAudit(r, "rollout."+string(act), "rollout", id.String(), details, err)
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s.presentRollout(r.Context(), ro))
}
