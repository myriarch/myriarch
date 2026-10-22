// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * MSW handlers for every operation of api/openapi.yaml (operationIds in comments). Responses use the
 * generated schema types; permissions, CSRF and problem+json errors mirror the real server so that
 * the UI's error handling is exercised in mock mode, too.
 */
import { delay, http, HttpResponse, type HttpHandler } from 'msw';
import type {
  AgentEvent,
  Artifact,
  ChangelogEntry,
  FleetBehind,
  MatchFleetsResponse,
  SelectFleetVersionRequest,
  Me,
  Permission,
  Problem,
  Role,
  Selector,
} from '@/api/types';
import {
  API_TOKEN_PREFIX,
  ENROLLMENT_TOKEN_PLACEHOLDER,
  ENROLLMENT_TOKEN_PREFIX,
  STORAGE_KEY_PREFIX,
} from '@/brand';
import { compareVersions } from '@/lib/format';
import { compareTargetsByActivity, compareTargetsByWave } from '@/lib/targetOrder';
import { toYaml } from '@/lib/yaml';
import { Rng } from './rng';
import { capabilityNames, configBody, componentSet } from './agents';
import { getDb, permissionsFor, type AgentQueryParams } from './db';
import { blueprintChain, mergedParameters, renderMock } from './render';
import { BLUEPRINTS, EXECUTORS, FLEETS, POLICIES, POLICY_BY_NAME, iso } from './static';

const BASE = '/api/v1';
export const MOCK_CSRF = 'mock-csrf-token';
let latencyMs: [number, number] = [60, 220];

/** Tests set 0 to run without artificial latency. */
export function setMockLatency(min: number, max = min): void {
  latencyMs = [min, max];
}

const lat = new Rng(1);
async function wait(): Promise<void> {
  const [a, b] = latencyMs;
  if (b <= 0) return;
  await delay(a + lat.next() * (b - a));
}

