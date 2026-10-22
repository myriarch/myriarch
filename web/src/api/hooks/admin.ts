// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { call, client } from '../client';
import type {
  ArtifactStatus,
  CreateAPITokenRequest,
  CreateEnrollmentTokenRequest,
  PrefetchRequest,
  UpdateUserRequest,
} from '../types';
import { qk } from './keys';

export interface ArtifactsQuery {
  distribution?: 'otelcol-contrib' | 'opampsupervisor';
  version?: string;
  status?: ArtifactStatus[];
  limit?: number;
  offset?: number;
}

export function useArtifacts(q: ArtifactsQuery, options?: { refetchInterval?: number }) {
  return useQuery({
    queryKey: qk.artifacts.list(q),
    queryFn: ({ signal }) => call(client.GET('/artifacts', { params: { query: q }, signal })),
    placeholderData: keepPreviousData,
    refetchInterval: options?.refetchInterval,
  });
}

export function usePrefetchArtifacts() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'actions.artifact.prefetchStarted', inlineError: true },
    mutationFn: (body: PrefetchRequest) =>
      call(client.POST('/artifacts/actions/prefetch', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.artifacts.all }),
  });
}

export function useDeleteArtifact() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'actions.artifact.deleted' },
    mutationFn: (id: string) =>
      call(client.DELETE('/artifacts/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.artifacts.all }),
  });
}

export interface AuditQuery {
  actor?: string;
  action?: string;
  targetType?: string;
  targetId?: string;
  from?: string;
  to?: string;
  limit?: number;
  offset?: number;
}

export function useAuditEvents(q: AuditQuery) {
  return useQuery({
    queryKey: qk.audit(q),
    queryFn: ({ signal }) => call(client.GET('/audit', { params: { query: q }, signal })),
    placeholderData: keepPreviousData,
  });
}

export function useNotificationChannels() {
  return useQuery({
    queryKey: qk.notifications,
    queryFn: ({ signal }) => call(client.GET('/notifications/channels', { signal })),
  });
}

export function useTestNotificationChannel() {
  return useMutation({
    meta: { success: 'actions.notification.testSent' },
    mutationFn: (name: string) =>
      call(
        client.POST('/notifications/channels/{name}/actions/test', { params: { path: { name } } }),
      ),
  });
}

export function useApiTokens(enabled = true) {
  return useQuery({
    queryKey: qk.apiTokens,
    enabled,
    queryFn: ({ signal }) => call(client.GET('/tokens', { signal })),
  });
}

export function useCreateApiToken() {
  const qc = useQueryClient();
  return useMutation({
    meta: { inlineError: true },
    mutationFn: (body: CreateAPITokenRequest) => call(client.POST('/tokens', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.apiTokens }),
  });
}

export function useRevokeApiToken() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'actions.token.revoked' },
    mutationFn: (id: string) => call(client.DELETE('/tokens/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.apiTokens }),
  });
}

export function useEnrollmentTokens(enabled = true) {
  return useQuery({
    queryKey: qk.enrollmentTokens,
    enabled,
    queryFn: ({ signal }) => call(client.GET('/enrollment-tokens', { signal })),
  });
}

/** `discard` drops the mutation (and the secret in its result) from the cache as soon as nothing observes it. */
export function useCreateEnrollmentToken({ discard = false }: { discard?: boolean } = {}) {
  const qc = useQueryClient();
  return useMutation({
    meta: { inlineError: true },
    gcTime: discard ? 0 : undefined,
    mutationFn: (body: CreateEnrollmentTokenRequest) =>
      call(client.POST('/enrollment-tokens', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.enrollmentTokens }),
  });
}

export function useRevokeEnrollmentToken() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'actions.token.revoked' },
    mutationFn: (id: string) =>
      call(client.DELETE('/enrollment-tokens/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.enrollmentTokens }),
  });
}

export function useUsers(enabled = true) {
  return useQuery({
    queryKey: qk.users,
    enabled,
    queryFn: ({ signal }) => call(client.GET('/users', { signal })),
  });
}

export function useUpdateUser() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'actions.user.updated' },
    mutationFn: (v: { id: string; body: UpdateUserRequest }) =>
      call(client.PATCH('/users/{id}', { params: { path: { id: v.id } }, body: v.body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.users }),
  });
}
