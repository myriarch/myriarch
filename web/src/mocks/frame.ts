// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * The Git frame (spec.update.selection) as the mock backend evaluates it. The real server uses
 * Masterminds semver constraints; the mock supports comma separated comparators (AND), which is
 * what the fixtures and the documentation use.
 */
import type { VersionSelectionSpec } from '@/api/types';
import { compareVersions } from '@/lib/format';

const COMPARATOR = /^\s*(>=|<=|!=|>|<|=)?\s*v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)\s*$/;

/** True when `version` satisfies every comparator of `range` (e.g. ">=0.161.0, <=0.162.0"). */
export function matchesRange(range: string, version: string): boolean {
  for (const part of range.split(',')) {
    const m = COMPARATOR.exec(part);
    if (!m) return false;
    const op = m[1] ?? '=';
    const c = compareVersions(version, m[2]!);
    const ok =
      op === '>='
        ? c >= 0
        : op === '<='
          ? c <= 0
          : op === '>'
            ? c > 0
            : op === '<'
              ? c < 0
              : op === '!='
                ? c !== 0
                : c === 0;
    if (!ok) return false;
  }
  return true;
}

/** Whether the frame lets an operator select `version` (ignores `enabled`). */
export function inFrame(frame: VersionSelectionSpec | undefined, version: string): boolean {
  if (!frame) return false;
  if (frame.versions && frame.versions.length > 0) return frame.versions.includes(version);
  if (frame.versionRange) return matchesRange(frame.versionRange, version);
  return false;
}

/** Human readable frame, e.g. for the `stateReason` of an invalidated selection. */
export function describeFrame(frame: VersionSelectionSpec): string {
  return frame.versionRange ?? (frame.versions ?? []).join(', ');
}
