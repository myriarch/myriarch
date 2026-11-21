// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
)

// Pull and merge requests of the gitops-pr executor (ADR-0052). The executor
// hands the update over as a request; the engine follows the state of the
// request (pollRequests) and closes open requests when the rollout ends or is
// withdrawn. A merged request never marks a target as done: that stays the job
// of evalVersion (the reported version plus the health wait).

// requestPollTimeout bounds one RequestStates call of a tick.
const requestPollTimeout = 10 * time.Second

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// requestProvider returns the provider named by the reference of a request.
func requestProvider(ref string) string {
	if strings.HasPrefix(ref, domain.ChangeRequestRefGitLab) {
		return "gitlab"
	}
	return "github"
}

// requestNoun is "pull request" for GitHub and "merge request" for GitLab.
func requestNoun(ref string) string {
	if requestProvider(ref) == "gitlab" {
		return "merge request"
	}
	return "pull request"
}

// openChange returns the open request of a target that updates the version
// (nil for rollback requests, which stay open for the operators to merge).
func openChange(t *domain.RolloutTarget) *domain.ChangeRequest {
	if t.Progress == nil {
		return nil
	}
	if cr := t.Progress.ChangeRequest; cr != nil && cr.State == domain.ChangeRequestOpen && !cr.Rollback {
		return cr
	}
	return nil
}

// mergedUpdate reports that the request that updates the version of the target
// was merged: Git holds the new version even if the agent still runs the old
// one, so a rollback needs a rollback request.
func mergedUpdate(t *domain.RolloutTarget) bool {
	if t.Progress == nil {
		return false
	}
	cr := t.Progress.ChangeRequest
	return cr != nil && cr.State == domain.ChangeRequestMerged && !cr.Rollback
}

// setChangeRequest stores the request of a target with its progress.
func (rc *runCtx) setChangeRequest(i int, cr domain.ChangeRequest) {
	t := &rc.targets[i]
	var p domain.TargetProgress
	if t.Progress != nil {
		p = *t.Progress
	}
	p.ChangeRequest = &cr
	t.Progress = &p
	rc.dirty[i] = struct{}{}
	rc.touched = true
}

// recordRequest notes the request an executor result names for target i (an
// update request, or a rollback request when rollback is set). It audits each
// new reference once.
func (rc *runCtx) recordRequest(i int, res executor.TargetResult, rollback bool, seen map[string]bool) {
	if !domain.IsChangeRequestRef(res.ExternalRef) {
		return
	}
	t := &rc.targets[i]
	rc.setChangeRequest(i, domain.ChangeRequest{
		Provider: requestProvider(res.ExternalRef), Ref: res.ExternalRef, URL: res.ExternalURL,
		State: domain.ChangeRequestOpen, Rollback: rollback,
	})
	if seen[res.ExternalRef] {
		return
	}
	seen[res.ExternalRef] = true
	rc.e.auditSystem(rc.ctx, "rollout.change_request.opened", rc.r, map[string]any{
		"ref": res.ExternalRef, "url": res.ExternalURL, "wave": t.Wave, "rollback": rollback,
	})
}

