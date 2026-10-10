// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/store"
)

// SyncNow performs one synchronization cycle: fetch the revision, compare
// with the applied one, validate, apply atomically. It is serialized (never
// two cycles at once). Network errors are returned (and recorded in the
// status); an invalid revision is NOT an error (it is recorded and the old
// snapshot stays active).
func (s *Syncer) SyncNow(ctx context.Context, trigger string) error {
	if s.src == nil {
		return errors.New("gitops syncer is disabled")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// one cycle (fetch, validation, apply, reassignment) has a total budget
	ctx, cancel := context.WithTimeout(ctx, syncBudget)
	defer cancel()
	defer s.cycle("gitops_sync")()

	started := s.now()
	gs := s.deps.Store.GitOps()
	prev, err := gs.GetStatus(ctx)
	if err != nil {
		return fmt.Errorf("load gitops status: %w", err)
	}

	f, err := s.src.fetch(ctx)
	if err != nil {
		s.handleFetchError(ctx, prev, trigger, started, err)
		return err
	}
	defer f.release()
	s.consecutiveErrors = 0
	s.lastErrorText = ""
	commit := f.commit

	appliedSHA := ""
	if prev.AppliedCommit != nil {
		appliedSHA = prev.AppliedCommit.SHA
	}

	// Unchanged against the applied revision.
	if f.revision == appliedSHA {
		s.lastInvalid = ""
		return s.handleUnchanged(ctx, f, prev, trigger, started)
	}
	// Same invalid revision as last time: do not re-validate and spam history.
	if f.revision == s.lastInvalid && prev.State == "error" {
		st := *prev
		now := s.now()
		st.LastSyncAt = &now
		st.LatestCommit = &commit
		if err := gs.SaveStatus(ctx, st); err != nil {
			s.log.Warn("save gitops status failed", "error", err)
		}
		return nil
	}

	// A revision we have not seen: validate and apply.
	syncing := *prev
	syncing.State = "syncing"
	now := s.now()
	syncing.LastSyncAt = &now
	syncing.LatestCommit = &commit
	syncing.Source, syncing.Branch, syncing.Path = s.src.describe(), s.cfg.Branch, s.cfg.Path
	_ = gs.SaveStatus(ctx, syncing)

	res := ValidateLimited(f.fsys, f.root, f.revision, s.deps.Renderer, nil, s.limits())
	issues := capIssues(res.Issues)
	finished := s.now()
	syncRec := domain.GitOpsSync{
		StartedAt: started, FinishedAt: &finished, Trigger: trigger, Commit: &commit, Issues: issues,
	}

	if !res.Valid() {
		s.lastInvalid = f.revision
		syncRec.Result = "invalid"
		if _, err := gs.RecordSync(ctx, syncRec); err != nil {
			s.log.Error("record gitops sync failed", "error", err)
		}
		st := *prev
		st.Source, st.Branch, st.Path = s.src.describe(), s.cfg.Branch, s.cfg.Path
		st.State = "error"
		st.LastSyncAt = &finished
		st.LatestCommit = &commit
		st.Issues = issues
		if st.ObjectCounts == nil {
			st.ObjectCounts = map[domain.ObjectKind]int{}
		}
		if err := gs.SaveStatus(ctx, st); err != nil {
			s.log.Error("save gitops status failed", "error", err)
		}
		s.syncTotal.WithLabelValues("invalid").Inc()
		s.log.Warn("gitops revision is invalid, keeping the previous snapshot", "commit", shortSHA(f.revision), "errors", countErrors(res.Issues))
		s.notifyInvalid(ctx, commit, res.Issues)
		s.publish(ctx, f.revision, "invalid")
		return nil
	}

	// Apply.
	syncRec.Result = "applied"
	changes, err := gs.ApplySnapshot(ctx, *res.Snapshot, syncRec)
	if err != nil {
		s.syncTotal.WithLabelValues("error").Inc()
		wrapped := fmt.Errorf("apply gitops snapshot %s: %w", shortSHA(f.revision), err)
		s.recordError(ctx, prev, trigger, started, &commit, wrapped)
		return wrapped
	}
	s.lastInvalid = ""
	s.lastUnchangedAt = time.Time{}
	st := domain.GitOpsSyncStatus{
		Source: s.src.describe(), Branch: s.cfg.Branch, Path: s.cfg.Path, State: "ok",
		LastSyncAt: &finished, LastSuccessAt: &finished,
		AppliedCommit: &commit, LatestCommit: &commit,
		Issues: issues, ObjectCounts: objectCounts(res.Snapshot),
	}
	if err := gs.SaveStatus(ctx, st); err != nil {
		s.log.Error("save gitops status failed", "error", err)
	}
	s.syncTotal.WithLabelValues("applied").Inc()
	s.lastSuccess.Set(float64(finished.Unix()))
	s.log.Info("gitops snapshot applied", "commit", shortSHA(f.revision), "trigger", trigger,
		"added", len(changes.Added), "changed", len(changes.Changed), "removed", len(changes.Removed))

	if s.deps.Audit != nil {
		s.deps.Audit.Record(ctx, audit.System(domain.ActorGitOps, "gitops.apply", "gitops", f.revision, map[string]any{
			"commit": f.revision, "trigger": trigger, "message": commit.Message,
			"added": changes.Added, "changed": changes.Changed, "removed": changes.Removed,
		}))
	}
	// Non-leader replicas reload their snapshot on this event; publish it
	// BEFORE the (long) reassignment so that they do not assign agents against
	// the previous commit meanwhile.
	s.publish(ctx, f.revision, "applied")
	rerr := s.reconcile(ctx, f.revision, res.Snapshot, changes)
	s.publish(ctx, f.revision, "applied")
	return rerr
}

// reconciledKey stores the revision up to which OnApplied (assignment and
// rollout planning) completed. A revision that is applied but not reconciled
// (OnApplied failed, or the leader crashed in between) is reconciled again by
// the next sync cycle of whichever replica leads.
const reconciledKey = "gitops/reconciled-revision"

func (s *Syncer) reconciledRevision(ctx context.Context) string {
	b, err := s.deps.Store.KV().Get(ctx, reconciledKey)
	if err != nil {
		return ""
	}
	return string(b)
}

// reconcile runs OnApplied and records its success.
func (s *Syncer) reconcile(ctx context.Context, revision string, snap *domain.GitOpsSnapshot, changes store.ObjectChanges) error {
	if s.deps.OnApplied == nil {
		return nil
	}
	if err := s.deps.OnApplied(ctx, snap, changes); err != nil {
		s.log.Error("reconciling the applied gitops snapshot failed (retried in the next sync cycle)", "commit", shortSHA(revision), "error", err)
		return fmt.Errorf("reconcile gitops snapshot %s: %w", shortSHA(revision), err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := s.deps.Store.KV().Put(ctx, reconciledKey, []byte(revision)); err != nil {
		s.log.Warn("saving the reconciled gitops revision failed (reconciled again in the next cycle)", "error", err)
	}
	return nil
}

// handleUnchanged updates the status for a revision equal to the applied one.
func (s *Syncer) handleUnchanged(ctx context.Context, f *fetched, prev *domain.GitOpsSyncStatus, trigger string, started time.Time) error {
	gs := s.deps.Store.GitOps()
	commit := f.commit
	now := s.now()
	st := *prev
	st.Source, st.Branch, st.Path = s.src.describe(), s.cfg.Branch, s.cfg.Path
	st.LastSyncAt = &now

	// Coming back from an invalid/failed state: refresh warnings and clear the
	// error, because the issues belonged to the other revision.
	needRefresh := prev.State != "ok" || prev.LatestCommit == nil || prev.LatestCommit.SHA != f.revision
	if needRefresh {
		res := ValidateLimited(f.fsys, f.root, f.revision, s.deps.Renderer, nil, s.limits())
		st.Issues = capIssues(res.Issues)
		if res.Valid() {
			st.ObjectCounts = objectCounts(res.Snapshot)
		}
	}
	st.State = "ok"
	st.LatestCommit = &commit
	if prev.AppliedCommit != nil {
		ac := *prev.AppliedCommit
		st.AppliedCommit = &ac
	}
	st.LastSuccessAt = &now
	if err := gs.SaveStatus(ctx, st); err != nil {
		s.log.Warn("save gitops status failed", "error", err)
	}
	s.lastSuccess.Set(float64(now.Unix()))

	if now.Sub(s.lastUnchangedAt) >= unchangedRecordInterval || needRefresh {
		s.lastUnchangedAt = now
		rec := domain.GitOpsSync{StartedAt: started, FinishedAt: &now, Trigger: trigger, Commit: &commit, Result: "unchanged", Issues: domain.Issues{}}
		if _, err := gs.RecordSync(ctx, rec); err != nil {
			s.log.Warn("record gitops sync failed", "error", err)
		}
		s.syncTotal.WithLabelValues("unchanged").Inc()
	}
	if needRefresh {
		s.publish(ctx, f.revision, "unchanged")
	}
	// The applied revision was not (fully) reconciled, e.g. because the leader
	// crashed during the reassignment: do it now.
	if s.deps.OnApplied != nil && s.reconciledRevision(ctx) != f.revision {
		snap, err := gs.GetSnapshot(ctx)
		if err != nil {
			return fmt.Errorf("load applied gitops snapshot: %w", err)
		}
		s.log.Info("reconciling the applied gitops snapshot again", "commit", shortSHA(f.revision), "trigger", trigger)
		if err := s.reconcile(ctx, f.revision, snap, store.ObjectChanges{}); err != nil {
			return err
		}
		s.publish(ctx, f.revision, "applied")
	}
	return nil
}

func (s *Syncer) handleFetchError(ctx context.Context, prev *domain.GitOpsSyncStatus, trigger string, started time.Time, err error) {
	s.syncTotal.WithLabelValues("error").Inc()
	s.recordError(ctx, prev, trigger, started, nil, err)
}

// recordError stores a failed sync attempt (history throttled to state
// changes and once per hour) and updates the status.
func (s *Syncer) recordError(ctx context.Context, prev *domain.GitOpsSyncStatus, trigger string, started time.Time, commit *domain.GitCommit, err error) {
	if ctx.Err() != nil {
		return
	}
	gs := s.deps.Store.GitOps()
	now := s.now()
	s.consecutiveErrors++
	issue := domain.Issue{Severity: domain.SeverityError, Code: "sync_failed", Message: truncateText(err.Error(), 500)}

	st := *prev
	st.Source, st.Branch, st.Path = s.src.describe(), s.cfg.Branch, s.cfg.Path
	st.State = "error"
	st.LastSyncAt = &now
	st.Issues = domain.Issues{issue}
	if st.ObjectCounts == nil {
		st.ObjectCounts = map[domain.ObjectKind]int{}
	}
	if commit != nil {
		st.LatestCommit = commit
	}
	if serr := gs.SaveStatus(ctx, st); serr != nil {
		s.log.Warn("save gitops status failed", "error", serr)
	}

	if issue.Message != s.lastErrorText || now.Sub(s.lastErrorRecorded) >= time.Hour {
		s.lastErrorText, s.lastErrorRecorded = issue.Message, now
		rec := domain.GitOpsSync{StartedAt: started, FinishedAt: &now, Trigger: trigger, Commit: commit, Result: "error", Issues: domain.Issues{issue}}
		if _, rerr := gs.RecordSync(ctx, rec); rerr != nil {
			s.log.Warn("record gitops sync failed", "error", rerr)
		}
	}
	// Persistent fetch problems (not a single hiccup) are worth a message.
	if s.consecutiveErrors == 5 {
		s.notify(ctx, notify.Notification{
			Event: notify.EventGitOpsSyncFailed, Severity: notify.SeverityWarning,
			Title:   "GitOps repository is not reachable",
			Summary: truncateText(err.Error(), 300),
			Link:    "/gitops", Time: now,
			Fields:   []notify.Field{{Name: "Source", Value: s.src.describe()}, {Name: "Consecutive failures", Value: fmt.Sprint(s.consecutiveErrors)}},
			DedupKey: fmt.Sprintf("gitops.sync_failed/fetch/%d", now.Unix()/3600),
		})
	}
}

func (s *Syncer) notifyInvalid(ctx context.Context, commit domain.GitCommit, issues domain.Issues) {
	var lines []string
	for _, is := range issues {
		if is.Severity != domain.SeverityError {
			continue
		}
		loc := is.File
		if is.Line > 0 {
			loc = fmt.Sprintf("%s:%d", is.File, is.Line)
		}
		lines = append(lines, fmt.Sprintf("%s [%s] %s", loc, is.Code, is.Message))
		if len(lines) == 3 {
			break
		}
	}
	n := countErrors(issues)
	s.notify(ctx, notify.Notification{
		Event: notify.EventGitOpsSyncFailed, Severity: notify.SeverityWarning,
		Title:   fmt.Sprintf("GitOps revision %s is invalid", shortSHA(commit.SHA)),
		Summary: fmt.Sprintf("%d validation error(s); the previous snapshot stays active. %s", n, strings.Join(lines, " | ")),
		Link:    "/gitops", Time: s.now(),
		Fields: []notify.Field{
			{Name: "Commit", Value: shortSHA(commit.SHA)}, {Name: "Author", Value: commit.Author},
			{Name: "Message", Value: commit.Message}, {Name: "Errors", Value: fmt.Sprint(n)},
		},
		DedupKey: "gitops.sync_failed/" + commit.SHA,
	})
}

func (s *Syncer) notify(ctx context.Context, n notify.Notification) {
	if s.deps.Notifier == nil {
		return
	}
	if err := s.deps.Notifier.Notify(ctx, n); err != nil {
		s.log.Warn("notification failed", "event", n.Event, "error", err)
	}
}

func (s *Syncer) publish(ctx context.Context, commit, result string) {
	if s.deps.Bus == nil {
		return
	}
	if err := s.deps.Bus.Publish(ctx, eventbus.GitOpsSynced, eventbus.GitOpsSyncedPayload{Commit: commit, Result: result}); err != nil {
		s.log.Warn("publish gitops.synced failed", "error", err)
	}
}

func objectCounts(s *domain.GitOpsSnapshot) map[domain.ObjectKind]int {
	return map[domain.ObjectKind]int{
		domain.KindFleet:         len(s.Fleets),
		domain.KindBlueprint:     len(s.Blueprints),
		domain.KindRolloutPolicy: len(s.RolloutPolicies),
		domain.KindExecutor:      len(s.Executors),
		domain.KindInventory:     len(s.Inventories),
	}
}

func countErrors(is domain.Issues) int {
	n := 0
	for _, i := range is {
		if i.Severity == domain.SeverityError {
			n++
		}
	}
	return n
}

// capIssues bounds the stored issue list (errors first).
func capIssues(is domain.Issues) domain.Issues {
	if len(is) <= maxStoredIssues {
		return append(domain.Issues{}, is...)
	}
	out := make(domain.Issues, 0, maxStoredIssues)
	for _, i := range is {
		if i.Severity == domain.SeverityError && len(out) < maxStoredIssues {
			out = append(out, i)
		}
	}
	for _, i := range is {
		if i.Severity != domain.SeverityError && len(out) < maxStoredIssues {
			out = append(out, i)
		}
	}
	return out
}

func shortSHA(s string) string {
	s = strings.TrimPrefix(s, "dir:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

var _ = store.ObjectChanges{}
