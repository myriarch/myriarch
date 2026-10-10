// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * Simplified blueprint rendering for mock mode: merges the chain, resolves `[[ .params.x ]]`
 * placeholders and applies `remove` paths. The real renderer lives in myriarchd (internal/blueprint).
 */
import type { Blueprint, Fleet, Issue, ParameterSpec } from '@/api/types';
import { toYaml } from '@/lib/yaml';
import { hashString } from './rng';
import { BLUEPRINTS, FLEETS } from './static';

type Obj = Record<string, unknown>;

const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

export function deepMerge(a: Obj, b: Obj): Obj {
  const out: Obj = { ...a };
  for (const [k, v] of Object.entries(b)) {
    const cur = out[k];
    out[k] = isObj(cur) && isObj(v) ? deepMerge(cur, v) : structuredClone(v);
  }
  return out;
}

function removePath(obj: Obj, path: string): void {
  const parts = path.split('::');
  let cur: unknown = obj;
  for (let i = 0; i < parts.length - 1; i++) {
    if (!isObj(cur)) return;
    cur = cur[parts[i]!];
  }
  if (isObj(cur)) delete cur[parts[parts.length - 1]!];
}

export function blueprintChain(name: string): Blueprint[] {
  const chain: Blueprint[] = [];
  let cur = BLUEPRINTS.find((b) => b.name === name);
  const seen = new Set<string>();
  while (cur && !seen.has(cur.name)) {
    seen.add(cur.name);
    chain.unshift(cur);
    cur = cur.extends ? BLUEPRINTS.find((b) => b.name === cur!.extends) : undefined;
  }
  return chain;
}

export function mergedParameters(name: string): ParameterSpec[] {
  const map = new Map<string, ParameterSpec>();
  for (const bp of blueprintChain(name))
    for (const p of bp.parameters ?? []) map.set(p.name, { ...map.get(p.name), ...p });
  return [...map.values()];
}

function substitute(value: unknown, params: Obj): unknown {
  if (typeof value === 'string') {
    const m = /^\[\[\s*\.params\.(\w+)\s*\]\]$/.exec(value);
    if (m) return params[m[1]!];
    return value.replace(/\[\[\s*\.params\.(\w+)\s*\]\]/g, (_s, k: string) =>
      String(params[k] ?? ''),
    );
  }
  if (Array.isArray(value)) return value.map((v) => substitute(v, params));
  if (isObj(value))
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, substitute(v, params)]));
  return value;
}

function conditionHolds(when: string, params: Obj): boolean {
  const neg = /^\[\[\s*not\s+\.params\.(\w+)\s*\]\]$/.exec(when);
  if (neg) return !params[neg[1]!];
  const pos = /^\[\[\s*\.params\.(\w+)\s*\]\]$/.exec(when);
  if (pos) return !!params[pos[1]!];
  return true;
}

export interface MockRenderInput {
  blueprint: string;
  fleet?: Fleet;
  attributes: Record<string, string>;
  overrides?: Record<string, unknown>;
  validate?: boolean;
}

export interface MockRenderResult {
  layers: string[];
  parameters: Obj;
  parameterSources: Record<string, 'default' | 'fleet' | 'attribute' | 'inventory' | 'override'>;
  body: string;
  hash: string;
  issues: Issue[];
}

export function fakeHash(text: string): string {
  let out = '';
  let seed = hashString(text);
  for (let i = 0; i < 8; i++) {
    seed = hashString(`${seed}:${text.length}:${i}`);
    out += seed.toString(16).padStart(8, '0');
  }
  return out;
}

