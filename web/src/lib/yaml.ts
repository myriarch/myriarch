// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * Minimal YAML emitter for JSON-like values (maps, lists, scalars). Used to display blueprint
 * config fragments that the API delivers as JSON objects. Strings that could be misread as other
 * scalar types are double-quoted (JSON strings are valid YAML).
 */
const PLAIN = /^[A-Za-z_./][A-Za-z0-9_./-]*$/;
const RESERVED = /^(true|false|null|yes|no|on|off|y|n|~)$/i;

function scalar(v: string | number | boolean | null | undefined): string {
  if (v === null || v === undefined) return 'null';
  if (typeof v === 'number') return Number.isFinite(v) ? String(v) : JSON.stringify(String(v));
  if (typeof v === 'boolean') return String(v);
  if (v === '') return '""';
  if (PLAIN.test(v) && !RESERVED.test(v) && !/^[0-9.]+$/.test(v)) return v;
  return JSON.stringify(v);
}

function key(k: string): string {
  return k !== '' && /^[A-Za-z0-9_./@$-]+(:[A-Za-z0-9_./@$-]+)*$/.test(k) && !RESERVED.test(k)
    ? k
    : JSON.stringify(k);
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function emit(value: unknown, indent: number, out: string[]): void {
  const pad = ' '.repeat(indent);
  if (Array.isArray(value)) {
    for (const item of value) {
      if (isPlainObject(item) && Object.keys(item).length > 0) {
        const sub: string[] = [];
        emit(item, indent + 2, sub);
        out.push(`${pad}- ${sub[0]!.slice(indent + 2)}`);
        out.push(...sub.slice(1));
      } else if (Array.isArray(item) && item.length > 0) {
        const sub: string[] = [];
        emit(item, indent + 2, sub);
        out.push(`${pad}- ${sub[0]!.slice(indent + 2)}`);
        out.push(...sub.slice(1));
      } else {
        out.push(`${pad}- ${inline(item)}`);
      }
    }
    return;
  }
  if (isPlainObject(value)) {
    for (const [k, v] of Object.entries(value)) {
      if (isPlainObject(v) && Object.keys(v).length > 0) {
        out.push(`${pad}${key(k)}:`);
        emit(v, indent + 2, out);
      } else if (Array.isArray(v) && v.length > 0) {
        out.push(`${pad}${key(k)}:`);
        emit(v, indent + 2, out);
      } else {
        out.push(`${pad}${key(k)}: ${inline(v)}`);
      }
    }
    return;
  }
  out.push(`${pad}${inline(value)}`);
}

function inline(v: unknown): string {
  if (Array.isArray(v)) return v.length === 0 ? '[]' : `[${v.map(inline).join(', ')}]`;
  if (isPlainObject(v)) return Object.keys(v).length === 0 ? '{}' : JSON.stringify(v);
  return scalar(v as string | number | boolean | null | undefined);
}

export function toYaml(value: unknown): string {
  const out: string[] = [];
  emit(value, 0, out);
  return out.join('\n') + '\n';
}

/** Parses a user-typed parameter override: JSON when valid (numbers, booleans, lists), else the raw string. */
export function parseLooseValue(text: string): unknown {
  const trimmed = text.trim();
  if (trimmed === '') return '';
  try {
    return JSON.parse(trimmed);
  } catch {
    return text;
  }
}