// pollRequests asks the executor for the state of the requests of in-flight
// targets (the executor throttles itself) and applies it: a merge is recorded,
// a request closed without merge fails its targets.
func (rc *runCtx) pollRequests() {
	r := rc.r
	if r.Kind != domain.RolloutKindVersion {
		return
	}
	ex, ok := rc.e.deps.Executors.Get(r.Executor)
	if !ok {
		return
	}
	tr, ok := ex.(executor.RequestTracker)
	if !ok {
		return
	}
	var refs []string
	seen := map[string]bool{}
	for i := range rc.targets {
		t := &rc.targets[i]
		if t.Status != domain.TargetInProgress || !domain.IsChangeRequestRef(t.ExecutorRef) {
			continue
		}
		if t.Progress != nil && t.Progress.ChangeRequest != nil && t.Progress.ChangeRequest.State != domain.ChangeRequestOpen {
			continue
		}
		if !seen[t.ExecutorRef] {
			seen[t.ExecutorRef] = true
			refs = append(refs, t.ExecutorRef)
		}
	}
	if len(refs) == 0 {
		return
	}
	spec, def := rc.e.updateSpec(rc.ctx, r)
	ctx, cancel := context.WithTimeout(rc.ctx, requestPollTimeout)
	defer cancel()
	states, err := tr.RequestStates(ctx, executor.StatusRequest{RolloutID: r.ID, Refs: refs, Spec: spec, Definition: def})
	if err != nil {
		rc.e.log.Warn("checking the requests of the rollout failed", "rollout_id", r.ID, "executor", r.Executor, "error", err)
	}
	audited := map[string]bool{}
	for i := range rc.targets {
		t := &rc.targets[i]
		if t.Status != domain.TargetInProgress {
			continue
		}
		st, ok := states[t.ExecutorRef]
		if !ok {
			continue
		}
		var prev domain.ChangeRequest
		if t.Progress != nil && t.Progress.ChangeRequest != nil {
			prev = *t.Progress.ChangeRequest
		}
		if prev.State != "" && prev.State != domain.ChangeRequestOpen {
			continue
		}
		cr := domain.ChangeRequest{
			Provider: requestProvider(st.Ref), Ref: st.Ref, URL: st.URL, State: st.State, MergeCommit: st.MergeCommit, Rollback: prev.Rollback,
		}
		if cr == prev {
			continue
		}
		rc.setChangeRequest(i, cr)
		switch st.State {
		case domain.ChangeRequestMerged:
			if !audited[st.Ref] {
				audited[st.Ref] = true
				rc.e.auditSystem(rc.ctx, "rollout.change_request.merged", r, map[string]any{"ref": st.Ref, "url": st.URL, "mergeCommit": st.MergeCommit})
			}
			rc.refreshProgress(i)
		case domain.ChangeRequestClosed:
			if !audited[st.Ref] {
				audited[st.Ref] = true
				rc.e.auditSystem(rc.ctx, "rollout.change_request.closed", r, map[string]any{"ref": st.Ref, "url": st.URL, "reason": "closed without merge"})
			}
			rc.setTarget(i, domain.TargetFailed, fmt.Sprintf("%s %s was closed without merge", requestNoun(st.Ref), st.Ref))
		default:
			rc.refreshProgress(i)
		}
	}
}

// closeOpenRequests closes the open update requests of all targets (best
// effort) when the rollout ends or is rolled back, and records that in the
// targets so that later ticks do not close them again.
func (rc *runCtx) closeOpenRequests(reason string) {
	if rc.r.Kind != domain.RolloutKindVersion {
		return
	}
	if ex, ok := rc.e.deps.Executors.Get(rc.r.Executor); !ok {
		return
	} else if _, ok := ex.(executor.RequestCloser); !ok {
		return
	}
	refs := map[domain.AgentID]string{}
	for i := range rc.targets {
		if cr := openChange(&rc.targets[i]); cr != nil {
			refs[rc.targets[i].AgentID] = cr.Ref
		}
	}
	if len(refs) == 0 {
		return
	}
	out := rc.e.closeRequests(rc.ctx, rc.r, refs, reason)
	if out == nil {
		return // the call failed or is still running: the next tick tries again
	}
	for i := range rc.targets {
		cr := openChange(&rc.targets[i])
		if cr == nil {
			continue
		}
		if _, ok := refs[rc.targets[i].AgentID]; !ok {
			continue
		}
		next := *cr
		next.State = domain.ChangeRequestClosed
		if !out.closed[cr.Ref] {
			next.State = domain.ChangeRequestMerged // it was not open any more
		}
		rc.setChangeRequest(i, next)
		rc.refreshProgress(i)
	}
}

// closeOutcome is the result of a closeRequests call.
type closeOutcome struct {
	closed map[string]bool // refs the executor closed
}

// closeRequests closes the open requests named by refs through the executor
// (best effort, bounded like every executor call) and audits each closed
// request. It returns nil if the executor cannot close requests, the call
// failed or did not finish in time.
func (e *Engine) closeRequests(ctx context.Context, r *domain.Rollout, refs map[domain.AgentID]string, reason string) *closeOutcome {
	ex, ok := e.deps.Executors.Get(r.Executor)
	if !ok {
		return nil
	}
	rcl, ok := ex.(executor.RequestCloser)
	if !ok {
		return nil
	}
	spec, def := e.updateSpec(ctx, r)
	ids := make([]domain.AgentID, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	req := executor.CancelRequest{RolloutID: r.ID, AgentIDs: ids, Refs: refs, Spec: spec, Definition: def}
	var (
		mu  sync.Mutex
		out *closeOutcome
	)
	e.bestEffort(ctx, func(cctx context.Context) {
		closed, err := rcl.CloseRequests(cctx, req)
		for _, c := range closed {
			e.auditSystem(cctx, "rollout.change_request.closed", r, map[string]any{"ref": c.Ref, "url": c.URL, "reason": reason})
		}
		if err != nil {
			e.log.Warn("closing requests failed", "rollout_id", r.ID, "executor", r.Executor, "error", err)
			return
		}
		o := &closeOutcome{closed: map[string]bool{}}
		for _, c := range closed {
			o.closed[c.Ref] = true
		}
		mu.Lock()
		out = o
		mu.Unlock()
	})
	mu.Lock()
	defer mu.Unlock()
	return out
}
