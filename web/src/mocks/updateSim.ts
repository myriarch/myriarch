// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * Time-driven simulation of one myriarch-updater update attempt for the mock backend: given the start
 * time, the current time and a script it returns the TargetProgress the real engine would derive
 * (steps, phase, download percent) and the matching target status. Pure, so it is unit tested.
 */
import type {
  TargetProgress,
  TargetStatus,
  UpdatePhase,
  UpdateStep,
  UpdateStepName,
} from '@/api/types';

/** Duration of one step. The demo moves through all steps in about 12 s. */
export const STEP_MS = 2000;

/**
 * success: all steps succeed; rollback: the health check fails and the host rolls back;
 * verify_fail: the package verification fails before anything was changed.
 */
export type UpdateScript = 'success' | 'rollback' | 'verify_fail';

const HOST_STAGES = [
  'downloading',
  'verifying',
  'installing',
  'restarting',
  'health_check',
  'health_wait',
] as const satisfies readonly UpdateStepName[];

const ALL_STEPS: UpdateStepName[] = [
  'offered',
  'downloading',
  'verifying',
  'installing',
  'restarting',
  'health_check',
  'rolling_back',
  'health_wait',
];

export interface SimInput {
  startedAt: number;
  now: number;
  script: UpdateScript;
  from: string;
  to: string;
  attempt?: number;
  stepMs?: number;
}

export interface SimResult {
  progress: TargetProgress;
  targetStatus: TargetStatus;
  final: boolean;
}

const iso = (ms: number) => new Date(ms).toISOString();

const DONE_DETAIL: Partial<Record<UpdateStepName, string>> = {
  offered: 'package offered to the myriarch-updater',
  downloading: 'downloaded from the artifact cache of myriarchd',
  verifying: 'sha256 + sigstore bundle verified',
  installing: 'new version activated',
  restarting: 'supervisor restarted',
  health_check: 'collector healthy',
  rolling_back: 'previous version restored',
  health_wait: 'version reported and stable',
};

export function simulateUpdate(input: SimInput): SimResult {
  const S = input.stepMs ?? STEP_MS;
  const e = Math.max(0, input.now - input.startedAt);
  const idx = Math.floor(e / S);
  const t0 = input.startedAt;
  const steps = new Map<UpdateStepName, UpdateStep>();
  for (const name of ALL_STEPS) steps.set(name, { name, status: 'pending' });
  const set = (name: UpdateStepName, patch: Partial<UpdateStep>) =>
    steps.set(name, { ...steps.get(name)!, ...patch });
  const stageStart = (i: number) => iso(t0 + i * S);

  set('offered', {
    status: 'done',
    startedAt: iso(t0),
    finishedAt: iso(t0),
    detail: DONE_DETAIL.offered,
  });

  let phase: UpdatePhase;
  let error: string | undefined;
  let final = false;
  let targetStatus: TargetStatus = 'in_progress';
  let downloadPercent: number | undefined;

  // Index of the stage that fails (script dependent); stages after it never run.
  const failAt =
    input.script === 'verify_fail' ? 1 : input.script === 'rollback' ? 4 : Number.POSITIVE_INFINITY;

  const doneStage = (i: number) =>
    set(HOST_STAGES[i]!, {
      status: 'done',
      startedAt: stageStart(i),
      finishedAt: stageStart(i + 1),
      detail: DONE_DETAIL[HOST_STAGES[i]!],
    });

  if (idx <= failAt && idx < HOST_STAGES.length) {
    for (let i = 0; i < idx; i++) doneStage(i);
    const name = HOST_STAGES[idx]!;
    set(name, { status: 'running', startedAt: stageStart(idx) });
    phase = name;
    if (name === 'health_wait') targetStatus = 'verifying';
    if (name === 'downloading') downloadPercent = Math.round(((e % S) / S) * 100);
  } else if (idx > failAt) {
    // the stage `failAt` failed at its end; the host reacts afterwards
    for (let i = 0; i < failAt; i++) doneStage(i);
    const failName = HOST_STAGES[failAt]!;
    error =
      input.script === 'verify_fail'
        ? 'package verification failed: sha256 mismatch for the downloaded archive'
        : 'health check failed: the collector did not become healthy after the restart';
    const failedAt = stageStart(failAt + 1);
    if (input.script === 'verify_fail') {
      // nothing was changed: the attempt is over, the following steps are skipped
      set(failName, {
        status: 'failed',
        startedAt: stageStart(failAt),
        finishedAt: failedAt,
        detail: error,
      });
      for (const n of ['installing', 'restarting', 'health_check', 'health_wait'] as const)
        set(n, { status: 'skipped' });
      phase = 'failed';
      final = true;
      targetStatus = 'failed';
    } else {
      set(failName, {
        status: 'failed',
        startedAt: stageStart(failAt),
        finishedAt: failedAt,
        detail: error,
      });
      set('health_wait', { status: 'skipped' });
      if (idx === failAt + 1) {
        set('rolling_back', { status: 'running', startedAt: failedAt });
        phase = 'rolling_back';
      } else {
        set('rolling_back', {
          status: 'done',
          startedAt: failedAt,
          finishedAt: iso(t0 + (failAt + 2) * S),
          detail: DONE_DETAIL.rolling_back,
        });
        phase = 'rolled_back';
        final = true;
        targetStatus = 'failed';
      }
    }
  } else {
    // all stages done: succeeded
    for (let i = 0; i < HOST_STAGES.length; i++) doneStage(i);
    phase = 'succeeded';
    final = true;
    targetStatus = 'succeeded';
  }

  // Without a rollback the step is not part of the list.
  const list = ALL_STEPS.filter(
    (n) => n !== 'rolling_back' || steps.get('rolling_back')!.status !== 'pending',
  ).map((n) => steps.get(n)!);

  return {
    progress: {
      phase,
      source: 'updater',
      attempt: input.attempt ?? 1,
      fromVersion: input.from,
      toVersion: input.to,
      downloadPercent,
      error,
      steps: list,
      updatedAt: iso(Math.min(input.now, t0 + e)),
    },
    targetStatus,
    final,
  };
}

/** Total duration until the script reaches its final state. */
export function scriptDurationMs(script: UpdateScript, stepMs = STEP_MS): number {
  switch (script) {
    case 'success':
      return HOST_STAGES.length * stepMs;
    case 'rollback':
      return 6 * stepMs;
    case 'verify_fail':
      return 2 * stepMs;
  }
}
