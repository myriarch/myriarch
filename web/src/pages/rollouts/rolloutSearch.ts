// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { ROLLOUT_KINDS, ROLLOUT_STATUSES } from '@/api/enums';
import type { RolloutKind, RolloutStatus } from '@/api/types';

export interface State {
  fleet?: string;
  kind?: RolloutKind;
  status: RolloutStatus[];
  active: boolean;
  limit: number;
  offset: number;
}

export const SIZES = [25, 50, 100] as const;

export function parseRolloutSearch(sp: URLSearchParams): State {
  const kind = sp.get('kind');
  const limit = Number(sp.get('limit'));
  const offset = Number(sp.get('offset'));
  return {
    fleet: sp.get('fleet') || undefined,
    kind: (ROLLOUT_KINDS as readonly string[]).includes(kind ?? '')
      ? (kind as RolloutKind)
      : undefined,
    status: sp
      .getAll('status')
      .filter((s): s is RolloutStatus => (ROLLOUT_STATUSES as readonly string[]).includes(s)),
    active: sp.get('active') === 'true',
    limit: (SIZES as readonly number[]).includes(limit) ? limit : 25,
    offset: Number.isInteger(offset) && offset > 0 ? offset : 0,
  };
}

export function toParams(s: State): URLSearchParams {
  const sp = new URLSearchParams();
  if (s.fleet) sp.set('fleet', s.fleet);
  if (s.kind) sp.set('kind', s.kind);
  s.status.forEach((v) => sp.append('status', v));
  if (s.active) sp.set('active', 'true');
  if (s.limit !== 25) sp.set('limit', String(s.limit));
  if (s.offset > 0) sp.set('offset', String(s.offset));
  return sp;
}
