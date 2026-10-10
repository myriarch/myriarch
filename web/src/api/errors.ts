// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { Issue, Problem } from './types';
import { PROBLEM_CODES } from './enums';

/** Error raised for every non-2xx response (and for network failures, status 0). */
export class ApiError extends Error {
  readonly status: number;
  readonly code: Problem['code'] | 'network' | 'unknown';
  readonly problem?: Problem;
  readonly issues: Issue[];
  readonly requestId?: string;

  constructor(init: {
    status: number;
    code: ApiError['code'];
    message: string;
    problem?: Problem;
    cause?: unknown;
  }) {
    super(init.message, { cause: init.cause });
    this.name = 'ApiError';
    this.status = init.status;
    this.code = init.code;
    this.problem = init.problem;
    this.issues = init.problem?.issues ?? [];
    this.requestId = init.problem?.requestId;
  }

  get isNetwork(): boolean {
    return this.code === 'network';
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

function isProblem(body: unknown): body is Problem {
  return (
    typeof body === 'object' &&
    body !== null &&
    typeof (body as Problem).code === 'string' &&
    typeof (body as Problem).status === 'number'
  );
}

/** Builds an ApiError from a parsed error body (RFC 9457 problem or anything else). */
export function toApiError(status: number, body: unknown): ApiError {
  if (isProblem(body)) {
    const known = (PROBLEM_CODES as readonly string[]).includes(body.code);
    return new ApiError({
      status,
      code: known ? body.code : 'unknown',
      message: body.detail || body.title || `HTTP ${status}`,
      problem: body,
    });
  }
  const text =
    typeof body === 'string' && body.trim() ? body.trim().slice(0, 200) : `HTTP ${status}`;
  return new ApiError({ status, code: 'unknown', message: text });
}

export function networkError(cause: unknown): ApiError {
  return new ApiError({
    status: 0,
    code: 'network',
    message: cause instanceof Error ? cause.message : 'network error',
    cause,
  });
}

/** Whether a failed request is worth retrying automatically. */
export function isRetryable(e: unknown): boolean {
  if (!(e instanceof ApiError)) return false;
  return e.status === 0 || e.status === 429 || e.status >= 500;
}
