// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { notifySuccess } from '@/lib/notify';
import { call, client } from '../client';
import type { Rollout, RolloutKind, RolloutStatus, TargetStatus } from '../types';
import { qk } from './keys';

export interface RolloutListQuery {
  fleet?: string;
  kind?: RolloutKind;
  status?: RolloutStatus[];
  active?: boolean;
  limit?: number;
  offset?: number;
}

export function useRollouts(query: RolloutListQuery, options?: { refetchInterval?: number }) {
  return useQuery({
    queryKey: qk.rollouts.list(query),
    queryFn: ({ signal }) => call(client.GET('/rollouts', { params: { query }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: options?.refetchInterval,
  });
}

/** A fixed interval, or a function of the last loaded rollout (e.g. poll only while it is active). */
export type RolloutInterval = number | false | ((rollout: Rollout | undefined) => number | false);

export function useRollout(
  id: string | undefined,
  options?: { refetchInterval?: RolloutInterval },
) {
  const interval = options?.refetchInterval;
  return useQuery<Rollout>({
    queryKey: qk.rollouts.detail(id ?? ''),
    enabled: !!id,
    refetchInterval:
      typeof interval === 'function' ? (query) => interval(query.state.data) : interval,
    queryFn: ({ signal }) =>
      call(client.GET('/rollouts/{id}', { params: { path: { id: id! } }, signal })),
  });
}

export interface TargetsQuery {
  wave?: number;
  status?: TargetStatus[];
  /** Only the target of this agent (at most one item). */
  agent?: string;
  /** `activity` lists targets with running work first; the API default is `wave`. */
  sort?: 'wave' | 'activity';
  limit?: number;
  offset?: number;
}

export function useRolloutTargets(
  id: string | undefined,
  query: TargetsQuery,
  options?: { refetchInterval?: number | false; enabled?: boolean },
) {
  return useQuery({
    queryKey: qk.rollouts.targets(id ?? '', query),
    enabled: !!id && options?.enabled !== false,
    refetchInterval: options?.refetchInterval,
    queryFn: ({ signal }) =>
      call(client.GET('/rollouts/{id}/targets', { params: { path: { id: id! }, query }, signal })),
    placeholderData: keepPreviousData,
  });
}

export type RolloutAction = 'pause' | 'resume' | 'abort' | 'rollback' | 'retry-failed';

const successKey: Record<RolloutAction | 'approve', string> = {
  pause: 'actions.rollout.paused',
  resume: 'actions.rollout.resumed',
  abort: 'actions.rollout.aborted',
  rollback: 'actions.rollout.rollbackStarted',
  'retry-failed': 'actions.rollout.retryStarted',
  approve: 'actions.rollout.approved',
};

/** One mutation for all reason-carrying rollout actions; the result patches the caches. */
export function useRolloutAction(id: string) {
  const qc = useQueryClient();
  return useMutation({
    meta: { inlineError: true },
    mutationFn: async (v: { action: RolloutAction; reason?: string }) => {
      const body = { reason: v.reason || undefined };
      const params = { path: { id } };
      switch (v.action) {
        case 'pause':
          return call(client.POST('/rollouts/{id}/actions/pause', { params, body }));
        case 'resume':
          return call(client.POST('/rollouts/{id}/actions/resume', { params, body }));
        case 'abort':
          return call(client.POST('/rollouts/{id}/actions/abort', { params, body }));
        case 'rollback':
          return call(client.POST('/rollouts/{id}/actions/rollback', { params, body }));
        case 'retry-failed':
          return call(client.POST('/rollouts/{id}/actions/retry-failed', { params, body }));
      }
    },
    onSuccess: (rollout, v) => {
      qc.setQueryData(qk.rollouts.detail(id), rollout);
      void qc.invalidateQueries({ queryKey: qk.rollouts.lists });
      void qc.invalidateQueries({ queryKey: qk.rollouts.targetsOf(id) });
      void qc.invalidateQueries({ queryKey: qk.stats.overview });
      notifySuccess(successKey[v.action]);
    },
  });
}

export function useApproveWave(id: string) {
  const qc = useQueryClient();
  return useMutation({
    meta: { inlineError: true },
    mutationFn: (v: { wave: number; reason?: string }) =>
      call(
        client.POST('/rollouts/{id}/actions/approve', {
          params: { path: { id } },
          body: { wave: v.wave, reason: v.reason || undefined },
        }),
      ),
    onSuccess: (rollout) => {
      qc.setQueryData(qk.rollouts.detail(id), rollout);
      void qc.invalidateQueries({ queryKey: qk.rollouts.lists });
      void qc.invalidateQueries({ queryKey: qk.stats.overview });
      notifySuccess(successKey.approve);
    },
  });
}
