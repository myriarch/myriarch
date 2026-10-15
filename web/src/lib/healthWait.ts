// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * The health wait of a rollout target as the web UI shows it. No React in here, so the rule is unit
 * tested without rendering.
 *
 * The API has no field for the end of a health wait. The UI computes it with the rule of the rollout
 * engine (internal/rollout/tick.go, evalVersion and evalConfig; docs/concepts/rollouts.md, "Health
 * wait"): a target in `verifying` succeeds once `now - verifiedAt >= healthWait`, where `healthWait`
 * is the one of the policy snapshot of its rollout. The planner applies the policy defaults before it
 * stores the snapshot (domain.RolloutPolicySpec.WithDefaults: no wait, or 0, means 60 seconds), so the
 * end is `verifiedAt + healthWait`. Both values are in the API: `verifiedAt` in the targets of
 * `GET /rollouts/{id}/targets` and `policy.healthWait` in `GET /rollouts/{id}`.
 */
import type { Rollout, RolloutStatus, RolloutTarget } from '@/api/types';

/** domain.DefaultHealthWait: the wait of a policy that sets none. */
export const DEFAULT_HEALTH_WAIT_MS = 60_000;

const UNIT_MS: Record<string, number> = {
  ns: 1e-6,
  us: 1e-3,
  µs: 1e-3, // U+00B5
  μs: 1e-3, // U+03BC
  ms: 1,
  s: 1000,
  m: 60_000,
  h: 3_600_000,
};

const DURATION = /^(?:\d+(?:\.\d+)?(?:ns|us|µs|μs|ms|s|m|h))+$/;
const DURATION_PART = /(\d+(?:\.\d+)?)(ns|us|µs|μs|ms|s|m|h)/g;

/**
 * Parses a Go duration ("5m0s", "1h30m", "1.5s", "250ms", "0") into milliseconds. The server writes
 * durations with `time.Duration.String`, and the GitOps files use the same syntax. undefined for
 * anything else.
 */
export function parseGoDuration(text: string): number | undefined {
  const s = text.trim();
  if (s === '0') return 0;
  if (!DURATION.test(s)) return undefined;
  let total = 0;
  for (const m of s.matchAll(DURATION_PART)) total += Number(m[1]) * UNIT_MS[m[2]!]!;
  return Math.round(total);
}

/**
 * The health wait of a policy snapshot in ms. A snapshot without `healthWait` has the default
 * (the field is left out when it is 0). undefined if the value is not a duration: the UI then shows
 * no countdown instead of a wrong one.
 */
export function healthWaitMs(healthWait: string | undefined): number | undefined {
  if (healthWait === undefined || healthWait.trim() === '') return DEFAULT_HEALTH_WAIT_MS;
  return parseGoDuration(healthWait);
}

/** When the health wait that began at `verifiedAt` ends (ms since the epoch); undefined without a valid start. */
export function healthWaitEnd(verifiedAt: string | undefined, waitMs: number): number | undefined {
  if (!verifiedAt) return undefined;
  const start = Date.parse(verifiedAt);
  return Number.isNaN(start) ? undefined : start + waitMs;
}

/**
 * The time left until `endsAt`, in ms, between 0 and the wait itself. The cap keeps a browser clock
 * that runs behind the one of the server from showing more than the policy says.
 */
export function remainingMs(endsAt: number, now: number, waitMs: number): number {
  return Math.max(0, Math.min(waitMs, endsAt - now));
}

/** The countdown as a timer shows it: "4:07", "0:42", "1:02:03". Whole seconds, rounded up, so 0:01 stays until the end. */
export function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const two = (n: number) => String(n).padStart(2, '0');
  return h > 0 ? `${h}:${two(m)}:${two(s)}` : `${m}:${two(s)}`;
}

/** A wait the way a GitOps file writes it: "5m", "30s", "1m30s", "1h". */
export function formatWait(ms: number): string {
  if (ms <= 0) return '0s';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const total = ms / 1000;
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = Math.round((total % 60) * 1000) / 1000;
  return `${h ? `${h}h` : ''}${m ? `${m}m` : ''}${s ? `${s}s` : ''}`;
}

/**
 * The engine evaluates the targets of a rollout in these states and only in these (processRollout and
 * run in internal/rollout/tick.go): a paused rollout and one that waits for an approval still finish
 * their running targets. The targets of a `failed` rollout stay as they are until an operator decides,
 * so no end can be promised for them.
 */
const EVALUATED: readonly RolloutStatus[] = ['running', 'paused', 'waiting_approval'];

/**
 * Whether the engine evaluates the targets of a rollout in this status. The countdown shows an end
 * only then, and the mock engine of `pnpm dev:mock` finishes the targets in the health wait only then,
 * so that the display and the mock keep to the same rule.
 */
export const isEvaluated = (status: RolloutStatus): boolean => EVALUATED.includes(status);

/** What the countdown needs to know about a rollout. */
export interface HealthWait {
  /** Name of the policy of the rollout: the link target of the source line. */
  policyName: string;
  /** The wait of the policy snapshot, in ms. */
  waitMs: number;
  /** The wait as the policy file writes it ("5m"). */
  waitText: string;
  /** Repository path of the policy, empty for the built-in policy. */
  path: string;
  /** Git commit of the policy snapshot, abbreviated. */
  commit: string;
  /** Whether the engine evaluates the targets of the rollout now. */
  evaluated: boolean;
}

/** Abbreviated commit as the other source references of the UI show it. */
const shortCommit = (commit: string) => commit.slice(0, 8);

/** The health wait of a rollout; undefined if its policy snapshot has no usable `healthWait`. */
export function healthWaitOf(
  r: Pick<Rollout, 'status' | 'policyName' | 'policy'>,
): HealthWait | undefined {
  const waitMs = healthWaitMs(r.policy.healthWait);
  if (waitMs === undefined) return undefined;
  return {
    policyName: r.policyName,
    waitMs,
    waitText: formatWait(waitMs),
    path: r.policy.source.path,
    commit: shortCommit(r.policy.source.commit),
    evaluated: isEvaluated(r.status),
  };
}

/** The start of the health wait that ends last: the one of the target that was verified latest. */
export function latestVerifiedAt(
  targets: readonly Pick<RolloutTarget, 'verifiedAt'>[],
): string | undefined {
  let best: string | undefined;
  let bestMs = Number.NEGATIVE_INFINITY;
  for (const t of targets) {
    const ms = t.verifiedAt ? Date.parse(t.verifiedAt) : Number.NaN;
    if (!Number.isNaN(ms) && ms > bestMs) {
      best = t.verifiedAt;
      bestMs = ms;
    }
  }
  return best;
}
