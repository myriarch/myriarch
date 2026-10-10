// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { notifications } from '@mantine/notifications';
import i18n from '@/i18n';
import { formatApiError } from './problem';

export function notifySuccess(key: string, values?: Record<string, unknown>): void {
  notifications.show({
    color: 'teal',
    title: i18n.t('common.done'),
    message: i18n.t(key, values),
    autoClose: 4000,
  });
}

export function notifyError(error: unknown): void {
  const f = formatApiError(i18n.t.bind(i18n), error);
  notifications.show({
    color: 'red',
    title: f.title,
    message: f.detail ?? f.title,
    autoClose: 8000,
  });
}

export function notifyInfo(title: string, message: string, onClick?: () => void): void {
  notifications.show({
    color: 'brand',
    title,
    message,
    autoClose: 10000,
    onClick,
    style: onClick ? { cursor: 'pointer' } : undefined,
  });
}
