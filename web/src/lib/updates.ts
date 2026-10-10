// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * Pure helpers of the UI-triggered version update: presentation of steps/phases, option
 * selection, error code mapping and the rollout annotation formatter. No React in here, so the
 * logic is unit tested without rendering.
 */
import type { TFunction } from 'i18next';
import { isApiError } from '@/api/errors';
import { SELECTION_ISSUE_CODES } from '@/api/enums';
import type {
  FleetVersionOptions,
  ReleaseFleetOption,
  FleetVersionTarget,
  TargetProgress,
  UpdatePhase,
  UpdateStep,
  UpdateStepStatus,
  VersionOption,
} from '@/api/types';
import { compareVersions } from './format';
import type { Tone } from '@/theme/status';

/** Visual state of one step; `tone` drives the color, `icon` keeps color from being the only cue. */
export interface StepView {
  tone: Tone;
  icon: 'pending' | 'running' | 'done' | 'failed' | 'skipped';
  dashed: boolean;
  animated: boolean;
}

export function stepView(status: UpdateStepStatus): StepView {
  switch (status) {
    case 'running':
      return { tone: 'info', icon: 'running', dashed: false, animated: true };
    case 'done':
      return { tone: 'success', icon: 'done', dashed: false, animated: false };
    case 'failed':
      return { tone: 'danger', icon: 'failed', dashed: false, animated: false };
    case 'skipped':
      return { tone: 'neutral', icon: 'skipped', dashed: true, animated: false };
    case 'pending':
      return { tone: 'neutral', icon: 'pending', dashed: false, animated: false };
  }
}

const PHASE_TONES: Record<UpdatePhase, Tone> = {
  offered: 'neutral',
  downloading: 'info',
  verifying: 'info',
  installing: 'info',
  restarting: 'info',
  health_check: 'info',
  rolling_back: 'drift',
  health_wait: 'info',
  succeeded: 'success',
  failed: 'danger',
  rolled_back: 'drift',
  cancelled: 'neutral',
};

export function phaseTone(phase: UpdatePhase): Tone {
  return PHASE_TONES[phase];
}

const FINAL_PHASES: readonly UpdatePhase[] = ['succeeded', 'failed', 'rolled_back', 'cancelled'];
export function isFinalPhase(phase: UpdatePhase): boolean {
  return FINAL_PHASES.includes(phase);
}

/** Percentage of a running download, only while the download step is the running one. */
export function downloadPercentOf(p: TargetProgress | undefined): number | undefined {
  if (!p || p.downloadPercent === undefined) return undefined;
  if (p.phase !== 'downloading') return undefined;
  return Math.max(0, Math.min(100, Math.round(p.downloadPercent)));
}

/** Percent shown on the download step itself (running step only). */
export function stepPercent(p: TargetProgress, step: UpdateStep): number | undefined {
  if (step.name !== 'downloading' || step.status !== 'running') return undefined;
  if (p.downloadPercent === undefined) return undefined;
  return Math.max(0, Math.min(100, Math.round(p.downloadPercent)));
}

/** Duration of a step in ms (running steps count up to `now`); undefined when it has not started. */
export function stepDurationMs(step: UpdateStep, now: number): number | undefined {
  if (!step.startedAt) return undefined;
  const start = Date.parse(step.startedAt);
  if (Number.isNaN(start)) return undefined;
  const end = step.finishedAt ? Date.parse(step.finishedAt) : step.status === 'running' ? now : NaN;
  if (Number.isNaN(end)) return undefined;
  return Math.max(0, end - start);
}

/** "1.2 s", "34 s", "2 min 05 s". */
export function formatDurationMs(ms: number): string {
  if (ms < 10_000) return `${(ms / 1000).toFixed(1)} s`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  return `${m} min ${String(s % 60).padStart(2, '0')} s`;
}

/* ------------------------------------------------------------------ version options */

/** The highest selectable option (the dialog's pre-selection and the button's label). */
export function highestSelectable(options: readonly VersionOption[] | undefined) {
  let best: VersionOption | undefined;
  for (const o of options ?? []) {
    if (!o.selectable) continue;
    if (!best || compareVersions(o.version, best.version) > 0) best = o;
  }
  return best;
}

/**
 * The reference version for the breaking-change link of an option: the version the fleet runs
 * towards today (effective target, else the Git target).
 */
export function compareReference(target: FleetVersionTarget | undefined): string | undefined {
  return target?.version || target?.gitVersion || undefined;
}

/**
 * Whether dropping the UI selection would lower the version of running collectors: the Git target
 * is below the highest version a member runs.
 */
export function clearWouldDowngrade(
  gitVersion: string | undefined,
  runningVersions: readonly string[],
): boolean {
  if (!gitVersion) return false;
  return runningVersions.some((v) => compareVersions(v, gitVersion) > 0);
}