export function renderMock(input: MockRenderInput): MockRenderResult {
  const chain = blueprintChain(input.blueprint);
  const issues: Issue[] = [];
  if (chain.length === 0) {
    return {
      layers: [],
      parameters: {},
      parameterSources: {},
      body: '',
      hash: '',
      issues: [
        {
          severity: 'error',
          code: 'blueprint_not_found',
          message: `Blueprint "${input.blueprint}" does not exist`,
        },
      ],
    };
  }
  const specs = mergedParameters(input.blueprint);
  const params: Obj = {};
  const sources: MockRenderResult['parameterSources'] = {};
  for (const s of specs) {
    if (s.default !== undefined) {
      params[s.name] = s.default;
      sources[s.name] = 'default';
    }
    if (s.fromAttribute && input.attributes[s.fromAttribute] !== undefined) {
      params[s.name] = input.attributes[s.fromAttribute];
      sources[s.name] = 'attribute';
    }
  }
  for (const [k, v] of Object.entries(input.fleet?.parameters ?? {})) {
    params[k] = v;
    sources[k] = 'fleet';
  }
  for (const [k, v] of Object.entries(input.overrides ?? {})) {
    params[k] = v;
    sources[k] = 'override';
  }
  for (const s of specs) {
    if (s.required && params[s.name] === undefined) {
      issues.push({
        severity: 'error',
        code: 'missing_required_parameter',
        message: `Required parameter "${s.name}" has no value`,
        path: `parameters::${s.name}`,
        object: `Blueprint/${input.blueprint}`,
      });
    }
    const v = params[s.name];
    if (v !== undefined && s.enum && !s.enum.includes(v)) {
      issues.push({
        severity: 'error',
        code: 'parameter_not_in_enum',
        message: `Parameter "${s.name}" must be one of ${s.enum.join(', ')}`,
        path: `parameters::${s.name}`,
      });
    }
    if (
      typeof v === 'number' &&
      ((s.min !== undefined && v < s.min) || (s.max !== undefined && v > s.max))
    ) {
      issues.push({
        severity: 'error',
        code: 'parameter_out_of_range',
        message: `Parameter "${s.name}" must be between ${s.min ?? '-∞'} and ${s.max ?? '∞'}`,
        path: `parameters::${s.name}`,
      });
    }
  }
  for (const k of Object.keys(input.overrides ?? {})) {
    if (!specs.some((s) => s.name === k)) {
      issues.push({
        severity: 'warning',
        code: 'unknown_parameter',
        message: `Parameter "${k}" is not declared by the blueprint chain`,
        path: `parameters::${k}`,
      });
    }
  }

  let config: Obj = {};
  for (const bp of chain) {
    for (const r of bp.remove ?? []) removePath(config, r);
    config = deepMerge(config, bp.config ?? {});
    for (const frag of bp.fragments ?? []) {
      if (frag.when && !conditionHolds(frag.when, params)) continue;
      for (const r of frag.remove ?? []) removePath(config, r);
      config = deepMerge(config, frag.config ?? {});
    }
  }
  const rendered = substitute(config, params);
  if (input.validate !== false && isObj(rendered)) {
    const svc = rendered['service'];
    const recv = isObj(rendered['receivers']) ? rendered['receivers'] : {};
    if (isObj(svc) && isObj(svc['pipelines'])) {
      for (const [name, pl] of Object.entries(svc['pipelines'])) {
        const rs = isObj(pl) && Array.isArray(pl['receivers']) ? (pl['receivers'] as string[]) : [];
        for (const r of rs) {
          if (!(r in recv)) {
            issues.push({
              severity: 'warning',
              code: 'unknown_component',
              message: `Pipeline "${name}" references receiver "${r}" which is not defined in this render`,
              path: `service::pipelines::${name}::receivers`,
            });
          }
        }
      }
    }
  }
  const body = toYaml(rendered);
  return {
    layers: chain.map((b) => b.name),
    parameters: params,
    parameterSources: sources,
    body,
    hash: fakeHash(body),
    issues,
  };
}

export function fleetByName(name: string | undefined): Fleet | undefined {
  return name ? FLEETS.find((f) => f.name === name) : undefined;
}
