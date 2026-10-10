// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { TFunction } from 'i18next';
import { isApiError } from '@/api/errors';
import type { Issue } from '@/api/types';

export interface FormattedError {
  /** Localized, code based headline (errors.<code>). */
  title: string;
  /** Server supplied detail text (English), if any. */
  detail?: string;
  issues: Issue[];
  requestId?: string;
  code: string;
  status: number;
}

/** Maps an error to a localized message. `code` is the stable key into `errors.<code>`. */
export function formatApiError(t: TFunction, error: unknown): FormattedError {
  if (isApiError(error)) {
    const key = `errors.${error.code}`;
    const title = t(key, { defaultValue: t('errors.unknown') });
    const detail =
      error.problem?.detail && error.problem.detail !== title ? error.problem.detail : undefined;
    return {
      title,
      detail,
      issues: error.issues,
      requestId: error.requestId,
      code: error.code,
      status: error.status,
    };
  }
  return {
    title: t('errors.unknown'),
    detail: error instanceof Error ? error.message : undefined,
    issues: [],
    code: 'unknown',
    status: 0,
  };
}
