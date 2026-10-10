// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Text, Tooltip } from '@mantine/core';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useLang } from '@/i18n/useLang';
import { formatDateTime, formatRelative, formatUtc } from '@/lib/format';

/** Shared minute ticker so that hundreds of rows re-render at most once per 30 s. */
const listeners = new Set<() => void>();
let ticker: ReturnType<typeof setInterval> | null = null;
function subscribe(fn: () => void) {
  listeners.add(fn);
  if (!ticker) ticker = setInterval(() => listeners.forEach((l) => l()), 30_000);
  return () => {
    listeners.delete(fn);
    if (listeners.size === 0 && ticker) {
      clearInterval(ticker);
      ticker = null;
    }
  };
}

function useTick(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => subscribe(() => setNow(Date.now())), []);
  return now;
}

/** Relative time ("vor 5 Minuten") with a tooltip showing local time and UTC. */
export function TimeAgo({
  value,
  fallback = '–',
}: {
  value: string | undefined | null;
  fallback?: string;
}) {
  const lang = useLang();
  const { t } = useTranslation();
  const now = useTick();
  if (!value)
    return (
      <Text span inherit c="dimmed">
        {fallback}
      </Text>
    );
  return (
    <Tooltip
      label={
        <>
          <div>{formatDateTime(lang, value, { seconds: true })}</div>
          <div style={{ opacity: 0.8 }}>
            {t('common.utc')}: {formatUtc(value)}
          </div>
        </>
      }
    >
      <Text span inherit className="myr-tabular" style={{ whiteSpace: 'nowrap' }}>
        {formatRelative(lang, value, now)}
      </Text>
    </Tooltip>
  );
}

/** Absolute local date/time with the UTC time in the tooltip. */
export function DateTime({
  value,
  seconds,
  fallback = '–',
}: {
  value: string | undefined | null;
  seconds?: boolean;
  fallback?: string;
}) {
  const lang = useLang();
  const { t } = useTranslation();
  if (!value)
    return (
      <Text span inherit c="dimmed">
        {fallback}
      </Text>
    );
  return (
    <Tooltip label={`${t('common.utc')}: ${formatUtc(value)}`}>
      <Text span inherit className="myr-tabular" style={{ whiteSpace: 'nowrap' }}>
        {formatDateTime(lang, value, { seconds })}
      </Text>
    </Tooltip>
  );
}