function problem(
  status: number,
  code: Problem['code'],
  title: string,
  detail?: string,
  issues?: Problem['issues'],
) {
  const body: Problem = {
    type: 'about:blank',
    title,
    status,
    detail,
    code,
    issues,
    requestId: `req_${lat.hex(10)}`,
  };
  return HttpResponse.json(body, {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

/** The three platforms of install.platforms, all ready (tests override the handler for the other states). */
function mockInstallBundles() {
  const platform = (os: 'linux' | 'windows', arch: 'amd64' | 'arm64') => ({
    os,
    arch,
    version: '0.2.0',
    filename: `myriarch-updater_0.2.0_${os}_${arch}.${os === 'windows' ? 'zip' : 'tar.gz'}`,
    status: 'ready' as const,
    sha256: `${os === 'windows' ? 'b' : 'a'}${arch === 'arm64' ? '1' : '0'}`.repeat(32),
    size: 25_000_000,
  });
  return {
    source: 'release' as const,
    version: '0.2.0',
    items: [platform('linux', 'amd64'), platform('linux', 'arm64'), platform('windows', 'amd64')],
  };
}

const unauthorized = () => problem(401, 'unauthorized', 'Unauthorized', 'authentication required');
const forbidden = (perm: Permission) =>
  problem(403, 'forbidden', 'Forbidden', `missing permission ${perm}`);
const notFound = (what: string) => problem(404, 'not_found', 'Not Found', `${what} not found`);
const preconditionFailed = (detail: string) =>
  problem(412, 'precondition_failed', 'Precondition Failed', detail);

const USER_IDS: Record<Role, string> = {
  admin: 'u-0001-0000-0000-000000000001',
  operator: 'u-0002-0000-0000-000000000002',
  viewer: 'u-0003-0000-0000-000000000003',
};

export function meFor(role: Role): Me {
  return {
    type: 'user',
    id: USER_IDS[role],
    name: role,
    email: `${role}@example.com`,
    role,
    authMethod: 'local',
    permissions: permissionsFor(role),
    csrfToken: MOCK_CSRF,
  };
}

type Ctx = { request: Request };

/** Wraps a handler with latency, session check, permission check and CSRF check. */
function guarded<P extends Record<string, string | readonly string[] | undefined>>(
  perm: Permission | 'public',
  fn: (c: { request: Request; params: P; url: URL; role: Role }) => Response | Promise<Response>,
) {
  return async ({ request, params }: Ctx & { params: unknown }) => {
    await wait();
    const db = getDb();
    const url = new URL(request.url);
    if (perm !== 'public') {
      const role = db.sessionRole;
      if (!role) return unauthorized();
      if (!permissionsFor(role).includes(perm)) return forbidden(perm);
      if (
        !['GET', 'HEAD', 'OPTIONS'].includes(request.method) &&
        request.headers.get('X-CSRF-Token') !== MOCK_CSRF
      ) {
        return problem(403, 'csrf_failed', 'Forbidden', 'CSRF token missing or invalid');
      }
      return fn({ request, params: params as P, url, role });
    }
    return fn({ request, params: params as P, url, role: db.sessionRole ?? 'viewer' });
  };
}

const num = (v: string | null, d: number) => {
  const n = Number(v);
  return v !== null && Number.isFinite(n) ? n : d;
};
const bool = (v: string | null) => (v === 'true' ? true : v === 'false' ? false : undefined);
const page = <T>(items: T[], url: URL, defLimit = 50) => {
  const limit = Math.min(500, Math.max(1, num(url.searchParams.get('limit'), defLimit)));
  const offset = Math.max(0, num(url.searchParams.get('offset'), 0));
  return { items: items.slice(offset, offset + limit), total: items.length, limit, offset };
};

function agentParams(url: URL): AgentQueryParams {
  const g = (k: string) => url.searchParams.getAll(k);
  return {
    q: url.searchParams.get('q') ?? undefined,
    kind: g('kind'),
    status: g('status'),
    healthy: bool(url.searchParams.get('healthy')),
    fleet: g('fleet'),
    unassigned: bool(url.searchParams.get('unassigned')),
    version: g('version'),
    outdated: bool(url.searchParams.get('outdated')),
    os: g('os'),
    attr: g('attr'),
    configSync: g('configSync'),
  };
}

export function selectorMatches(sel: Selector | undefined, attrs: Record<string, string>): boolean {
  for (const [k, v] of Object.entries(sel?.matchAttributes ?? {})) if (attrs[k] !== v) return false;
  for (const e of sel?.matchExpressions ?? []) {
    const v = attrs[e.key];
    const vals = e.values ?? [];
    switch (e.operator) {
      case 'In':
        if (v === undefined || !vals.includes(v)) return false;
        break;
      case 'NotIn':
        if (v !== undefined && vals.includes(v)) return false;
        break;
      case 'Exists':
        if (v === undefined) return false;
        break;
      case 'DoesNotExist':
        if (v !== undefined) return false;
        break;
      case 'Glob':
        if (
          v === undefined ||
          !vals.some((g) =>
            new RegExp(
              `^${g
                .replace(/[.+^${}()|[\]\\]/g, '\\$&')
                .replace(/\*/g, '.*')
                .replace(/\?/g, '.')}$`,
            ).test(v),
          )
        )
          return false;
        break;
    }
  }
  return true;
}

function inRange(entries: ChangelogEntry[], from: string, to: string): ChangelogEntry[] {
  return entries.filter(
    (e) => compareVersions(e.version, from) > 0 && compareVersions(e.version, to) <= 0,
  );
}

const json = <T>(data: T, init?: ResponseInit) => HttpResponse.json(data as never, init);

export const handlers: HttpHandler[] = [
  /* ------------------------------------------------------------------ auth */
  // getAuthConfig
  http.get(
    `${BASE}/auth/config`,
    guarded('public', () => json({ ...getDb().authConfig, ui: { defaultLanguage: 'en' } })),
  ),
  // startOidcLogin / oidcCallback: not available in mock mode
  http.get(
    `${BASE}/auth/login`,
    guarded('public', () => notFound('OIDC login')),
  ),
  http.get(
    `${BASE}/auth/callback`,
    guarded('public', () => unauthorized()),
  ),
  // devLogin: user name selects the role (admin/operator/viewer), any password
  http.post(`${BASE}/auth/dev-login`, async ({ request }) => {
    await wait();
    const body = (await request.json()) as { username?: string; password?: string };
    const name = (body.username ?? '').trim().toLowerCase();
    if (!name || !body.password)
      return problem(401, 'unauthorized', 'Unauthorized', 'invalid credentials');
    const role: Role = name === 'viewer' ? 'viewer' : name === 'operator' ? 'operator' : 'admin';
    getDb().sessionRole = role;
    getDb().sessionAuthMethod = 'local';
    persistSession(role, 'local');
    return json({ ...meFor(role), name: body.username!.trim() });
  }),
  // bootstrapLogin: user name `admin` with any password, or any token that was not used before
  http.post(`${BASE}/auth/bootstrap-login`, async ({ request }) => {
    await wait();
    const db = getDb();
    const { bootstrapPasswordEnabled, bootstrapTokenEnabled } = db.authConfig;
    if (!bootstrapPasswordEnabled && !bootstrapTokenEnabled)
      return notFound('the bootstrap admin is not enabled');
    const body = (await request.json()) as { username?: string; password?: string; token?: string };
    if (!body.password === !body.token)
      return problem(
        400,
        'bad_request',
        'Bad Request',
        'exactly one of password and token is required',
      );
    const invalid = () =>
      problem(401, 'unauthorized', 'Unauthorized', 'invalid bootstrap credentials');
    if (body.token) {
      if (!bootstrapTokenEnabled || db.usedBootstrapTokens.has(body.token)) return invalid();
      db.usedBootstrapTokens.add(body.token);
    } else if (!bootstrapPasswordEnabled || (body.username ?? '').trim() !== 'admin') {
      return invalid();
    }
    db.sessionRole = 'admin';
    db.sessionAuthMethod = 'bootstrap';
    persistSession('admin', 'bootstrap');
    return json({ ...meFor('admin'), name: 'admin', authMethod: 'bootstrap' });
  }),
  // logout
  http.post(
    `${BASE}/auth/logout`,
    guarded('read', () => {
      getDb().sessionRole = null;
      getDb().sessionAuthMethod = 'local';
      persistSession(null);
      return new HttpResponse(null, { status: 204 });
    }),
  ),
  // getMe
  http.get(`${BASE}/auth/me`, async () => {
    await wait();
    const role = getDb().sessionRole;
    return role ? json({ ...meFor(role), authMethod: getDb().sessionAuthMethod }) : unauthorized();
  }),

  /* ------------------------------------------------------------------ users */
  // listUsers
  http.get(
    `${BASE}/users`,
    guarded('users:manage', () => json({ items: getDb().users })),
  ),
  // updateUser
  http.patch(
    `${BASE}/users/:id`,
    guarded<{ id: string }>('users:manage', async ({ request, params, role }) => {
      const u = getDb().users.find((x) => x.id === params.id);
      if (!u) return notFound('user');
      if (u.provider === 'bootstrap')
        return preconditionFailed(
          'the bootstrap admin is managed by configuration (auth.bootstrapAdmin)',
        );
      const body = (await request.json()) as { role?: Role; disabled?: boolean };
      if (body.role) {
        u.role = body.role;
        u.roleSource = 'manual';
      }
      if (body.disabled !== undefined) u.disabled = body.disabled;
      getDb().recordAudit(role, 'user.update', 'user', u.id, body as Record<string, unknown>);
      return json(u);
    }),
  ),

  /* ------------------------------------------------------------------ tokens */
  // listApiTokens / createApiToken / revokeApiToken
  http.get(
    `${BASE}/tokens`,
    guarded('tokens:manage', () => json({ items: getDb().apiTokens })),
  ),
  http.post(
    `${BASE}/tokens`,
    guarded('tokens:manage', async ({ request, role }) => {
      const body = (await request.json()) as { name?: string; role?: Role; expiresAt?: string };
      if (!body.name?.trim() || !body.role) {
        return problem(422, 'validation_failed', 'Validation failed', 'invalid token request', [
          {
            severity: 'error',
            code: 'required',
            message: 'name and role are required',
            path: 'name',
          },
        ]);
      }
      const db = getDb();
      if (db.apiTokens.some((t) => t.name === body.name && !t.revokedAt))
        return problem(409, 'conflict', 'Conflict', 'a token with this name already exists');
      const secret = `${API_TOKEN_PREFIX}${lat.hex(40)}`;
      const token = {
        id: lat.uuid(),
        name: body.name.trim(),
        displayPrefix: secret.slice(0, 11),
        role: body.role,
        createdBy: role,
        createdAt: iso(0),
        expiresAt: body.expiresAt,
      };
      db.apiTokens.unshift(token);
      db.recordAudit(role, 'token.create', 'api-token', token.id, {
        name: token.name,
        role: token.role,
      });
      return json({ token, secret }, { status: 201 });
    }),
  ),
  http.delete(
    `${BASE}/tokens/:id`,
    guarded<{ id: string }>('tokens:manage', ({ params, role }) => {
      const t = getDb().apiTokens.find((x) => x.id === params.id);
      if (!t) return notFound('token');
      t.revokedAt = iso(0);
      getDb().recordAudit(role, 'token.revoke', 'api-token', t.id);
      return new HttpResponse(null, { status: 204 });
    }),
  ),
  // listEnrollmentTokens / createEnrollmentToken / revokeEnrollmentToken
  http.get(
    `${BASE}/enrollment-tokens`,
    guarded('enrollment:manage', () => json({ items: getDb().enrollmentTokens })),
  ),
  http.post(
    `${BASE}/enrollment-tokens`,
    guarded('enrollment:manage', async ({ request, role }) => {
      const body = (await request.json()) as {
        name?: string;
        labels?: Record<string, string>;
        expiresAt?: string;
      };
      if (!body.name?.trim()) {
        return problem(422, 'validation_failed', 'Validation failed', 'invalid token request', [
          { severity: 'error', code: 'required', message: 'name is required', path: 'name' },
        ]);
      }
      const secret = `${ENROLLMENT_TOKEN_PREFIX}${lat.hex(40)}`;
      const token = {
        id: lat.uuid(),
        name: body.name.trim(),
        displayPrefix: secret.slice(0, 11),
        labels: body.labels,
        createdBy: role,
        createdAt: iso(0),
        expiresAt: body.expiresAt,
        useCount: 0,
      };
      getDb().enrollmentTokens.unshift(token);
      getDb().recordAudit(role, 'enrollment_token.create', 'enrollment-token', token.id, {
        name: token.name,
      });
      return json({ token, secret }, { status: 201 });
    }),
  ),
  http.delete(
    `${BASE}/enrollment-tokens/:id`,
    guarded<{ id: string }>('enrollment:manage', ({ params, role }) => {
      const t = getDb().enrollmentTokens.find((x) => x.id === params.id);
      if (!t) return notFound('token');
      t.revokedAt = iso(0);
      getDb().recordAudit(role, 'enrollment_token.revoke', 'enrollment-token', t.id);
      return new HttpResponse(null, { status: 204 });
    }),
  ),

  /* ------------------------------------------------------------------ install */
  // listInstallBundles
  http.get(
    `${BASE}/install/bundles`,
    guarded('read', () => json(mockInstallBundles())),
  ),
  // renderInstallCommands
  http.post(
    `${BASE}/install/commands`,
    guarded('read', async ({ request }) => {
      const body = (await request.json()) as {
        variant?: string;
        platforms?: string[];
        attributes?: Record<string, string>;
      };
      const issues: NonNullable<Problem['issues']> = [];
      for (const k of ['site.type', 'site.id']) {
        if (!body.attributes?.[k]) {
          issues.push({
            severity: 'error',
            code: 'invalid_value',
            message: `${k} is required`,
            path: `attributes.${k}`,
          });
        }
      }
      if (issues.length) {
        return problem(422, 'validation_failed', 'Validation failed', 'invalid request', issues);
      }
      const variant = body.variant ?? 'token_inline';
      const bundles = mockInstallBundles().items.filter(
        (b) => !body.platforms?.length || body.platforms.includes(`${b.os}/${b.arch}`),
      );
      const commands = bundles.map((bundle) => {
        const windows = bundle.os === 'windows';
        const token =
          variant === 'token_file' ? '(read from the token file)' : ENROLLMENT_TOKEN_PLACEHOLDER;
        return {
          platform: `${bundle.os}/${bundle.arch}`,
          shell: windows ? ('powershell' as const) : ('bash' as const),
          command: `# mock command for ${bundle.filename}\ninstall --server https://fleet.example.com --token ${token} --site-type ${body.attributes?.['site.type']} --site-id ${body.attributes?.['site.id']}\n`,
          bundle,
        };
      });
      return json(
        {
          variant: variant as 'token_inline' | 'token_file' | 'lab',
          server: 'https://fleet.example.com',
          version: '0.2.0',
          insecure: variant === 'lab',
          tokenPlaceholder: variant === 'token_file' ? undefined : ENROLLMENT_TOKEN_PLACEHOLDER,
          commands,
        },
        { headers: { 'Cache-Control': 'no-store' } },
      );
    }),
  ),

  /* ------------------------------------------------------------------ agents */
  // getAgentFacets (registered before /agents/:id)
  http.get(
    `${BASE}/agents/facets`,
    guarded('read', ({ url }) => json(getDb().facets(agentParams(url)))),
  ),
  // listAgents
  http.get(
    `${BASE}/agents`,
    guarded('read', ({ url }) => {
      const sort = url.searchParams.get('sort') ?? 'hostname';
      const limit = Math.min(500, Math.max(1, num(url.searchParams.get('limit'), 50)));
      const offset = Math.max(0, num(url.searchParams.get('offset'), 0));
      const { items, total } = getDb().queryAgents(agentParams(url), sort, limit, offset);
      return json({ items, total, limit, offset });
    }),
  ),
  // getAgent
  http.get(
    `${BASE}/agents/:id`,
    guarded<{ id: string }>('read', ({ params }) => {
      const db = getDb();
      const e = db.entries.get(params.id);
      if (!e) return notFound('agent');
      const a = e.agent;
      const linkedId = a.kind === 'updater' ? a.updater?.agentId : a.updater?.agentId;
      const active = db.rollouts
        .filter(
          (r) =>
            !['completed', 'failed', 'aborted', 'rolled_back', 'superseded'].includes(
              r.rollout.status,
            ),
        )
        .flatMap((r) => r.targets.filter((t) => t.agentId === a.id));
      return json({
        agent: a,
        configSync: e.sync,
        outdated: a.kind === 'collector' && compareVersions(a.version, db.latestVersion()) < 0,
        latestVersion: db.latestVersion(),
        capabilityNames: capabilityNames(a.capabilities),
        activeRolloutTargets: active,
        linkedAgent: linkedId ? db.summary(linkedId) : undefined,
      });
    }),
  ),
  // deleteAgent
  http.delete(
    `${BASE}/agents/:id`,
    guarded<{ id: string }>('agents:delete', ({ params, role }) => {
      const db = getDb();
      const e = db.entries.get(params.id);
      if (!e) return notFound('agent');
      if (e.agent.connection.status === 'online')
        return preconditionFailed('agent is online; only offline agents can be deleted');
      db.removeAgent(params.id);
      db.recordAudit(role, 'agent.delete', 'agent', params.id);
      return new HttpResponse(null, { status: 204 });
    }),
  ),
  // getAgentEffectiveConfig
  http.get(
    `${BASE}/agents/:id/effective-config`,
    guarded<{ id: string }>('read', ({ params }) => {
      const a = getDb().agentOf(params.id);
      if (!a) return notFound('agent');
      if (!a.effectiveConfigHash) return notFound('effective config');
      const { body, hash } = configBody(a, 'effective');
      return json({
        hash,
        contentType: 'text/yaml',
        body,
        updatedAt: a.effectiveConfigUpdatedAt,
        source: 'effective',
      });
    }),
  ),
  // getAgentDesiredConfig
  http.get(
    `${BASE}/agents/:id/desired-config`,
    guarded<{ id: string }>('read', ({ params, url }) => {
      const a = getDb().agentOf(params.id);
      if (!a) return notFound('agent');
      const which = url.searchParams.get('which') === 'assigned' ? 'assigned' : 'desired';
      if (!a.assignment.fleetName) return notFound('assigned config');
      const { body, hash } = configBody(a, which);
      return json({
        hash,
        contentType: 'text/yaml',
        body,
        updatedAt: a.assignment.assignedAt,
        source: which,
      });
    }),
  ),
  // getAgentComponents
  http.get(
    `${BASE}/agents/:id/components`,
    guarded<{ id: string }>('read', ({ params }) => {
      const a = getDb().agentOf(params.id);
      if (!a) return notFound('agent');
      return json(componentSet(a));
    }),
  ),
  // listAgentEvents
  http.get(
    `${BASE}/agents/:id/events`,
    guarded<{ id: string }>('read', ({ params, url }) => {
      if (!getDb().agentOf(params.id)) return notFound('agent');
      const limit = Math.min(500, Math.max(1, num(url.searchParams.get('limit'), 100)));
      const before = num(url.searchParams.get('before'), 0);
      const res: { items: AgentEvent[]; nextBefore: number } = getDb().events(
        params.id,
        before,
        limit,
      );
      return json(res);
    }),
  ),
  // restartAgent
  http.post(
    `${BASE}/agents/:id/actions/restart`,
    guarded<{ id: string }>('agents:operate', ({ params, role }) => {
      const db = getDb();
      const a = db.agentOf(params.id);
      if (!a) return notFound('agent');
      if (!capabilityNames(a.capabilities).includes('AcceptsRestartCommand'))
        return preconditionFailed('agent does not accept restart commands');
      if (a.connection.status !== 'online') return preconditionFailed('agent is offline');
      db.addEvent(a.id, {
        type: 'command',
        severity: 'info',
        message: `Restart command sent by ${role}`,
      });
      db.recordAudit(role, 'agent.restart', 'agent', a.id);
      return json({ accepted: true, message: 'restart command queued' }, { status: 202 });
    }),
  ),
  // resyncAgent
  http.post(
    `${BASE}/agents/:id/actions/resync`,
    guarded<{ id: string }>('agents:operate', ({ params, role }) => {
      const db = getDb();
      const e = db.entries.get(params.id);
      if (!e) return notFound('agent');
      if (e.sync === 'failed') {
        e.sync = 'pending';
        e.agent.assignment.failedConfigHash = undefined;
        e.agent.remoteConfig.status = 'applying';
        e.agent.remoteConfig.errorMessage = undefined;
        const s = db.summary(e.agent.id);
        if (s) db.emit('agent.updated', s);
      }
      db.addEvent(e.agent.id, {
        type: 'command',
        severity: 'info',
        message: `Resync requested by ${role}`,
      });
      db.recordAudit(role, 'agent.resync', 'agent', e.agent.id);
      return json({ accepted: true }, { status: 202 });
    }),
  ),

  /* ------------------------------------------------------------------ saved filters */
  // listSavedFilters / createSavedFilter / deleteSavedFilter
  http.get(
    `${BASE}/saved-filters`,
    guarded('read', ({ url, role }) => {
      const pageName = url.searchParams.get('page') ?? 'agents';
      return json({
        items: getDb().savedFilters.filter(
          (f) => f.page === pageName && (f.userId === USER_IDS[role] || f.shared),
        ),
      });
    }),
  ),
  http.post(
    `${BASE}/saved-filters`,
    guarded('saved_filters:write', async ({ request, role }) => {
      const body = (await request.json()) as {
        name?: string;
        page?: string;
        query?: Record<string, unknown>;
        shared?: boolean;
      };
      if (!body.name?.trim()) {
        return problem(422, 'validation_failed', 'Validation failed', 'name is required', [
          { severity: 'error', code: 'required', message: 'name is required', path: 'name' },
        ]);
      }
      const db = getDb();
      if (
        db.savedFilters.some(
          (f) =>
            f.userId === USER_IDS[role] &&
            f.name === body.name!.trim() &&
            f.page === (body.page ?? 'agents'),
        )
      ) {
        return problem(409, 'conflict', 'Conflict', 'a filter with this name already exists');
      }
      const f = {
        id: lat.uuid(),
        userId: USER_IDS[role],
        name: body.name.trim(),
        page: body.page ?? 'agents',
        query: body.query ?? {},
        shared: !!body.shared,
        createdAt: iso(0),
      };
      db.savedFilters.push(f);
      return json(f, { status: 201 });
    }),
  ),
  http.delete(
    `${BASE}/saved-filters/:id`,
    guarded<{ id: string }>('saved_filters:write', ({ params, role }) => {
      const db = getDb();
      const idx = db.savedFilters.findIndex(
        (f) => f.id === params.id && f.userId === USER_IDS[role],
      );
      if (idx < 0) return notFound('saved filter');
      db.savedFilters.splice(idx, 1);
      return new HttpResponse(null, { status: 204 });
    }),
  ),

  /* ------------------------------------------------------------------ stats, system, audit */
  // getOverviewStats
  http.get(
    `${BASE}/stats/overview`,
    guarded('read', () => json(getDb().overview())),
  ),
  // getSystemInfo
  http.get(
    `${BASE}/system/info`,
    guarded('read', () => json(getDb().system())),
  ),
  // listAuditEvents
  http.get(
    `${BASE}/audit`,
    guarded('audit:read', ({ url }) => {
      const p = url.searchParams;
      const action = p.get('action');
      let items = getDb().audit.filter((e) => {
        if (p.get('actor') && e.actorId !== p.get('actor') && e.actorName !== p.get('actor'))
          return false;
        if (action) {
          if (
            action.endsWith('*') ? !e.action.startsWith(action.slice(0, -1)) : e.action !== action
          )
            return false;
        }
        if (p.get('targetType') && e.targetType !== p.get('targetType')) return false;
        if (p.get('targetId') && e.targetId !== p.get('targetId')) return false;
        if (p.get('from') && e.time < p.get('from')!) return false;
        if (p.get('to') && e.time > p.get('to')!) return false;
        return true;
      });
      items = [...items].sort((a, b) => (a.time < b.time ? 1 : -1));
      return json(page(items, url));
    }),
  ),

  /* ------------------------------------------------------------------ fleets */
  // listFleets
  http.get(
    `${BASE}/fleets`,
    guarded('read', () =>
      json({ items: FLEETS.map((f) => ({ fleet: f, status: getDb().fleetStatus(f.name) })) }),
    ),
  ),
  // matchFleets
  http.post(
    `${BASE}/fleets/match`,
    guarded('read', async ({ request }) => {
      const body = (await request.json()) as {
        agentId?: string;
        attributes?: Record<string, string>;
      };
      const db = getDb();
      let attrs: Record<string, string> = body.attributes ?? {};
      if (body.agentId) {
        const a = db.agentOf(body.agentId);
        if (!a) return notFound('agent');
        attrs = {
          ...a.identifyingAttributes,
          ...a.nonIdentifyingAttributes,
          ...Object.fromEntries(Object.entries(a.labels).map(([k, v]) => [`labels.${k}`, v])),
        };
      }
      const inventoryLabels: Record<string, string> = {};
      if (attrs['site.id'] === '1001' || attrs['site.id'] === '1002')
        inventoryLabels['pilot'] = 'true';
      if (inventoryLabels['pilot']) attrs = { ...attrs, 'labels.pilot': 'true' };
      const candidates = FLEETS.filter((f) => selectorMatches(f.selector, attrs)).sort(
        (a, b) => b.priority - a.priority || a.name.localeCompare(b.name),
      );
      const res: MatchFleetsResponse = {
        fleet: candidates[0]?.name,
        candidates: candidates.map((c) => c.name),
        error:
          candidates.length === 0 ? 'no fleet selector matches the given attributes' : undefined,
        inventoryLabels: Object.keys(inventoryLabels).length ? inventoryLabels : undefined,
      };
      return json(res);
    }),
  ),
  // getFleetVersionOptions
  http.get(
    `${BASE}/fleets/:name/version-options`,
    guarded<{ name: string }>('read', ({ params }) => {
      const o = getDb().versionOptions(params.name);
      return o ? json(o) : notFound('fleet');
    }),
  ),
  // selectFleetVersion
  http.post(
    `${BASE}/fleets/:name/actions/select-version`,
    guarded<{ name: string }>('fleets:set_version', async ({ request, params, role }) => {
      const body = (await request.json().catch(() => null)) as SelectFleetVersionRequest | null;
      if (!body || typeof body.version !== 'string')
        return problem(400, 'bad_request', 'Bad Request', 'version is required');
      const res = getDb().selectVersion(params.name, body, { name: role });
      if (res.ok) return json(res.response);
      const titles = {
        400: 'Bad Request',
        404: 'Not Found',
        409: 'Conflict',
        412: 'Precondition Failed',
        422: 'Unprocessable Entity',
      } as const;
      return problem(res.status, res.code, titles[res.status], res.detail, res.issues);
    }),
  ),
  // clearFleetVersionSelection
  http.post(
    `${BASE}/fleets/:name/actions/clear-version-selection`,
    guarded<{ name: string }>('fleets:set_version', async ({ request, params, role }) => {
      const body = (await request.json().catch(() => ({}))) as { reason?: string };
      const res = getDb().clearSelection(params.name, body.reason, { name: role });
      if (res.ok) return json(res.target);
      return problem(res.status, res.code, 'Unprocessable Entity', res.detail, res.issues);
    }),
  ),
  // getFleet
  http.get(
    `${BASE}/fleets/:name`,
    guarded<{ name: string }>('read', ({ params }) => {
      const fleet = FLEETS.find((f) => f.name === params.name);
      if (!fleet) return notFound('fleet');
      const exec = EXECUTORS.find((e) => e.name === fleet.update.executorRef);
      return json({
        fleet,
        status: getDb().fleetStatus(fleet.name),
        blueprintChain: blueprintChain(fleet.blueprint).map((b) => b.name),
        configPolicy: POLICY_BY_NAME.get(fleet.rollout.configPolicy ?? 'default') ?? POLICIES[0]!,
        versionPolicy: POLICY_BY_NAME.get(fleet.rollout.versionPolicy ?? 'default') ?? POLICIES[0]!,
        executor: exec,
        issues: fleet.name === 'linux-hosts' ? getDb().gitopsStatus.issues : [],
        webUrl: `https://git.example.internal/observability/myriarch-config/-/blob/main/${fleet.source.path}`,
      });
    }),
  ),

  /* ------------------------------------------------------------------ blueprints */
  // listBlueprints
  http.get(
    `${BASE}/blueprints`,
    guarded('read', () =>
      json({
        items: BLUEPRINTS.map((b) => ({
          name: b.name,
          description: b.description,
          extends: b.extends,
          parameterCount: mergedParameters(b.name).length,
          fragmentCount: (b.fragments ?? []).length,
          usedBy: FLEETS.filter((f) =>
            blueprintChain(f.blueprint).some((x) => x.name === b.name),
          ).map((f) => f.name),
          source: b.source,
          issueCount: b.name === 'office-linux' ? 1 : 0,
        })),
      }),
    ),
  ),
  // getBlueprint
  http.get(
    `${BASE}/blueprints/:name`,
    guarded<{ name: string }>('read', ({ params }) => {
      const b = BLUEPRINTS.find((x) => x.name === params.name);
      if (!b) return notFound('blueprint');
      return json({
        blueprint: b,
        chain: blueprintChain(b.name).map((x) => x.name),
        parameters: mergedParameters(b.name),
        usedBy: FLEETS.filter((f) =>
          blueprintChain(f.blueprint).some((x) => x.name === b.name),
        ).map((f) => f.name),
        issues: b.name === 'office-linux' ? getDb().gitopsStatus.issues : [],
        webUrl: `https://git.example.internal/observability/myriarch-config/-/blob/main/${b.source.path}`,
      });
    }),
  ),
  // renderBlueprint
  http.post(
    `${BASE}/blueprints/:name/render`,
    guarded<{ name: string }>('read', async ({ request, params }) => {
      if (!BLUEPRINTS.some((x) => x.name === params.name)) return notFound('blueprint');
      const body = (await request.json()) as {
        agentId?: string;
        attributes?: Record<string, string>;
        fleet?: string;
        parameters?: Record<string, unknown>;
        validate?: boolean;
      };
      const db = getDb();
      let attributes = body.attributes ?? {};
      if (body.agentId) {
        const a = db.agentOf(body.agentId);
        if (!a) return notFound('agent');
        attributes = { ...a.identifyingAttributes, ...a.nonIdentifyingAttributes };
      }
      const fleet = FLEETS.find(
        (f) =>
          f.name ===
          (body.fleet ??
            (body.agentId ? db.agentOf(body.agentId)?.assignment.fleetName : undefined)),
      );
      const r = renderMock({
        blueprint: params.name,
        fleet,
        attributes,
        overrides: body.parameters,
        validate: body.validate,
      });
      return json({
        blueprint: params.name,
        fleet: fleet?.name,
        layers: r.layers,
        parameters: r.parameters,
        parameterSources: r.parameterSources,
        hash: r.issues.some((i) => i.severity === 'error') ? undefined : r.hash,
        body: r.issues.some((i) => i.severity === 'error') ? '' : r.body,
        issues: r.issues,
      });
    }),
  ),

  /* ------------------------------------------------------------------ policies, executors */
  // listRolloutPolicies / getRolloutPolicy / listExecutors
  http.get(
    `${BASE}/rollout-policies`,
    guarded('read', () => json({ items: POLICIES })),
  ),
  http.get(
    `${BASE}/rollout-policies/:name`,
    guarded<{ name: string }>('read', ({ params }) => {
      const p = POLICY_BY_NAME.get(params.name);
      return p ? json(p) : notFound('rollout policy');
    }),
  ),
  http.get(
    `${BASE}/executors`,
    guarded('read', () => json({ items: EXECUTORS })),
  ),

  /* ------------------------------------------------------------------ rollouts */
  // listRollouts
  http.get(
    `${BASE}/rollouts`,
    guarded('read', ({ url }) => {
      const p = url.searchParams;
      const statuses = p.getAll('status');
      getDb().syncSimulations();
      let items = getDb()
        .rollouts.map((r) => r.rollout)
        .filter((r) => {
          if (p.get('fleet') && r.fleetName !== p.get('fleet')) return false;
          if (p.get('kind') && r.kind !== p.get('kind')) return false;
          if (statuses.length && !statuses.includes(r.status)) return false;
          if (
            p.get('active') === 'true' &&
            ['completed', 'failed', 'aborted', 'rolled_back', 'superseded'].includes(r.status)
          )
            return false;
          return true;
        });
      items = [...items].sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1));
      const pg = page(items, url);
      return json({ ...pg, items: pg.items.map((r) => getDb().rolloutSummary(r)) });
    }),
  ),
  // getRollout
  http.get(
    `${BASE}/rollouts/:id`,
    guarded<{ id: string }>('read', ({ params }) => {
      getDb().syncSimulations();
      const r = getDb().findRollout(params.id);
      return r ? json(r.rollout) : notFound('rollout');
    }),
  ),
  // listRolloutTargets
  http.get(
    `${BASE}/rollouts/:id/targets`,
    guarded<{ id: string }>('read', ({ params, url }) => {
      const r = getDb().findRollout(params.id);
      if (!r) return notFound('rollout');
      const wave = url.searchParams.get('wave');
      const statuses = url.searchParams.getAll('status');
      const agent = url.searchParams.get('agent');
      const sort = url.searchParams.get('sort') || 'wave';
      if (sort !== 'wave' && sort !== 'activity')
        return problem(
          400,
          'bad_request',
          'Bad Request',
          `unsupported sort "${sort}" (allowed: wave, activity)`,
        );
      const items = getDb()
        .targetsOf(r)
        .filter(
          (t) =>
            (wave === null || t.wave === Number(wave)) &&
            (statuses.length === 0 || statuses.includes(t.status)) &&
            (agent === null || t.agentId === agent),
        )
        .sort(sort === 'activity' ? compareTargetsByActivity : compareTargetsByWave);
      return json(page(items, url));
    }),
  ),
  // pauseRollout / resumeRollout / abortRollout / rollbackRollout / retryFailedRolloutTargets / approveRolloutWave
  ...(['pause', 'resume', 'abort', 'rollback', 'retry-failed'] as const).map((action) =>
    http.post(
      `${BASE}/rollouts/:id/actions/${action}`,
      guarded<{ id: string }>('rollouts:operate', async ({ request, params, role }) => {
        const db = getDb();
        const mr = db.findRollout(params.id);
        if (!mr) return notFound('rollout');
        const body = (await request.json().catch(() => ({}))) as { reason?: string };
        const r = mr.rollout;
        const st = r.status;
        const reason = body.reason ? `${role}: ${body.reason}` : undefined;
        switch (action) {
          case 'pause':
            if (st !== 'running')
              return preconditionFailed(`cannot pause a rollout in state ${st}`);
            db.setRolloutStatus(mr, 'paused', reason ?? `Paused by ${role}`);
            break;
          case 'resume':
            if (!['paused', 'pending'].includes(st))
              return preconditionFailed(`cannot resume a rollout in state ${st}`);
            db.setRolloutStatus(
              mr,
              r.waves.some((w) => w.status === 'waiting_approval') ? 'waiting_approval' : 'running',
              undefined,
            );
            break;
          case 'abort':
            if (['completed', 'aborted', 'rolled_back', 'superseded'].includes(st))
              return preconditionFailed(`cannot abort a rollout in state ${st}`);
            for (const t of mr.targets)
              if (['pending', 'waiting_window'].includes(t.status)) t.status = 'cancelled';
            for (const w of r.waves)
              if (['pending', 'waiting_approval', 'running'].includes(w.status))
                w.status = 'skipped';
            db.setRolloutStatus(mr, 'aborted', reason ?? `Aborted by ${role}`);
            break;
          case 'rollback':
            if (!['running', 'paused', 'failed'].includes(st))
              return preconditionFailed(`cannot roll back a rollout in state ${st}`);
            for (const t of mr.targets) {
              if (['succeeded', 'in_progress', 'verifying', 'failed'].includes(t.status)) {
                t.status = 'rolled_back';
                const e = db.entries.get(t.agentId);
                if (e && r.kind === 'version' && r.fromRef) {
                  e.agent.version = r.fromRef;
                  db.emit('agent.updated', db.summary(e.agent.id));
                }
              } else if (['pending', 'waiting_window'].includes(t.status)) t.status = 'cancelled';
            }
            db.setRolloutStatus(mr, 'rolled_back', reason ?? `Rolled back by ${role}`);
            break;
          case 'retry-failed':
            if (!['paused', 'running', 'failed'].includes(st))
              return preconditionFailed(`cannot retry targets of a rollout in state ${st}`);
            for (const t of mr.targets)
              if (['failed', 'skipped'].includes(t.status)) {
                t.status = 'pending';
                t.lastError = undefined;
              }
            for (const w of r.waves) if (w.status === 'failed') w.status = 'running';
            db.setRolloutStatus(
              mr,
              st === 'failed' || (mr.simPlan && st === 'paused') ? 'running' : st,
              undefined,
            );
            db.restartSimulation(mr);
            break;
        }
        db.recordAudit(role, `rollout.${action}`, 'rollout', r.id, { reason: body.reason });
        return json(mr.rollout);
      }),
    ),
  ),
  http.post(
    `${BASE}/rollouts/:id/actions/approve`,
    guarded<{ id: string }>('rollouts:operate', async ({ request, params, role }) => {
      const db = getDb();
      const mr = db.findRollout(params.id);
      if (!mr) return notFound('rollout');
      const body = (await request.json()) as { wave?: number; reason?: string };
      const w = mr.rollout.waves.find((x) => x.index === body.wave);
      if (!w) return notFound('wave');
      if (w.status !== 'waiting_approval')
        return preconditionFailed(`wave ${w.index + 1} is not waiting for approval`);
      w.status = 'running';
      w.approvedBy = role;
      w.approvedAt = iso(0);
      w.startedAt = iso(0);
      mr.rollout.currentWave = w.index;
      db.setRolloutStatus(mr, 'running', undefined);
      db.recordAudit(role, 'rollout.approve', 'rollout', mr.rollout.id, {
        wave: w.index,
        reason: body.reason,
      });
      return json(mr.rollout);
    }),
  ),

  /* ------------------------------------------------------------------ releases */
  // getLatestRelease (before /releases/:distribution/:version)
  http.get(
    `${BASE}/releases/latest`,
    guarded('read', ({ url }) => {
      const dist = url.searchParams.get('distribution') ?? 'otelcol-contrib';
      const db = getDb();
      const latest = db.releases.find(
        (r) => r.distribution === dist && r.version === db.latestVersion(dist),
      );
      return latest
        ? json(releaseDetail(latest.distribution, latest.version))
        : notFound('release');
    }),
  ),
  // compareVersions
  http.get(
    `${BASE}/releases/compare`,
    guarded('read', ({ url }) => {
      const dist = url.searchParams.get('distribution') ?? 'otelcol-contrib';
      const from = url.searchParams.get('from');
      const to = url.searchParams.get('to');
      if (!from || !to)
        return problem(400, 'bad_request', 'Bad Request', 'from and to are required');
      if (compareVersions(from, to) >= 0)
        return problem(400, 'bad_request', 'Bad Request', 'from must be lower than to');
      const rel = getDb().releases.filter(
        (r) =>
          r.distribution === dist &&
          compareVersions(r.version, from) > 0 &&
          compareVersions(r.version, to) <= 0,
      );
      return json({
        distribution: dist,
        from,
        to,
        versions: rel.map((r) => r.version).sort(compareVersions),
        breakingChanges: inRange(
          rel.flatMap((r) => r.breakingChanges),
          from,
          to,
        ),
        deprecations: inRange(
          rel.flatMap((r) => r.deprecations),
          from,
          to,
        ),
      });
    }),
  ),
  // refreshReleases
  http.post(
    `${BASE}/releases/actions/refresh`,
    guarded('releases:acknowledge', () =>
      json({ accepted: true, message: 'release poll requested' }, { status: 202 }),
    ),
  ),
  // listReleases
  http.get(
    `${BASE}/releases`,
    guarded('read', ({ url }) => {
      const dist = url.searchParams.get('distribution') ?? 'otelcol-contrib';
      const pre = url.searchParams.get('includePrerelease') === 'true';
      const db = getDb();
      const items = db.releases
        .filter((r) => r.distribution === dist && (pre || !r.prerelease))
        .sort((a, b) => compareVersions(b.version, a.version));
      const pg = page(items, url);
      return json({ ...pg, items: pg.items.map((r) => db.releaseSummary(r)) });
    }),
  ),
  // getRelease
  http.get(
    `${BASE}/releases/:distribution/:version`,
    guarded<{ distribution: string; version: string }>('read', ({ params }) => {
      const r = getDb().releases.find(
        (x) => x.distribution === params.distribution && x.version === params.version,
      );
      return r ? json(releaseDetail(r.distribution, r.version)) : notFound('release');
    }),
  ),
  // acknowledgeRelease
  http.post(
    `${BASE}/releases/:distribution/:version/actions/acknowledge`,
    guarded<{ distribution: string; version: string }>(
      'releases:acknowledge',
      ({ params, role }) => {
        const db = getDb();
        const r = db.releases.find(
          (x) => x.distribution === params.distribution && x.version === params.version,
        );
        if (!r) return notFound('release');
        r.acknowledgedBy = role;
        r.acknowledgedAt = iso(0);
        db.recordAudit(role, 'release.acknowledge', 'release', `${r.distribution}/${r.version}`);
        db.emit('stats.changed', {});
        return json(r);
      },
    ),
  ),

  /* ------------------------------------------------------------------ gitops */
  // getGitOpsStatus / listGitOpsSyncs / listGitOpsObjects / triggerGitOpsSync / gitopsWebhook
  http.get(
    `${BASE}/gitops/status`,
    guarded('read', () => json(getDb().gitopsStatus)),
  ),
  http.get(
    `${BASE}/gitops/syncs`,
    guarded('read', ({ url }) => json(page(getDb().gitopsSyncs, url, 25))),
  ),
  http.get(
    `${BASE}/gitops/objects`,
    guarded('read', () => json({ items: getDb().gitopsObjects })),
  ),
  http.post(
    `${BASE}/gitops/actions/sync`,
    guarded('gitops:sync', ({ role }) => {
      const db = getDb();
      db.recordAudit(role, 'gitops.sync', 'gitops', undefined);
      db.gitopsStatus = { ...db.gitopsStatus, state: 'syncing' };
      db.emit('gitops.synced', { state: 'syncing', issueCount: db.gitopsStatus.issues.length });
      setTimeout(() => {
        const s = db.gitopsStatus;
        const applied = s.latestCommit?.sha !== s.appliedCommit?.sha;
        db.gitopsSyncs.unshift({
          id: (db.gitopsSyncs[0]?.id ?? 400) + 1,
          startedAt: iso(-1500),
          finishedAt: iso(0),
          trigger: 'manual',
          commit: s.latestCommit,
          result: applied ? 'applied' : 'unchanged',
          issues: [],
          changed: applied
            ? ['blueprints/office-linux.yaml', 'fleets/linux-hosts.yaml']
            : undefined,
        });
        db.gitopsStatus = {
          ...s,
          state: 'ok',
          lastSyncAt: iso(0),
          lastSuccessAt: iso(0),
          appliedCommit: s.latestCommit ?? s.appliedCommit,
        };
        db.emit('gitops.synced', {
          state: 'ok',
          appliedCommit: db.gitopsStatus.appliedCommit?.sha,
          issueCount: s.issues.length,
        });
      }, 1500);
      return json({ accepted: true, message: 'sync requested' }, { status: 202 });
    }),
  ),
  http.post(
    `${BASE}/gitops/webhook`,
    guarded('public', () => json({ accepted: true }, { status: 202 })),
  ),

  /* ------------------------------------------------------------------ artifacts */
  // listArtifacts
  http.get(
    `${BASE}/artifacts`,
    guarded('read', ({ url }) => {
      const p = url.searchParams;
      const statuses = p.getAll('status');
      const items = getDb().artifacts.filter((a) => {
        if (p.get('distribution') && a.distribution !== p.get('distribution')) return false;
        if (p.get('version') && a.version !== p.get('version')) return false;
        if (statuses.length && !statuses.includes(a.status)) return false;
        return true;
      });
      // pending/downloading artifacts progress on every poll in mock mode
      for (const a of getDb().artifacts) {
        if (a.status === 'pending') a.status = 'downloading';
        else if (a.status === 'downloading') {
          a.status = 'ready';
          a.sha256 = a.sha256 ?? 'b'.repeat(64);
          a.signatureVerified = true;
          a.signatureIdentity =
            'https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/release-contrib.yaml@refs/tags/v' +
            a.version;
        }
      }
      return json(page(items, url, 25));
    }),
  ),
  // prefetchArtifacts
  http.post(
    `${BASE}/artifacts/actions/prefetch`,
    guarded('artifacts:manage', async ({ request, role }) => {
      const body = (await request.json()) as {
        distribution?: string;
        version?: string;
        platforms?: string[];
      };
      if (!body.version || !/^\d+\.\d+\.\d+$/.test(body.version)) {
        return problem(422, 'validation_failed', 'Validation failed', 'invalid version', [
          {
            severity: 'error',
            code: 'invalid_version',
            message: 'version must look like 0.162.0 (no leading "v")',
            path: 'version',
          },
        ]);
      }
      const db = getDb();
      const platforms = body.platforms?.length ? body.platforms : ['linux/amd64', 'windows/amd64'];
      const created: Artifact[] = [];
      for (const pf of platforms) {
        const [os, arch] = pf.split('/');
        if (
          db.artifacts.some(
            (a) =>
              a.version === body.version && a.os === os && a.arch === arch && a.status !== 'failed',
          )
        )
          continue;
        const a = db.makeArtifact(
          body.version,
          os ?? 'linux',
          arch ?? 'amd64',
          'pending',
          false,
          db.artifacts.length,
        );
        a.id = lat.uuid();
        db.artifacts.unshift(a);
        created.push(a);
      }
      db.recordAudit(role, 'artifact.prefetch', 'artifact', body.version, { platforms });
      return json({ items: created }, { status: 202 });
    }),
  ),
  // getArtifact / deleteArtifact
  http.get(
    `${BASE}/artifacts/:id`,
    guarded<{ id: string }>('read', ({ params }) => {
      const a = getDb().artifacts.find((x) => x.id === params.id);
      return a ? json(a) : notFound('artifact');
    }),
  ),
  http.delete(
    `${BASE}/artifacts/:id`,
    guarded<{ id: string }>('artifacts:manage', ({ params, role }) => {
      const db = getDb();
      const idx = db.artifacts.findIndex((x) => x.id === params.id);
      if (idx < 0) return notFound('artifact');
      db.artifacts.splice(idx, 1);
      db.recordAudit(role, 'artifact.delete', 'artifact', params.id);
      return new HttpResponse(null, { status: 204 });
    }),
  ),

  /* ------------------------------------------------------------------ notifications */
  // listNotificationChannels / testNotificationChannel
  http.get(
    `${BASE}/notifications/channels`,
    guarded('read', () => json({ items: getDb().channels })),
  ),
  http.post(
    `${BASE}/notifications/channels/:name/actions/test`,
    guarded<{ name: string }>('notifications:test', ({ params, role }) => {
      if (!getDb().channels.some((c) => c.name === params.name)) return notFound('channel');
      getDb().recordAudit(role, 'notification.test', 'channel', params.name);
      return json({ accepted: true, message: 'test notification sent' }, { status: 202 });
    }),
  ),
];

function releaseDetail(distribution: string, version: string) {
  const db = getDb();
  const r = db.releases.find((x) => x.distribution === distribution && x.version === version)!;
  const behind: FleetBehind[] = [];
  if (distribution === 'otelcol-contrib') {
    for (const f of FLEETS) {
      const target = db.effectiveTarget(f.name).version ?? f.collector.version ?? '0.0.0';
      if (compareVersions(target, version) >= 0) continue;
      const members = db.collectors().filter((e) => e.agent.assignment.fleetName === f.name);
      const between = db.releases.filter(
        (x) =>
          x.distribution === distribution &&
          compareVersions(x.version, target) > 0 &&
          compareVersions(x.version, version) <= 0,
      );
      behind.push({
        fleet: f.name,
        targetVersion: target,
        agents: members.length,
        versions: db.versionCounts(members),
        breakingCount: between.reduce((s, x) => s + x.breakingChanges.length, 0),
        sourcePath: f.source.path,
      });
    }
  }
  const snippet = behind
    .map((b) => `# ${b.sourcePath}\n${toYaml({ spec: { collector: { version } } })}`)
    .join('\n');
  return {
    release: r,
    isLatest: version === db.latestVersion(distribution),
    fleetsBehind: behind,
    agentsBehind: db.collectors().filter((e) => compareVersions(e.agent.version, version) < 0)
      .length,
    gitopsSnippet: snippet,
    updatableFleets: distribution === 'otelcol-contrib' ? db.updatableFleets(version) : [],
  };
}

const SESSION_KEY = STORAGE_KEY_PREFIX + 'mock.session';
const SESSION_METHOD_KEY = STORAGE_KEY_PREFIX + 'mock.sessionMethod';

export function persistSession(role: Role | null, method: Me['authMethod'] = 'local'): void {
  try {
    window.localStorage.setItem(SESSION_KEY, role ?? 'none');
    window.localStorage.setItem(SESSION_METHOD_KEY, method);
  } catch {
    /* ignore */
  }
}

export function restoreSession(): void {
  try {
    const v = window.localStorage.getItem(SESSION_KEY);
    if (v === 'none') getDb().sessionRole = null;
    else if (v === 'admin' || v === 'operator' || v === 'viewer') getDb().sessionRole = v;
    if (window.localStorage.getItem(SESSION_METHOD_KEY) === 'bootstrap')
      getDb().sessionAuthMethod = 'bootstrap';
  } catch {
    /* ignore */
  }
}
