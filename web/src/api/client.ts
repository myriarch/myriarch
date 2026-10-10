// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import createClient, { type Middleware } from 'openapi-fetch';
import { ApiError, networkError, toApiError } from './errors';
import { getCsrfToken, notifyUnauthorized } from './session';
import type { paths } from './types';

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);
/** Endpoints where a 401 is an expected answer and must not trigger the global logout flow. */
const AUTH_PROBES = ['/auth/me', '/auth/config', '/auth/dev-login', '/auth/bootstrap-login'];

export const csrfMiddleware: Middleware = {
  onRequest({ request }) {
    if (!SAFE_METHODS.has(request.method.toUpperCase())) {
      const token = getCsrfToken();
      if (token) request.headers.set('X-CSRF-Token', token);
    }
    return request;
  },
};

export const unauthorizedMiddleware: Middleware = {
  onResponse({ request, response }) {
    if (response.status === 401) {
      const path = new URL(request.url, window.location.origin).pathname;
      if (!AUTH_PROBES.some((p) => path.endsWith(p))) notifyUnauthorized();
    }
    return response;
  },
};

export function apiBaseUrl(): string {
  return `${window.location.origin}/api/v1`;
}

export function createApiClient(baseUrl: string = apiBaseUrl()) {
  const c = createClient<paths>({
    baseUrl,
    credentials: 'include',
    // Resolve fetch lazily so that test/mock layers that patch globalThis.fetch later still work.
    fetch: (input) => globalThis.fetch(input),
    headers: { Accept: 'application/json, application/problem+json' },
  });
  c.use(csrfMiddleware, unauthorizedMiddleware);
  return c;
}

export const client = createApiClient();

interface Result<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/**
 * Unwraps an openapi-fetch result: returns `data` (or undefined for 204) and throws ApiError for
 * problem responses and network failures. Every hook goes through this function.
 */
export async function call<T>(promise: Promise<Result<T>>): Promise<T> {
  let res: Result<T>;
  try {
    res = await promise;
  } catch (e) {
    if (e instanceof ApiError) throw e;
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw networkError(e);
  }
  if (!res.response.ok) throw toApiError(res.response.status, res.error);
  return res.data as T;
}
