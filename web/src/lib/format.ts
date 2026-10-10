// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * Locale aware formatting helpers built on Intl. All timestamps from the API are UTC; they are
 * displayed in the browser's local zone, and `formatUtc` provides the UTC text for tooltips.
 */
import type { Lang } from '@/i18n/locales';

export type { Lang };

/**
 * First day of the week for the locale as Mantine expects it (0 = Sunday ... 6 = Saturday).
 * Reads `Intl.Locale` (`getWeekInfo()` or the older `weekInfo` property; both use 1 = Monday ...
 * 7 = Sunday) and falls back to Monday where the runtime offers neither.
 */
export function firstDayOfWeek(lang: Lang): 0 | 1 | 2 | 3 | 4 | 5 | 6 {
  type WeekInfo = { firstDay?: number };
  type LocaleWithWeek = Intl.Locale & { getWeekInfo?: () => WeekInfo; weekInfo?: WeekInfo };
  try {
    const locale = new Intl.Locale(lang) as LocaleWithWeek;
    const day = (locale.getWeekInfo?.() ?? locale.weekInfo)?.firstDay;
    if (typeof day === 'number' && Number.isInteger(day) && day >= 1 && day <= 7) {
      return (day % 7) as 0 | 1 | 2 | 3 | 4 | 5 | 6;
    }
  } catch {
    /* Intl.Locale unavailable or the tag is not valid: use the default below */
  }
  return 1;
}

const numberFormats = new Map<string, Intl.NumberFormat>();
function nf(lang: Lang, opts?: Intl.NumberFormatOptions): Intl.NumberFormat {
  const key = lang + JSON.stringify(opts ?? {});
  let f = numberFormats.get(key);
  if (!f) {
    f = new Intl.NumberFormat(lang, opts);
    numberFormats.set(key, f);
  }
  return f;
}

export function formatNumber(
  lang: Lang,
  n: number | undefined | null,
  maxFractionDigits = 0,
): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '–';
  return nf(lang, { maximumFractionDigits: maxFractionDigits }).format(n);
}

/** `value` is a percentage (0..100). */
export function formatPercent(lang: Lang, value: number | undefined | null, digits = 1): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '–';
  return nf(lang, {
    style: 'percent',
    maximumFractionDigits: digits,
    minimumFractionDigits: 0,
  }).format(value / 100);
}

export function formatBytes(lang: Lang, bytes: number | undefined | null): string {
  if (bytes === undefined || bytes === null) return '–';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = bytes;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${nf(lang, { maximumFractionDigits: i === 0 ? 0 : 1 }).format(v)} ${units[i]}`;
}

function parse(value: string | Date | undefined | null): Date | null {
  if (!value) return null;
  const d = value instanceof Date ? value : new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

export function formatDateTime(
  lang: Lang,
  value: string | Date | undefined | null,
  opts: { seconds?: boolean } = {},
): string {
  const d = parse(value);
  if (!d) return '–';
  return new Intl.DateTimeFormat(lang, {
    dateStyle: 'medium',
    timeStyle: opts.seconds ? 'medium' : 'short',
  }).format(d);
}

export function formatDate(lang: Lang, value: string | Date | undefined | null): string {
  const d = parse(value);
  if (!d) return '–';
  return new Intl.DateTimeFormat(lang, { dateStyle: 'medium' }).format(d);
}

/** Explicit UTC text (tooltips), e.g. "2026-10-06 13:02:11 UTC". */
export function formatUtc(value: string | Date | undefined | null): string {
  const d = parse(value);
  if (!d) return '';
  return d
    .toISOString()
    .replace('T', ' ')
    .replace(/\.\d+Z$/, ' UTC');
}

const DIVISIONS: { amount: number; unit: Intl.RelativeTimeFormatUnit }[] = [
  { amount: 60, unit: 'second' },
  { amount: 60, unit: 'minute' },
  { amount: 24, unit: 'hour' },
  { amount: 7, unit: 'day' },
  { amount: 4.345, unit: 'week' },
  { amount: 12, unit: 'month' },
  { amount: Number.POSITIVE_INFINITY, unit: 'year' },
];

export function formatRelative(
  lang: Lang,
  value: string | Date | undefined | null,
  now: number = Date.now(),
): string {
  const d = parse(value);
  if (!d) return '–';
  let delta = (d.getTime() - now) / 1000;
  const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' });
  if (Math.abs(delta) < 5) return rtf.format(0, 'second');
  for (const div of DIVISIONS) {
    if (Math.abs(delta) < div.amount) return rtf.format(Math.round(delta), div.unit);
    delta /= div.amount;
  }
  return '–';
}

/** "1h 20m" style rendering of a Go duration string ("30s", "5m", "2h"). Unknown input is returned as is. */
export function formatDuration(value: string | undefined): string {
  if (!value) return '–';
  return value;
}

/** Compares collector versions such as "0.162.0" numerically (-1, 0, 1). Non-numeric parts sort last. */
export function compareVersions(a: string, b: string): number {
  const pa = a.replace(/^v/, '').split(/[.+-]/);
  const pb = b.replace(/^v/, '').split(/[.+-]/);
  const n = Math.max(pa.length, pb.length);
  for (let i = 0; i < n; i++) {
    const x = pa[i];
    const y = pb[i];
    if (x === y) continue;
    if (x === undefined) return -1;
    if (y === undefined) return 1;
    const nx = Number(x);
    const ny = Number(y);
    if (!Number.isNaN(nx) && !Number.isNaN(ny)) {
      if (nx !== ny) return nx < ny ? -1 : 1;
    } else {
      return x < y ? -1 : 1;
    }
  }
  return 0;
}

export function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}

export function shortHash(hash: string | undefined, n = 8): string {
  if (!hash) return '–';
  return hash.slice(0, n);
}

/** Display text for arbitrary JSON parameter values. */
export function formatValue(v: unknown): string {
  if (v === undefined) return '–';
  if (v === null) return 'null';
  if (typeof v === 'string') return v;
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  return JSON.stringify(v);
}