/* ------------------------------------------------------------------ errors */

export type SelectionIssueCode = (typeof SELECTION_ISSUE_CODES)[number];

/** Known `issues[].code` values of a refused selection (the dialog translates them). */
export function selectionIssueCodes(error: unknown): SelectionIssueCode[] {
  if (!isApiError(error)) return [];
  const known = SELECTION_ISSUE_CODES as readonly string[];
  const out: SelectionIssueCode[] = [];
  for (const i of error.issues) {
    if (known.includes(i.code) && !out.includes(i.code as SelectionIssueCode))
      out.push(i.code as SelectionIssueCode);
  }
  return out;
}

/* ------------------------------------------------------------------ rollout annotations */

/** Annotations that get their own labelled row; everything else is technical detail or shown elsewhere. */
const VISIBLE_ANNOTATIONS = ['executorRef', 'distribution', 'updateParams'] as const;
/** Shown in the page header instead (origin badge with who/when/why). */
const HEADER_ANNOTATIONS = ['selection'];

export interface AnnotationView {
  key: string;
  /** One entry per badge: `k=v` for objects, the elements of arrays, the text of scalars. */
  items: string[];
}

function scalarText(v: unknown): string {
  if (v === null) return 'null';
  if (v === undefined) return '';
  if (typeof v === 'string') return v;
  if (typeof v === 'number' || typeof v === 'boolean' || typeof v === 'bigint') return String(v);
  return JSON.stringify(v);
}

/**
 * Formats one annotation value without ever producing "[object Object]": scalars as text, arrays
 * as their (comma separated) elements, objects as `k=v` pairs of one level (nested values as
 * compact JSON).
 */
export function formatAnnotation(_key: string, value: unknown): string[] {
  if (value === null || value === undefined) return [];
  if (Array.isArray(value)) return value.length === 0 ? [] : [value.map(scalarText).join(', ')];
  if (typeof value === 'object') {
    return Object.entries(value as Record<string, unknown>).map(
      ([k, v]) => `${k}=${scalarText(v)}`,
    );
  }
  const text = scalarText(value);
  return text === '' ? [] : [text];
}

/** Splits annotations into labelled rows and the "technical details" JSON blob. */
export function splitAnnotations(annotations: Record<string, unknown> | undefined): {
  visible: AnnotationView[];
  technical: Record<string, unknown>;
} {
  const visible: AnnotationView[] = [];
  const technical: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(annotations ?? {})) {
    if ((VISIBLE_ANNOTATIONS as readonly string[]).includes(key)) {
      const items = formatAnnotation(key, value);
      if (items.length > 0) visible.push({ key, items });
    } else if (!HEADER_ANNOTATIONS.includes(key)) {
      technical[key] = value;
    }
  }
  visible.sort(
    (a, b) =>
      (VISIBLE_ANNOTATIONS as readonly string[]).indexOf(a.key) -
      (VISIBLE_ANNOTATIONS as readonly string[]).indexOf(b.key),
  );
  return { visible, technical };
}

/** Reads the `selection` annotation (origin ui) defensively. */
export function selectionAnnotation(annotations: Record<string, unknown> | undefined): {
  version?: string;
  selectedBy?: string;
  selectedAt?: string;
  reason?: string;
} {
  const raw = annotations?.['selection'];
  if (!raw || typeof raw !== 'object') return {};
  const o = raw as Record<string, unknown>;
  const str = (k: string) =>
    typeof o[k] === 'string' && o[k] !== '' ? (o[k] as string) : undefined;
  return {
    version: str('version'),
    selectedBy: str('selectedBy'),
    selectedAt: str('selectedAt'),
    reason: str('reason'),
  };
}

export function hasProgress(items: readonly { progress?: TargetProgress }[]): boolean {
  return items.some((i) => i.progress !== undefined);
}

export function optionsAllowed(options: FleetVersionOptions | undefined): boolean {
  return !!options && options.allowed && highestSelectable(options.options) !== undefined;
}

/** Accessible name of a step, e.g. "Download: running, 42 %". */
export function stepLabel(t: TFunction, step: UpdateStep, percent?: number): string {
  const base = `${t(`enum.updateStep.${step.name}`)}: ${t(`enum.updateStepStatus.${step.status}`)}`;
  return percent === undefined ? base : `${base}, ${percent} %`;
}

/** Sorted like the server does: selectable fleets first, then by name. */
export function sortFleetOptions(options: readonly ReleaseFleetOption[]): ReleaseFleetOption[] {
  return [...options].sort(
    (a, b) => Number(b.selectable) - Number(a.selectable) || a.fleet.localeCompare(b.fleet),
  );
}
