// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * In-memory "backend" for mock mode and tests. Deterministic fixtures (seeded), actions mutate the
 * state, and a simulation tick produces live updates for the fake event stream.
 */
import type {
  APIToken,
  Agent,
  AgentEvent,
  AgentFacets,
  AgentSummary,
  Artifact,
  AuthConfigInfo,
  AuditEvent,
  ChannelInfo,
  ActorType,
  ConfigSyncState,
  EnrollmentToken,
  FacetValue,
  Fleet,
  FleetCounts,
  FleetStatus,
  FleetVersionOptions,
  FleetVersionSelection,
  FleetVersionTarget,
  Me,
  ReleaseFleetOption,
  SelectFleetVersionRequest,
  SelectFleetVersionResponse,
  TargetProgress,
  VersionOption,
  GitOpsObjectRef,
  GitOpsSync,
  GitOpsSyncStatus,
  OverviewStats,
  Permission,
  Release,
  ReleaseSummary,
  Role,
  Rollout,
  RolloutCounts,
  RolloutPolicy,
  RolloutSummary,
  RolloutTarget,
  SavedFilter,
  SystemInfo,
  TargetStatus,
  User,
  VersionCount,
  WaveStatus,
} from '@/api/types';
import { compareVersions } from '@/lib/format';
import { DEFAULT_HEALTH_WAIT_MS, formatWait, healthWaitMs, isEvaluated } from '@/lib/healthWait';
import { CONFIG_SYNC_STATES } from '@/api/enums';
import { generateAgents, generateEvents, summarize, type MockAgentEntry } from './agents';
import { describeFrame, inFrame } from './frame';
import { fakeHash } from './render';
import { Rng, hashString } from './rng';
import { simulateUpdate, STEP_MS, type UpdateScript } from './updateSim';
import {
  COMMIT_APPLIED,
  COMMIT_LATEST,
  COMMIT_PREV,
  DAY,
  FLEETS,
  FLEET_BY_NAME,
  GITOPS_ISSUES,
  GIT_WEB,
  HOUR,
  iso,
  LATEST_VERSION,
  MIN,
  NOW,
  POLICIES,
  RELEASES,
} from './static';
import { BLUEPRINTS, EXECUTORS } from './static';

export type LiveListener = (type: string, data: unknown) => void;

export interface AgentQueryParams {
  q?: string;
  kind?: string[];
  status?: string[];
  healthy?: boolean;
  fleet?: string[];
  unassigned?: boolean;
  version?: string[];
  outdated?: boolean;
  os?: string[];
  attr?: string[];
  configSync?: string[];
}

/** Mirrors the server: a failed rollout stays active until an operator aborts it. */
const TERMINAL = new Set(['completed', 'aborted', 'rolled_back', 'superseded']);

export function permissionsFor(role: Role): Permission[] {
  const viewer: Permission[] = ['read', 'saved_filters:write'];
  const operator: Permission[] = [
    ...viewer,
    'agents:operate',
    'agents:delete',
    'rollouts:operate',
    'fleets:set_version',
    'releases:acknowledge',
    'gitops:sync',
    'artifacts:manage',
    'notifications:test',
    'audit:read',
  ];
  const admin: Permission[] = [...operator, 'tokens:manage', 'enrollment:manage', 'users:manage'];
  return role === 'admin' ? admin : role === 'operator' ? operator : viewer;
}

/** A running simulated myriarch-updater attempt (rollouts that were started in the mock UI). */
interface UpdateSim {
  agentId: string;
  script: UpdateScript;
  startedAt: number;
  attempt: number;
}

interface MockRollout {
  rollout: Rollout;
  targets: RolloutTarget[];
  /** Present while the simulated update of the (single) target is running. */
  sim?: UpdateSim;
  /** Kept after the simulation ended: what a "retry failed" starts again. */
  simPlan?: { agentId: string; from: string; to: string };
}

/** The mock rolls out to one collector only, so the live view of a single target is visible. */
const MOCK_MAX_TARGETS = 1;

export type SelectResult =
  | { ok: true; response: SelectFleetVersionResponse }
  | {
      ok: false;
      status: 400 | 404 | 409 | 412 | 422;
      code: 'bad_request' | 'not_found' | 'conflict' | 'precondition_failed' | 'validation_failed';
      detail: string;
      issues?: { severity: 'error'; code: string; message: string }[];
    };

type Actor = { name: string; type?: ActorType };

export class MockDb {
  readonly entries = new Map<string, MockAgentEntry>();
  readonly byFleet = new Map<string, string[]>();
  readonly rollouts: MockRollout[] = [];
  readonly runtimeEvents = new Map<string, AgentEvent[]>();
  savedFilters: SavedFilter[] = [];
  apiTokens: APIToken[] = [];
  enrollmentTokens: EnrollmentToken[] = [];
  users: User[] = [];
  audit: AuditEvent[] = [];
  gitopsStatus!: GitOpsSyncStatus;
  gitopsSyncs: GitOpsSync[] = [];
  gitopsObjects: GitOpsObjectRef[] = [];
  artifacts: Artifact[] = [];
  channels: ChannelInfo[] = [];
  releases: Release[] = structuredClone(RELEASES);
  /** UI version selections per fleet (one row per fleet, like fleet_version_selections). */
  readonly selections = new Map<string, FleetVersionSelection>();
  /** Demo/test switch: the leader plans a selection only after `deferPlanMs` (planned=false first). */
  deferPlanning = false;
  deferPlanMs = 3000;
  private simTimer: ReturnType<typeof setInterval> | null = null;
  /** null = logged out; otherwise the role of the session (mock logins accept any password). */
  sessionRole: Role | null = 'admin';
  /** How the mock session signed in (`bootstrap` shows the break-glass banner). */
  sessionAuthMethod: Me['authMethod'] = 'local';
  /** Sign-in options offered by GET /auth/config; tests and demos switch them. */
  authConfig: Pick<
    AuthConfigInfo,
    'oidcEnabled' | 'devLoginEnabled' | 'bootstrapPasswordEnabled' | 'bootstrapTokenEnabled'
  > = {
    oidcEnabled: false,
    devLoginEnabled: true,
    bootstrapPasswordEnabled: false,
    bootstrapTokenEnabled: false,
  };
  /** Bootstrap tokens that already signed in once (they are single use). */
  readonly usedBootstrapTokens = new Set<string>();
  private listeners = new Set<LiveListener>();
  private nextEventId = 5_000_000;
  private nextAuditId = 100000;
  private rng = new Rng(77);
  private tickCount = 0;
  now = () => Date.now();

  constructor() {
    const gen = generateAgents();
    for (const e of gen.agents) this.entries.set(e.agent.id, e);
    for (const [k, v] of gen.byFleet) this.byFleet.set(k, v);
    this.buildRollouts();
    this.buildAdminData();
    this.buildGitops();
    this.seedSelections();
    this.applyTargetVersions();
  }

  /** Stops the simulation timer (a new MockDb replaces this one in tests). */
  dispose(): void {
    if (this.simTimer) clearInterval(this.simTimer);
    this.simTimer = null;
  }

  /* ------------------------------------------------------------------ live */

  subscribe(fn: LiveListener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  emit(type: string, data: unknown): void {
    for (const l of [...this.listeners]) l(type, data);
  }

  /* ------------------------------------------------------------------ agents */

  summary(id: string): AgentSummary | undefined {
    const e = this.entries.get(id);
    return e ? summarize(e, this.latestVersion()) : undefined;
  }

  latestVersion(distribution = 'otelcol-contrib'): string {
    return (
      this.releases
        .filter((r) => r.distribution === distribution && !r.prerelease)
        .map((r) => r.version)
        .sort((a, b) => compareVersions(b, a))[0] ?? LATEST_VERSION
    );
  }

  private matches(s: AgentSummary, e: MockAgentEntry, p: AgentQueryParams, skip?: string): boolean {
    const a = e.agent;
    if (p.q && skip !== 'q') {
      const needle = p.q.toLowerCase();
      const hay = [
        a.hostname,
        a.id,
        a.version,
        ...Object.values(a.nonIdentifyingAttributes),
        ...Object.values(a.labels),
      ]
        .join(' ')
        .toLowerCase();
      if (!hay.includes(needle)) return false;
    }
    if (skip !== 'kind' && p.kind?.length && !p.kind.includes(s.kind)) return false;
    if (skip !== 'status' && p.status?.length && !p.status.includes(s.status)) return false;
    if (skip !== 'healthy' && p.healthy !== undefined && s.healthy !== p.healthy) return false;
    if (skip !== 'fleet') {
      if (p.unassigned && s.fleetName) return false;
      if (!p.unassigned && p.fleet?.length && !p.fleet.includes(s.fleetName ?? '')) return false;
    }
    if (skip !== 'version' && p.version?.length && !p.version.includes(s.version)) return false;
    if (skip !== 'outdated' && p.outdated !== undefined) {
      if (s.kind !== 'collector') return false;
      if (s.outdated !== p.outdated) return false;
    }
    if (skip !== 'os' && p.os?.length && !p.os.includes(s.osType)) return false;
    if (skip !== 'configSync' && p.configSync?.length && !p.configSync.includes(s.configSync))
      return false;
    if (p.attr?.length) {
      const groups = new Map<string, string[]>();
      for (const kv of p.attr) {
        const i = kv.indexOf('=');
        if (i < 0) continue;
        const k = kv.slice(0, i);
        if (skip === `attr:${k}`) continue;
        groups.set(k, [...(groups.get(k) ?? []), kv.slice(i + 1)]);
      }
      for (const [k, values] of groups) {
        const v = k.startsWith('labels.')
          ? a.labels[k.slice(7)]
          : (a.nonIdentifyingAttributes[k] ?? a.identifyingAttributes[k]);
        if (v === undefined || !values.includes(v)) return false;
      }
    }
    return true;
  }

  queryAgents(
    p: AgentQueryParams,
    sort = 'hostname',
    limit = 50,
    offset = 0,
  ): { items: AgentSummary[]; total: number } {
    const latest = this.latestVersion();
    const rows: { s: AgentSummary; e: MockAgentEntry }[] = [];
    for (const e of this.entries.values()) {
      const s = summarize(e, latest);
      if (this.matches(s, e, p)) rows.push({ s, e });
    }
    const desc = sort.startsWith('-');
    const field = desc ? sort.slice(1) : sort;
    const key = (r: { s: AgentSummary; e: MockAgentEntry }): string | number => {
      switch (field) {
        case 'version':
          return r.s.version;
        case 'status':
          return r.s.status;
        case 'fleet':
          return r.s.fleetName ?? '';
        case 'lastSeenAt':
          return r.s.lastSeenAt ?? '';
        case 'firstSeenAt':
          return r.e.agent.firstSeenAt;
        case 'health':
          return r.s.healthy === undefined ? 1 : r.s.healthy ? 0 : 2;
        case 'configSync':
          return CONFIG_SYNC_STATES.indexOf(r.s.configSync);
        case 'osType':
          return r.s.osType;
        case 'siteId':
          return r.s.site?.['site.id'] ?? '';
        default:
          return r.s.hostname.toLowerCase();
      }
    };
    rows.sort((a, b) => {
      const ka = key(a);
      const kb = key(b);
      let c: number;
      if (field === 'version') c = compareVersions(String(ka), String(kb));
      else if (typeof ka === 'number' && typeof kb === 'number') c = ka - kb;
      else c = String(ka).localeCompare(String(kb), 'en', { numeric: true });
      if (c === 0)
        c =
          a.s.hostname.localeCompare(b.s.hostname, 'en', { numeric: true }) ||
          a.s.id.localeCompare(b.s.id);
      return desc ? -c : c;
    });
    return { items: rows.slice(offset, offset + limit).map((r) => r.s), total: rows.length };
  }

  facets(p: AgentQueryParams): AgentFacets {
    const latest = this.latestVersion();
    const count = (
      skip: string,
      pick: (s: AgentSummary, e: MockAgentEntry) => string | undefined,
    ): FacetValue[] => {
      const m = new Map<string, number>();
      for (const e of this.entries.values()) {
        const s = summarize(e, latest);
        if (!this.matches(s, e, p, skip)) continue;
        const v = pick(s, e);
        if (v !== undefined) m.set(v, (m.get(v) ?? 0) + 1);
      }
      return [...m.entries()]
        .map(([value, c]) => ({ value, count: c }))
        .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
    };
    let total = 0;
    for (const e of this.entries.values()) if (this.matches(summarize(e, latest), e, p)) total++;
    return {
      total,
      status: count('status', (s) => s.status),
      health: count('healthy', (s) =>
        s.healthy === undefined ? 'unknown' : s.healthy ? 'healthy' : 'unhealthy',
      ),
      kinds: count('kind', (s) => s.kind),
      versions: count('version', (s) => s.version),
      fleets: count('fleet', (s) => s.fleetName ?? ''),
      osTypes: count('os', (s) => s.osType),
      siteTypes: count('attr:site.type', (s) => s.site?.['site.type']),
      countries: count('attr:site.country', (s) => s.site?.['site.country']),
      configSync: count('configSync', (s) => s.configSync),
    };
  }

  addEvent(agentId: string, ev: Omit<AgentEvent, 'id' | 'agentId' | 'time'>): void {
    const list = this.runtimeEvents.get(agentId) ?? [];
    list.unshift({
      id: this.nextEventId++,
      agentId,
      time: new Date(this.now()).toISOString(),
      ...ev,
    });
    this.runtimeEvents.set(agentId, list);
  }

  events(
    agentId: string,
    before: number,
    limit: number,
  ): { items: AgentEvent[]; nextBefore: number } {
    const e = this.entries.get(agentId);
    if (!e) return { items: [], nextBefore: 0 };
    const idx = [...this.entries.keys()].indexOf(agentId);
    const all = [
      ...(this.runtimeEvents.get(agentId) ?? []),
      ...generateEvents(e.agent, 1_000_000 + idx * 1000),
    ];
    const filtered = all.filter((x) => before === 0 || x.id < before).sort((a, b) => b.id - a.id);
    const items = filtered.slice(0, limit);
    return { items, nextBefore: items.length > 0 ? items[items.length - 1]!.id : 0 };
  }

  removeAgent(id: string): boolean {
    const e = this.entries.get(id);
    if (!e) return false;
    this.entries.delete(id);
    const fleet = e.agent.assignment.fleetName;
    if (fleet)
      this.byFleet.set(
        fleet,
        (this.byFleet.get(fleet) ?? []).filter((x) => x !== id),
      );
    this.emit('agent.removed', { id });
    this.emit('stats.changed', {});
    return true;
  }

  /* ------------------------------------------------------------------ fleets / stats */

  collectors(): MockAgentEntry[] {
    return [...this.entries.values()].filter((e) => e.agent.kind === 'collector');
  }

  fleetCounts(fleetName: string): FleetCounts {
    const members = this.collectors().filter((e) => e.agent.assignment.fleetName === fleetName);
    const target = this.effectiveTarget(fleetName).version;
    return {
      fleet: fleetName,
      total: members.length,
      online: members.filter((e) => e.agent.connection.status === 'online').length,
      healthy: members.filter((e) => e.agent.health?.healthy === true).length,
      inSync: members.filter((e) => e.sync === 'in_sync').length,
      pending: members.filter((e) => e.sync === 'pending').length,
      failed: members.filter((e) => e.sync === 'failed').length,
      targetVersion: target,
      onTarget: members.filter((e) => e.agent.version === target).length,
    };
  }

  versionCounts(entries: MockAgentEntry[]): VersionCount[] {
    const latest = this.latestVersion();
    const m = new Map<string, number>();
    for (const e of entries) m.set(e.agent.version, (m.get(e.agent.version) ?? 0) + 1);
    return [...m.entries()]
      .map(([version, count]) => ({
        version,
        count,
        outdated: compareVersions(version, latest) < 0,
      }))
      .sort((a, b) => compareVersions(b.version, a.version));
  }

  fleetStatus(fleetName: string): FleetStatus {
    this.syncSimulations();
    const versionTarget = this.effectiveTarget(fleetName);
    const members = this.collectors().filter((e) => e.agent.assignment.fleetName === fleetName);
    const latest = this.latestVersion();
    return {
      agents: this.fleetCounts(fleetName),
      versions: this.versionCounts(members),
      activeRollouts: this.rollouts
        .filter((r) => r.rollout.fleetName === fleetName && !TERMINAL.has(r.rollout.status))
        .map((r) => this.rolloutSummary(r.rollout)),
      behindLatest: !!versionTarget.version && compareVersions(versionTarget.version, latest) < 0,
      latestVersion: latest,
      renderErrors: 0,
      versionTarget,
    };
  }

  overview(): OverviewStats {
    const all = [...this.entries.values()];
    const collectors = all.filter((e) => e.agent.kind === 'collector');
    const latest = this.latestVersion();
    const outdated = collectors.filter((e) => compareVersions(e.agent.version, latest) < 0).length;
    const sync = new Map<string, number>();
    for (const e of all) sync.set(e.sync, (sync.get(e.sync) ?? 0) + 1);
    const latestRel = this.releases.find(
      (r) => r.distribution === 'otelcol-contrib' && r.version === latest,
    );
    return {
      generatedAt: new Date(this.now()).toISOString(),
      agents: {
        total: all.length,
        collectors: collectors.length,
        updaters: all.length - collectors.length,
        online: all.filter((e) => e.agent.connection.status === 'online').length,
        offline: all.filter((e) => e.agent.connection.status === 'offline').length,
        healthy: all.filter((e) => e.agent.health?.healthy === true).length,
        unhealthy: all.filter((e) => e.agent.health?.healthy === false).length,
        healthUnknown: all.filter((e) => e.agent.health === undefined).length,
      },
      versions: this.versionCounts(collectors),
      outdated: {
        latestVersion: latest,
        count: outdated,
        percent: collectors.length ? (outdated / collectors.length) * 100 : 0,
      },
      configSync: [...sync.entries()].map(([value, count]) => ({ value, count })),
      fleets: FLEETS.map((f) => this.fleetCounts(f.name)),
      unassigned: collectors.filter((e) => !e.agent.assignment.fleetName).length,
      activeRollouts: this.rollouts
        .filter((r) => !TERMINAL.has(r.rollout.status))
        .map((r) => this.rolloutSummary(r.rollout)),
      latestRelease: latestRel ? this.releaseSummary(latestRel) : undefined,
      gitops: {
        state: this.gitopsStatus.state,
        appliedCommit: this.gitopsStatus.appliedCommit?.sha,
        issueCount: this.gitopsStatus.issues.length,
      },
    };
  }

  /* ------------------------------------------------------------------ releases */

  releaseSummary(r: Release): ReleaseSummary {
    const latest = this.latestVersion(r.distribution);
    const fleetsBehind =
      r.distribution === 'otelcol-contrib'
        ? FLEETS.filter(
            (f) => compareVersions(this.effectiveTarget(f.name).version ?? '0', r.version) < 0,
          ).length
        : 0;
    return {
      distribution: r.distribution,
      version: r.version,
      tag: r.tag,
      publishedAt: r.publishedAt,
      prerelease: r.prerelease,
      htmlUrl: r.htmlUrl,
      breakingCount: r.breakingChanges.length,
      deprecationCount: r.deprecations.length,
      acknowledged: !!r.acknowledgedAt,
      fleetsBehind,
      isLatest: r.version === latest,
    };
  }

  /* ------------------------------------------------------------------ rollouts */

  recount(r: MockRollout): void {
    const count = (ts: RolloutTarget[]): RolloutCounts => {
      const c = (s: TargetStatus) => ts.filter((t) => t.status === s).length;
      return {
        total: ts.length,
        pending: c('pending'),
        waitingWindow: c('waiting_window'),
        inProgress: c('in_progress') + c('verifying'),
        succeeded: c('succeeded'),
        failed: c('failed'),
        skipped: c('skipped'),
        cancelled: c('cancelled'),
        rolledBack: c('rolled_back'),
      };
    };
    r.rollout.counts = count(r.targets);
    for (const w of r.rollout.waves) w.counts = count(r.targets.filter((t) => t.wave === w.index));
    r.rollout.updatedAt = new Date(this.now()).toISOString();
  }

  rolloutSummary(r: Rollout): RolloutSummary {
    return {
      id: r.id,
      kind: r.kind,
      fleetName: r.fleetName,
      policyName: r.policyName,
      status: r.status,
      statusReason: r.statusReason,
      fromRef: r.fromRef,
      toRef: r.toRef,
      gitCommit: r.gitCommit,
      currentWave: r.currentWave,
      waveCount: r.waves.length,
      counts: r.counts,
      origin: r.origin,
      createdBy: r.createdBy,
      createdAt: r.createdAt,
      updatedAt: r.updatedAt,
      finishedAt: r.finishedAt,
    };
  }

  findRollout(id: string): MockRollout | undefined {
    return this.rollouts.find((r) => r.rollout.id === id);
  }

  private makeRollout(opts: {
    id: string;
    kind: 'config' | 'version';
    fleet: string;
    policy: RolloutPolicy;
    status: Rollout['status'];
    statusReason?: string;
    fromRef: string;
    toRef: string;
    gitCommit?: string;
    executor: 'updater' | 'awx' | 'webhook';
    agents: MockAgentEntry[];
    waveOf: (idx: number, total: number, e: MockAgentEntry) => number;
    waveStatuses: WaveStatus[];
    statusOf: (
      waveIdx: number,
      idxInWave: number,
      waveSize: number,
      e: MockAgentEntry,
    ) => TargetStatus;
    createdAgoMs: number;
    startedAgoMs?: number;
    finishedAgoMs?: number;
    currentWave: number;
    /** UI rollouts: who selected the version, why (default: GitOps). */
    ui?: { by: string; reason?: string; atAgoMs: number };
  }): MockRollout {
    const waves = opts.policy.waves.map((w, index) => ({
      index,
      name: w.name,
      selector: w.selector,
      percent: w.percent,
      requireApproval: !!w.requireApproval,
      status: opts.waveStatuses[index] ?? 'pending',
      startedAt:
        opts.waveStatuses[index] &&
        opts.waveStatuses[index] !== 'pending' &&
        opts.waveStatuses[index] !== 'waiting_approval'
          ? iso(-(opts.startedAgoMs ?? HOUR) + index * 20 * MIN)
          : undefined,
      finishedAt:
        opts.waveStatuses[index] === 'completed'
          ? iso(-(opts.startedAgoMs ?? HOUR) + (index + 1) * 20 * MIN)
          : undefined,
      approvedBy:
        w.requireApproval &&
        opts.waveStatuses[index] !== 'waiting_approval' &&
        opts.waveStatuses[index] !== 'pending'
          ? 'alice'
          : undefined,
      approvedAt:
        w.requireApproval &&
        opts.waveStatuses[index] !== 'waiting_approval' &&
        opts.waveStatuses[index] !== 'pending'
          ? iso(-(opts.startedAgoMs ?? HOUR) + 10 * MIN)
          : undefined,
      counts: {
        total: 0,
        pending: 0,
        waitingWindow: 0,
        inProgress: 0,
        succeeded: 0,
        failed: 0,
        skipped: 0,
        cancelled: 0,
        rolledBack: 0,
      },
    }));
    const rng = new Rng(hashString(opts.id));
    const waveSizes = new Map<number, number>();
    const assigned = opts.agents.map((e, i) => ({
      e,
      wave: opts.waveOf(i, opts.agents.length, e),
    }));
    for (const a of assigned) waveSizes.set(a.wave, (waveSizes.get(a.wave) ?? 0) + 1);
    const seen = new Map<number, number>();
    const targets: RolloutTarget[] = assigned.map(({ e, wave }) => {
      const idx = seen.get(wave) ?? 0;
      seen.set(wave, idx + 1);
      const status = opts.statusOf(wave, idx, waveSizes.get(wave) ?? 0, e);
      const notStarted = status === 'pending' || status === 'waiting_window';
      const startedAgo = notStarted ? 0 : rng.int(10, 600) * MIN;
      // A target in the health wait began it 15 to 95 s ago (staggered per target), after the five
      // update steps of two seconds each, so its timeline hangs together.
      const verifiedAgo = (15 + 20 * (idx % 5)) * 1000;
      const verified = status === 'verifying' ? iso(-verifiedAgo) : undefined;
      const started = notStarted
        ? undefined
        : iso(status === 'verifying' ? -verifiedAgo - 5 * STEP_MS : -startedAgo);
      const finished =
        status === 'succeeded' || status === 'failed' || status === 'skipped'
          ? iso(-rng.int(1, 9) * MIN)
          : undefined;
      return {
        rolloutId: opts.id,
        agentId: e.agent.id,
        hostname: e.agent.hostname,
        wave,
        status,
        fromRef: opts.fromRef,
        toRef: opts.toRef,
        attempts:
          status === 'pending' || status === 'waiting_window' ? 0 : status === 'failed' ? 2 : 1,
        startedAt: started,
        verifiedAt: status === 'succeeded' ? finished : verified,
        finishedAt: finished,
        lastError:
          status === 'failed'
            ? 'package install failed: sha256 mismatch for downloaded archive (expected 5d41…, got 7f3a…)'
            : status === 'skipped'
              ? 'agent offline longer than offlineGracePeriod (2h0m0s)'
              : undefined,
        executorRef:
          opts.executor === 'updater' && e.agent.updater ? e.agent.updater.agentId : undefined,
        updatedAt: iso(-rng.int(1, 30) * MIN),
      };
    });
    const rollout: Rollout = {
      id: opts.id,
      kind: opts.kind,
      fleetName: opts.fleet,
      policyName: opts.policy.name,
      policy: opts.policy,
      status: opts.status,
      statusReason: opts.statusReason,
      fromRef: opts.fromRef,
      toRef: opts.toRef,
      gitCommit: opts.gitCommit,
      executor: opts.executor,
      currentWave: opts.currentWave,
      waves,
      counts: {
        total: 0,
        pending: 0,
        waitingWindow: 0,
        inProgress: 0,
        succeeded: 0,
        failed: 0,
        skipped: 0,
        cancelled: 0,
        rolledBack: 0,
      },
      createdBy: opts.ui?.by ?? 'gitops',
      origin: opts.ui ? 'ui' : 'gitops',
      requestReason: opts.ui?.reason,
      createdAt: iso(-opts.createdAgoMs),
      startedAt: opts.startedAgoMs ? iso(-opts.startedAgoMs) : undefined,
      updatedAt: iso(-3 * MIN),
      finishedAt: opts.finishedAgoMs ? iso(-opts.finishedAgoMs) : undefined,
      annotations: opts.ui
        ? {
            distribution: 'otelcol-contrib',
            selection: {
              version: opts.toRef,
              selectedBy: opts.ui.by,
              selectedAt: iso(-opts.ui.atAgoMs),
              reason: opts.ui.reason,
            },
            ackedFailures: [],
          }
        : {
            'git.message':
              opts.kind === 'version' ? `Bump collector to ${opts.toRef}` : 'Tune memory limits',
          },
    };
    const mr: MockRollout = { rollout, targets };
    this.recount(mr);
    rollout.updatedAt = iso(-3 * MIN);
    return mr;
  }

  private buildRollouts(): void {
    const policy = (n: string) => POLICIES.find((p) => p.name === n)!;
    const members = (fleet: string) =>
      (this.byFleet.get(fleet) ?? []).map((id) => this.entries.get(id)!);

    // R1: datacenter version rollout 0.161.0 -> 0.162.0, running (canary done, "all" in progress)
    const dc = members('datacenter');
    this.rollouts.push(
      this.makeRollout({
        id: '5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11',
        kind: 'version',
        fleet: 'datacenter',
        policy: policy('standard'),
        status: 'running',
        fromRef: '0.161.0',
        toRef: '0.162.0',
        executor: 'updater',
        agents: dc,
        waveOf: (i) => (i < 12 ? 0 : 1),
        waveStatuses: ['completed', 'running'],
        statusOf: (w, idx) => {
          if (w === 0) return 'succeeded';
          if (idx < 40) return 'succeeded';
          if (idx < 47) return 'in_progress';
          // in the health wait of the policy "standard" (2 minutes), which ends in 25 to 105 seconds
          if (idx < 52) return 'verifying';
          if (idx === 52) return 'failed';
          if (idx < 60) return 'waiting_window';
          return 'pending';
        },
        createdAgoMs: 3 * HOUR,
        startedAgoMs: 2 * HOUR,
        currentWave: 1,
        ui: {
          by: 'Alice Operator',
          reason: 'Canary in the night window was uneventful',
          atAgoMs: 3 * HOUR,
        },
      }),
    );
    // R2: windows-hosts version rollout, waiting for approval of wave 2 ("early")
    const windowsHosts = members('windows-hosts').filter(
      (e) => compareVersions(e.agent.version, '0.162.0') < 0 || e.agent.labels['pilot'] === 'true',
    );
    this.rollouts.push(
      this.makeRollout({
        id: '8a7d9c20-3b1e-4f65-b0a2-6c4e1d9f5a22',
        kind: 'version',
        fleet: 'windows-hosts',
        policy: policy('edge-careful'),
        status: 'waiting_approval',
        statusReason: 'Wave 2 "early" requires approval',
        fromRef: '0.161.0',
        toRef: '0.162.0',
        executor: 'updater',
        agents: windowsHosts,
        waveOf: (i, total, e) => {
          if (e.agent.labels['pilot'] === 'true') return 0;
          const rest = i / total;
          return rest < 0.2 ? 1 : rest < 0.6 ? 2 : 3;
        },
        waveStatuses: ['completed', 'waiting_approval', 'pending', 'pending'],
        statusOf: (w) => (w === 0 ? 'succeeded' : 'pending'),
        createdAgoMs: 26 * HOUR,
        startedAgoMs: 25 * HOUR,
        currentWave: 1,
      }),
    );
    // R3: edge-sites config rollout, completed 2 days ago
    const edgeSites = members('edge-sites');
    this.rollouts.push(
      this.makeRollout({
        id: 'c3e5f7a9-1b2d-4e6f-8a0c-2d4f6a8c0e33',
        kind: 'config',
        fleet: 'edge-sites',
        policy: policy('edge-careful'),
        status: 'completed',
        fromRef: COMMIT_PREV,
        toRef: COMMIT_APPLIED,
        gitCommit: COMMIT_APPLIED,
        executor: 'updater',
        agents: edgeSites,
        waveOf: (i, total) => (i < total * 0.1 ? 1 : i < total * 0.5 ? 2 : 3),
        waveStatuses: ['skipped', 'completed', 'completed', 'completed'],
        statusOf: (_w, _i, _s, e) =>
          e.agent.connection.status === 'offline' ? 'skipped' : 'succeeded',
        createdAgoMs: 2 * DAY,
        startedAgoMs: 2 * DAY - 10 * MIN,
        finishedAgoMs: 2 * DAY - 4 * HOUR,
        currentWave: 3,
      }),
    );
    // Make agent versions consistent with the version rollouts.
    for (const mr of this.rollouts) {
      if (mr.rollout.kind !== 'version') continue;
      for (const t of mr.targets) {
        const e = this.entries.get(t.agentId);
        if (!e) continue;
        if (t.status === 'succeeded' || t.status === 'verifying')
          e.agent.version = mr.rollout.toRef;
        else if (
          t.status === 'pending' ||
          t.status === 'in_progress' ||
          t.status === 'waiting_window' ||
          t.status === 'failed'
        ) {
          if (compareVersions(e.agent.version, mr.rollout.toRef) >= 0)
            e.agent.version = mr.rollout.fromRef ?? '0.161.0';
        }
        e.agent.identifyingAttributes['service.version'] = e.agent.version;
      }
    }
  }

  /** Applies the effects of a status change that an operator action caused. */
  setRolloutStatus(mr: MockRollout, status: Rollout['status'], reason?: string): void {
    mr.rollout.status = status;
    mr.rollout.statusReason = reason;
    if (TERMINAL.has(status)) {
      mr.rollout.finishedAt = new Date(this.now()).toISOString();
      mr.sim = undefined;
    }
    this.recount(mr);
    this.emit('rollout.updated', this.rolloutSummary(mr.rollout));
    this.emit('stats.changed', {});
  }

  /* ------------------------------------------------------------------ admin data */

  recordAudit(
    actorName: string,
    action: string,
    targetType: string | undefined,
    targetId: string | undefined,
    details?: Record<string, unknown>,
    opts: { actorType?: ActorType; result?: 'success' | 'failure'; time?: string } = {},
  ): void {
    this.audit.unshift({
      id: this.nextAuditId++,
      time: opts.time ?? new Date(this.now()).toISOString(),
      actorType: opts.actorType ?? 'user',
      actorId: actorName,
      actorName,
      action,
      targetType,
      targetId,
      result: opts.result ?? 'success',
      details,
      sourceIp: '10.20.4.17',
      requestId: `req_${this.rng.hex(12)}`,
    });
  }

  private buildAdminData(): void {
    const rng = new Rng(4242);
    this.users = [
      {
        id: 'u-0001-0000-0000-000000000001',
        subject: 'admin',
        provider: 'local',
        name: 'admin',
        role: 'admin',
        roleSource: 'manual',
        disabled: false,
        createdAt: iso(-90 * DAY),
        lastLoginAt: iso(-5 * MIN),
      },
      {
        id: 'u-0002-0000-0000-000000000002',
        subject: 'operator',
        provider: 'local',
        name: 'operator',
        role: 'operator',
        roleSource: 'manual',
        disabled: false,
        createdAt: iso(-90 * DAY),
        lastLoginAt: iso(-2 * DAY),
      },
      {
        id: 'u-0003-0000-0000-000000000003',
        subject: 'viewer',
        provider: 'local',
        name: 'viewer',
        role: 'viewer',
        roleSource: 'manual',
        disabled: false,
        createdAt: iso(-90 * DAY),
      },
      {
        id: 'u-0004-0000-0000-000000000004',
        subject: 'alice@example.com',
        provider: 'oidc',
        email: 'alice@example.com',
        name: 'Alice Operator',
        role: 'operator',
        roleSource: 'mapping',
        disabled: false,
        createdAt: iso(-60 * DAY),
        lastLoginAt: iso(-3 * HOUR),
      },
      {
        id: 'u-0005-0000-0000-000000000005',
        subject: 'carol@example.com',
        provider: 'oidc',
        email: 'carol@example.com',
        name: 'Carol Viewer',
        role: 'viewer',
        roleSource: 'mapping',
        disabled: false,
        createdAt: iso(-45 * DAY),
        lastLoginAt: iso(-9 * DAY),
      },
      {
        id: 'u-0006-0000-0000-000000000006',
        subject: 'bob@example.com',
        provider: 'oidc',
        email: 'bob@example.com',
        name: 'Bob Admin',
        role: 'admin',
        roleSource: 'manual',
        disabled: false,
        createdAt: iso(-80 * DAY),
        lastLoginAt: iso(-1 * DAY),
      },
      {
        id: 'u-0007-0000-0000-000000000007',
        subject: 'old.user@example.com',
        provider: 'oidc',
        email: 'old.user@example.com',
        name: 'Former employee',
        role: 'viewer',
        roleSource: 'mapping',
        disabled: true,
        createdAt: iso(-200 * DAY),
        lastLoginAt: iso(-120 * DAY),
      },
      {
        // Deactivated by configuration (OIDC is on): the row stays for the audit trail.
        id: 'u-0008-0000-0000-000000000008',
        subject: 'bootstrap:admin',
        provider: 'bootstrap',
        name: 'admin',
        role: 'admin',
        roleSource: 'mapping',
        disabled: true,
        createdAt: iso(-90 * DAY),
        lastLoginAt: iso(-89 * DAY),
      },
    ];
    this.apiTokens = [
      {
        id: 'a1000000-0000-4000-8000-000000000001',
        name: 'ci-gitops-validate',
        displayPrefix: 'myra_3kQ9zXw',
        role: 'viewer',
        createdBy: 'admin',
        createdAt: iso(-70 * DAY),
        lastUsedAt: iso(-2 * HOUR),
      },
      {
        id: 'a1000000-0000-4000-8000-000000000002',
        name: 'awx-callback',
        displayPrefix: 'myra_Pq81mNz',
        role: 'operator',
        createdBy: 'bob@example.com',
        createdAt: iso(-30 * DAY),
        expiresAt: iso(150 * DAY),
        lastUsedAt: iso(-1 * DAY),
      },
      {
        id: 'a1000000-0000-4000-8000-000000000003',
        name: 'old-migration',
        displayPrefix: 'myra_7Yh2LcD',
        role: 'admin',
        createdBy: 'admin',
        createdAt: iso(-180 * DAY),
        revokedAt: iso(-100 * DAY),
      },
    ];
    this.enrollmentTokens = [
      {
        id: 'e2000000-0000-4000-8000-000000000001',
        name: 'edge-sites-2026',
        displayPrefix: 'myre_Kd93nXa',
        labels: { rollout: 'wave-2026' },
        createdBy: 'admin',
        createdAt: iso(-120 * DAY),
        useCount: 750,
        lastUsedAt: iso(-12 * MIN),
      },
      {
        id: 'e2000000-0000-4000-8000-000000000002',
        name: 'data-centers',
        displayPrefix: 'myre_Vb41qRt',
        createdBy: 'admin',
        createdAt: iso(-110 * DAY),
        useCount: 120,
        lastUsedAt: iso(-40 * MIN),
      },
      {
        id: 'e2000000-0000-4000-8000-000000000003',
        name: 'lab-dev',
        displayPrefix: 'myre_Zz07yUp',
        labels: { env: 'lab' },
        createdBy: 'alice@example.com',
        createdAt: iso(-20 * DAY),
        expiresAt: iso(-1 * DAY),
        useCount: 3,
        lastUsedAt: iso(-3 * DAY),
      },
      {
        id: 'e2000000-0000-4000-8000-000000000004',
        name: 'retired-pilot',
        displayPrefix: 'myre_Mn55kLs',
        createdBy: 'admin',
        createdAt: iso(-200 * DAY),
        revokedAt: iso(-150 * DAY),
        useCount: 15,
      },
    ];
    this.enrollmentTokens.push({
      id: 'e2000000-0000-4000-8000-000000000005',
      name: 'sites-declared',
      displayPrefix: 'myre_Hc62wTq',
      labels: { source: 'config' },
      createdBy: 'config',
      createdAt: iso(-60 * DAY),
      useCount: 42,
      lastUsedAt: iso(-1 * HOUR),
    });
    this.channels = [
      {
        name: 'teams-observability',
        type: 'teams',
        events: ['release.new', 'rollout.waiting_approval', 'rollout.failed'],
        minSeverity: 'warning',
        target: 'https://example.webhook.office.com/webhookb2/…/IncomingWebhook/…',
      },
      {
        name: 'slack-ops',
        type: 'slack',
        events: ['rollout.failed', 'gitops.error'],
        minSeverity: 'critical',
        target: 'https://hooks.slack.com/services/T000/B000/…',
      },
      {
        name: 'itsm-webhook',
        type: 'webhook',
        events: [],
        minSeverity: 'critical',
        target: 'https://itsm.example.internal/api/myriarch/events',
      },
    ];
    this.artifacts = [
      ['0.162.0', 'linux', 'amd64', 'ready', true],
      ['0.162.0', 'windows', 'amd64', 'ready', true],
      ['0.162.0', 'linux', 'arm64', 'ready', true],
      ['0.161.0', 'linux', 'amd64', 'ready', true],
      ['0.161.0', 'windows', 'amd64', 'ready', true],
      ['0.163.0', 'linux', 'amd64', 'downloading', false],
      ['0.163.0', 'windows', 'amd64', 'pending', false],
      ['0.160.0', 'windows', 'amd64', 'failed', false],
    ].map(([version, os, arch, status, ok], i) =>
      this.makeArtifact(
        String(version),
        String(os),
        String(arch),
        status as Artifact['status'],
        !!ok,
        i,
      ),
    );

    const actions: [string, string, string | undefined][] = [
      ['rollout.approve', 'rollout', '8a7d9c20-3b1e-4f65-b0a2-6c4e1d9f5a22'],
      ['rollout.pause', 'rollout', '5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11'],
      ['rollout.resume', 'rollout', '5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11'],
      ['agent.restart', 'agent', [...this.entries.keys()][3]],
      ['agent.resync', 'agent', [...this.entries.keys()][17]],
      ['release.acknowledge', 'release', 'otelcol-contrib/0.162.0'],
      ['gitops.sync', 'gitops', undefined],
      ['token.create', 'enrollment-token', 'e2000000-0000-4000-8000-000000000003'],
      ['user.update', 'user', 'u-0005-0000-0000-000000000005'],
      ['auth.login', 'user', 'u-0001-0000-0000-000000000001'],
    ];
    const actors = ['admin', 'Alice Operator', 'Bob Admin', 'operator'];
    for (let i = 0; i < 120; i++) {
      const [action, targetType, targetId] = rng.pick(actions);
      const actor = rng.pick(actors);
      this.audit.push({
        id: 99999 - i,
        time: iso(-(i * 47 + rng.int(1, 40)) * MIN),
        actorType: rng.chance(0.15) ? 'gitops' : 'user',
        actorId: actor.toLowerCase().replace(' ', '.'),
        actorName: actor,
        action,
        targetType,
        targetId,
        result: rng.chance(0.93) ? 'success' : 'failure',
        details: action.startsWith('rollout')
          ? { reason: rng.pick(['Maintenance', 'Pilot succeeded', 'Check error rate']) }
          : undefined,
        sourceIp: `10.20.${rng.int(1, 9)}.${rng.int(2, 250)}`,
        requestId: `req_${rng.hex(12)}`,
      });
    }
  }

  makeArtifact(
    version: string,
    os: string,
    arch: string,
    status: Artifact['status'],
    verified: boolean,
    i: number,
  ): Artifact {
    const ext = 'tar.gz';
    const filename = `otelcol-contrib_${version}_${os}_${arch}.${ext}`;
    const ready = status === 'ready';
    return {
      id: `d3000000-0000-4000-8000-0000000000${String(i + 10).padStart(2, '0')}`,
      distribution: 'otelcol-contrib',
      version,
      os,
      arch,
      format: ext,
      filename,
      size: 215_000_000 + i * 1_300_000,
      sha256: ready ? fakeHash(filename) : undefined,
      signatureVerified: verified,
      signatureIdentity: verified
        ? 'https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/release-contrib.yaml@refs/tags/v' +
          version
        : undefined,
      verificationError:
        status === 'failed'
          ? 'sha256 mismatch: expected 5d41402abc4b2a76…, got 7f3a9e11d0c88b42…'
          : undefined,
      sourceUrl: `https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v${version}/${filename}`,
      status,
      error: status === 'failed' ? 'verification failed' : undefined,
      createdAt: iso(-(i + 1) * 5 * HOUR),
      updatedAt: iso(-(i + 1) * 3 * HOUR),
    };
  }

  private buildGitops(): void {
    this.gitopsStatus = {
      source: 'https://git.example.internal/observability/myriarch-config.git',
      branch: 'main',
      path: '/',
      state: 'ok',
      lastSyncAt: iso(-2 * MIN),
      lastSuccessAt: iso(-2 * MIN),
      appliedCommit: {
        sha: COMMIT_APPLIED,
        author: 'Bob Admin',
        message: 'edge-sites: new journal paths and target version 0.161.0',
        time: iso(-2 * DAY),
      },
      latestCommit: {
        sha: COMMIT_LATEST,
        author: 'Alice Operator',
        message: 'linux-hosts: add chronyd.service to journaldUnits',
        time: iso(-14 * MIN),
      },
      issues: GITOPS_ISSUES,
      objectCounts: {
        Fleet: FLEETS.length,
        Blueprint: BLUEPRINTS.length,
        RolloutPolicy: POLICIES.length - 1,
        Executor: EXECUTORS.length,
        Inventory: 1,
      },
    };
    const rng = new Rng(9);
    const triggers: GitOpsSync['trigger'][] = ['poll', 'poll', 'poll', 'webhook', 'manual'];
    for (let i = 0; i < 40; i++) {
      const applied = i % 9 === 3;
      const invalid = i === 14;
      this.gitopsSyncs.push({
        id: 400 - i,
        startedAt: iso(-(i * 31 + 2) * MIN),
        finishedAt: iso(-(i * 31 + 2) * MIN + 1800),
        trigger: i === 39 ? 'startup' : rng.pick(triggers),
        commit: {
          sha: fakeHash(`commit${i}`).slice(0, 40),
          author: 'Bob Admin',
          message: invalid ? 'fleets: edge-lu (WIP)' : 'Update configuration',
          time: iso(-(i * 31 + 5) * MIN),
        },
        result: invalid ? 'invalid' : applied ? 'applied' : 'unchanged',
        issues: invalid
          ? [
              {
                severity: 'error',
                code: 'unknown_blueprint',
                message: 'Fleet "edge-lu" references unknown blueprint "edge-windows-lu"',
                file: 'fleets/edge-lu.yaml',
                line: 14,
                object: 'Fleet/edge-lu',
              },
            ]
          : [],
        added: applied && i % 2 === 1 ? ['fleets/edge-lu.yaml'] : undefined,
        changed: applied ? ['fleets/edge-sites.yaml', 'blueprints/edge-windows.yaml'] : undefined,
        removed: applied && i === 21 ? ['executors/legacy-awx.yaml'] : undefined,
      });
    }
    const obj = (kind: GitOpsObjectRef['kind'], name: string, path: string): GitOpsObjectRef => ({
      kind,
      name,
      path,
      commit: COMMIT_APPLIED,
      webUrl: `${GIT_WEB}/${path}`,
    });
    this.gitopsObjects = [
      ...FLEETS.map((f) => obj('Fleet', f.name, `fleets/${f.name}.yaml`)),
      ...BLUEPRINTS.map((b) => obj('Blueprint', b.name, `blueprints/${b.name}.yaml`)),
      ...POLICIES.filter((p) => p.name !== 'default').map((p) =>
        obj('RolloutPolicy', p.name, `policies/${p.name}.yaml`),
      ),
      ...EXECUTORS.map((e) => obj('Executor', e.name, `executors/${e.name}.yaml`)),
      obj('Inventory', 'sites', 'inventory/sites.yaml'),
    ];
  }

  system(): SystemInfo {
    return {
      version: '0.1.0-alpha.1',
      commit: '4be81d09c7a2f3e5',
      buildDate: new Date(NOW - 3 * DAY).toISOString(),
      replicaId: 'myriarchd-1',
      leaderId: 'myriarchd-0',
      replicas: [0, 1, 2].map((n) => ({
        id: `myriarchd-${n}`,
        hostname: `myriarchd-${n}.fleet.svc`,
        version: '0.1.0-alpha.1',
        startedAt: iso(-(n + 1) * 2 * DAY),
        lastHeartbeatAt: iso(-3000),
        isLeader: n === 0,
        connections: 330 + n * 12,
      })),
      publicUrl: window.location.origin,
      opampEndpoint: `${window.location.protocol === 'https:' ? 'wss' : 'ws'}://${window.location.host}/v1/opamp`,
      database: { ok: true, serverVersion: 'PostgreSQL 17.6', migrationVersion: 3 },
      features: {
        oidc: this.authConfig.oidcEnabled,
        devLogin: this.authConfig.devLoginEnabled,
        bootstrapAdmin:
          this.authConfig.bootstrapPasswordEnabled || this.authConfig.bootstrapTokenEnabled,
        gitops: true,
        releases: true,
        devMode: true,
      },
      settings: {
        gitopsSource: this.gitopsStatus.source,
        gitopsBranch: 'main',
        gitopsPollInterval: '1m',
        releasesRepo: 'open-telemetry/opentelemetry-collector-releases',
        releasesPollInterval: '1h',
        trackedDistributions: ['otelcol-contrib', 'opampsupervisor'],
        artifactSource: 'github',
        signatureVerification: true,
        defaultTimezone: 'Europe/Amsterdam',
        offlineAfter: '3m',
        retentionAgentEvents: '720h',
        retentionAudit: '9600h',
      },
    };
  }

  /* ------------------------------------------------------------------ simulation */

  /** One simulation step: heartbeats, health flaps and rollout progress -> live events. */
  tick(): void {
    this.tickCount++;
    const online = [...this.entries.values()].filter((e) => e.agent.connection.status === 'online');
    const n = this.rng.int(1, 3);
    for (let i = 0; i < n && online.length > 0; i++) {
      const e = this.rng.pick(online);
      const nowIso = new Date(this.now()).toISOString();
      e.agent.connection.lastSeenAt = nowIso;
      if (this.rng.chance(0.15) && e.agent.health) {
        e.agent.health.healthy = !e.agent.health.healthy;
        e.agent.health.status = e.agent.health.healthy ? 'StatusOK' : 'StatusRecoverableError';
        e.agent.health.lastError = e.agent.health.healthy
          ? undefined
          : 'exporter otlp_grpc/gateway: connection refused';
        e.agent.health.updatedAt = nowIso;
        this.addEvent(e.agent.id, {
          type: 'health_changed',
          severity: e.agent.health.healthy ? 'info' : 'warning',
          message: e.agent.health.healthy
            ? 'Health changed to healthy'
            : 'Health changed to unhealthy: exporter otlp_grpc/gateway: connection refused',
        });
      }
      this.emit('agent.updated', summarize(e, this.latestVersion()));
    }
    if (this.rng.chance(0.08)) {
      const off = this.rng.pick(online);
      off.agent.connection.status = 'offline';
      off.agent.connection.disconnectedAt = new Date(this.now()).toISOString();
      this.addEvent(off.agent.id, {
        type: 'disconnected',
        severity: 'warning',
        message: 'Connection closed by peer',
      });
      this.emit('agent.updated', summarize(off, this.latestVersion()));
      this.emit('stats.changed', {});
    }
    // Progress the rollouts: pending -> in_progress -> succeeded (agent version changes).
    this.syncSimulations();
    for (const mr of this.rollouts) {
      // A simulated update is driven by syncSimulations.
      if (mr.simPlan) continue;
      // Like the engine, which evaluates its in-flight targets before it looks at the status of the
      // rollout, the targets in the health wait finish in every status that the engine evaluates, so
      // also while the rollout is paused or waits for an approval. The countdown of the rollout page
      // keeps counting then, and the wait has to end.
      const finished = isEvaluated(mr.rollout.status) ? this.finishHealthWaits(mr) : 0;
      if (mr.rollout.status !== 'running') {
        // Only a running rollout starts targets and moves on to the next wave.
        if (finished > 0) {
          this.recount(mr);
          this.emit('rollout.updated', this.rolloutSummary(mr.rollout));
        }
        continue;
      }
      const inProg = mr.targets.filter((t) => t.status === 'in_progress');
      // The simplification of this demo: a target in progress succeeds without a health wait of its
      // own, and only while the rollout runs.
      if (inProg.length > 0 && this.rng.chance(0.8)) this.succeedTarget(mr, this.rng.pick(inProg));
      const runningWaves = new Set(
        mr.rollout.waves.filter((w) => w.status === 'running').map((w) => w.index),
      );
      const pend = mr.targets.filter(
        (t) =>
          (t.status === 'pending' || t.status === 'waiting_window') && runningWaves.has(t.wave),
      );
      if (pend.length > 0 && inProg.length < 12) {
        const t = pend[0]!;
        t.status = 'in_progress';
        t.startedAt = t.updatedAt = new Date(this.now()).toISOString();
        t.attempts = 1;
      }
      this.recount(mr);
      this.advanceWaves(mr);
      this.emit('rollout.updated', this.rolloutSummary(mr.rollout));
    }
    if (this.tickCount % 8 === 0) this.emit('stats.changed', {});
  }

  /**
   * The rule of the engine for the targets in the health wait: a target succeeds once the wait of
   * the policy snapshot is over (verifiedAt + healthWait). Returns how many targets it finished;
   * the caller counts them again and tells the page.
   */
  private finishHealthWaits(mr: MockRollout): number {
    const waitMs = healthWaitMs(mr.rollout.policy.healthWait) ?? DEFAULT_HEALTH_WAIT_MS;
    const now = this.now();
    let finished = 0;
    for (const t of mr.targets) {
      if (t.status === 'verifying' && t.verifiedAt && now - Date.parse(t.verifiedAt) >= waitMs) {
        this.succeedTarget(mr, t);
        finished++;
      }
    }
    return finished;
  }

  /** A target reaches its final state: the agent runs the target version, with the live events. */
  private succeedTarget(mr: MockRollout, t: RolloutTarget): void {
    const finishedAt = new Date(this.now()).toISOString();
    t.status = 'succeeded';
    t.verifiedAt ??= finishedAt;
    t.finishedAt = t.updatedAt = finishedAt;
    const e = this.entries.get(t.agentId);
    if (e && e.agent.version !== mr.rollout.toRef) {
      e.agent.version = mr.rollout.toRef;
      e.agent.identifyingAttributes['service.version'] = mr.rollout.toRef;
      this.addEvent(e.agent.id, {
        type: 'version_changed',
        severity: 'info',
        message: `Version changed to ${mr.rollout.toRef}`,
        details: { from: t.fromRef, to: t.toRef },
      });
      this.emit('agent.updated', summarize(e, this.latestVersion()));
    }
  }

  /** Completes finished waves and starts the next one (or waits for its approval). */
  private advanceWaves(mr: MockRollout): void {
    const r = mr.rollout;
    for (const w of r.waves) {
      if (w.status !== 'running') continue;
      const open = mr.targets.filter(
        (t) =>
          t.wave === w.index &&
          ['pending', 'waiting_window', 'in_progress', 'verifying'].includes(t.status),
      ).length;
      if (open > 0) continue;
      w.status = 'completed';
      w.finishedAt = new Date(this.now()).toISOString();
      const next = r.waves.find((x) => x.index === w.index + 1);
      if (!next) {
        this.setRolloutStatus(mr, 'completed');
      } else if (next.requireApproval) {
        next.status = 'waiting_approval';
        r.currentWave = next.index;
        this.setRolloutStatus(
          mr,
          'waiting_approval',
          `Wave ${next.index + 1} "${next.name}" requires approval`,
        );
      } else {
        next.status = 'running';
        next.startedAt = new Date(this.now()).toISOString();
        r.currentWave = next.index;
      }
    }
  }

  /* ------------------------------------------------------------------ version selection */

  private fleetOf(name: string): Fleet | undefined {
    return FLEET_BY_NAME.get(name);
  }

  private membersOf(fleetName: string): MockAgentEntry[] {
    return this.collectors().filter((e) => e.agent.assignment.fleetName === fleetName);
  }

  /** The effective target of a fleet (mirrors domain.ResolveVersionTarget). */
  effectiveTarget(fleetName: string): FleetVersionTarget {
    const fleet = this.fleetOf(fleetName);
    const git = fleet?.collector.version || undefined;
    const frame = fleet?.update.selection;
    const sel = this.selections.get(fleetName);
    let version: string | undefined;
    let source: FleetVersionTarget['source'];
    if (sel?.state === 'active') {
      version = sel.version;
      source = 'ui';
    } else if (sel?.state === 'invalidated') {
      source = 'hold';
    } else if (git) {
      version = git;
      source = 'git';
    } else source = 'none';
    return {
      version,
      source,
      gitVersion: git,
      selectionEnabled: !!frame?.enabled,
      frame,
      selection: sel,
    };
  }

  /** Writes the effective target into the assignment of every member (what the server does on reassign). */
  private applyTargetVersions(): void {
    for (const f of FLEETS) {
      const target = this.effectiveTarget(f.name).version;
      for (const e of this.membersOf(f.name)) e.agent.assignment.targetVersion = target;
    }
  }

  /** Fixture selections: datacenter was updated from the UI, edge-sites lost its selection to a narrower Git frame. */
  private seedSelections(): void {
    const dc = this.rollouts.find(
      (r) => r.rollout.fleetName === 'datacenter' && r.rollout.kind === 'version',
    );
    this.selections.set('datacenter', {
      fleet: 'datacenter',
      version: '0.162.0',
      state: 'active',
      gitVersion: '0.161.0',
      gitCommit: COMMIT_APPLIED,
      selectedBy: 'Alice Operator',
      selectedAt: iso(-3 * HOUR),
      reason: 'Canary in the night window was uneventful',
      rolloutId: dc?.rollout.id,
      plannedAt: iso(-3 * HOUR + 4000),
      updatedAt: iso(-3 * HOUR + 4000),
    });
    this.selections.set('edge-sites', {
      fleet: 'edge-sites',
      version: '0.162.0',
      state: 'invalidated',
      stateReason: `version 0.162.0 is outside the frame of commit ${COMMIT_APPLIED.slice(0, 7)} (${describeFrame(FLEET_BY_NAME.get('edge-sites')!.update.selection!)})`,
      gitVersion: '0.161.0',
      gitCommit: COMMIT_PREV,
      selectedBy: 'Bob Admin',
      selectedAt: iso(-2 * DAY - 3 * HOUR),
      reason: 'Pilot at two edge sites',
      updatedAt: iso(-2 * DAY),
    });
    this.recordAudit(
      'Alice Operator',
      'fleet.version.select',
      'fleet',
      'datacenter',
      {
        version: '0.162.0',
        reason: 'Canary in the night window was uneventful',
        previousVersion: '0.161.0',
        previousSource: 'git',
        gitVersion: '0.161.0',
        rolloutId: dc?.rollout.id,
      },
      { time: iso(-3 * HOUR) },
    );
    this.recordAudit(
      'Bob Admin',
      'fleet.version.select',
      'fleet',
      'edge-sites',
      {
        version: '0.162.0',
        reason: 'Pilot at two edge sites',
        previousVersion: '0.161.0',
        previousSource: 'git',
        gitVersion: '0.161.0',
      },
      { time: iso(-2 * DAY - 3 * HOUR) },
    );
    this.recordAudit(
      'gitops',
      'fleet.version.invalidated',
      'fleet',
      'edge-sites',
      { version: '0.162.0', frame: '>=0.160.0, <0.162.0', commit: COMMIT_APPLIED },
      { time: iso(-2 * DAY), actorType: 'gitops' },
    );
  }

  private blockingRollout(fleetName: string): Rollout | undefined {
    return this.rollouts.find(
      (r) =>
        r.rollout.fleetName === fleetName &&
        r.rollout.kind === 'version' &&
        (r.rollout.status === 'failed' || r.rollout.status === 'rolling_back'),
    )?.rollout;
  }

  private activeVersionRollout(fleetName: string): MockRollout | undefined {
    return this.rollouts.find(
      (r) =>
        r.rollout.fleetName === fleetName &&
        r.rollout.kind === 'version' &&
        !TERMINAL.has(r.rollout.status),
    );
  }

  /** Evaluates one candidate version against the frame, like the server's version options. */
  evaluateVersion(fleetName: string, version: string): VersionOption {
    const fleet = this.fleetOf(fleetName)!;
    const frame = fleet.update.selection;
    const target = this.effectiveTarget(fleetName);
    const members = this.membersOf(fleetName);
    const rel = this.releases.find(
      (r) =>
        r.distribution === (fleet.collector.distribution ?? 'otelcol-contrib') &&
        r.version === version,
    );
    const prerelease = rel?.prerelease ?? version.includes('-');
    const known = !!rel;
    const ref =
      target.version ??
      members.map((m) => m.agent.version).sort(compareVersions)[0] ??
      target.gitVersion;
    const isDowngrade = !!target.version && compareVersions(version, target.version) < 0;
    let reasonCode: VersionOption['reasonCode'];
    let reason: string | undefined;
    if (version === target.version) {
      reasonCode = 'current_target';
      reason = 'already the target version';
    } else if (prerelease) {
      reasonCode = 'prerelease_not_allowed';
      reason = 'pre-releases cannot be selected';
    } else if (!known && !(frame?.versions ?? []).includes(version)) {
      reasonCode = 'unknown_version';
      reason = 'not a known release';
    } else if (!inFrame(frame, version)) {
      reasonCode = 'outside_frame';
      reason = 'outside the frame defined in Git';
    } else if (isDowngrade && !frame?.allowDowngrade) {
      reasonCode = 'downgrade_not_allowed';
      reason = 'downgrades are not allowed by the frame';
    }
    const breaking = ref
      ? this.releases
          .filter(
            (r) =>
              r.distribution === (rel?.distribution ?? 'otelcol-contrib') &&
              compareVersions(r.version, ref) > 0 &&
              compareVersions(r.version, version) <= 0,
          )
          .reduce(
            (n, r) => ({ b: n.b + r.breakingChanges.length, d: n.d + r.deprecations.length }),
            {
              b: 0,
              d: 0,
            },
          )
      : { b: 0, d: 0 };
    return {
      version,
      selectable: reasonCode === undefined,
      reasonCode,
      reason,
      known,
      prerelease,
      isLatest: version === this.latestVersion(),
      isCurrentTarget: version === target.version,
      isGitVersion: version === target.gitVersion,
      isDowngrade,
      publishedAt: rel?.publishedAt,
      agentsToUpdate: members.filter((m) => m.agent.version !== version).length,
      breakingCount: breaking.b,
      deprecationCount: breaking.d,
    };
  }

  versionOptions(fleetName: string): FleetVersionOptions | undefined {
    const fleet = this.fleetOf(fleetName);
    if (!fleet) return undefined;
    this.syncSimulations();
    const frame = fleet.update.selection;
    const target = this.effectiveTarget(fleetName);
    const dist = fleet.collector.distribution ?? 'otelcol-contrib';
    const candidates = new Set<string>();
    for (const r of this.releases) {
      if (r.distribution === dist && !r.prerelease && inFrame(frame, r.version))
        candidates.add(r.version);
    }
    for (const v of frame?.versions ?? []) candidates.add(v);
    if (target.gitVersion) candidates.add(target.gitVersion);
    if (target.selection) candidates.add(target.selection.version);
    const executor = fleet.update.executor ?? 'none';
    const blocking = this.blockingRollout(fleetName);
    let notAllowedCode: FleetVersionOptions['notAllowedCode'];
    let notAllowedReason: string | undefined;
    if (!frame?.enabled) {
      notAllowedCode = 'selection_disabled';
      notAllowedReason = 'UI-triggered updates are not enabled in Git (spec.update.selection)';
    } else if (executor === 'none') {
      notAllowedCode = 'no_executor';
      notAllowedReason = 'the fleet has no update executor';
    } else if (blocking) {
      notAllowedCode = 'blocking_rollout';
      notAllowedReason = `version rollout ${blocking.id.slice(0, 8)} is ${blocking.status}; abort it first`;
    }
    const active = this.activeVersionRollout(fleetName);
    const platforms = new Map<string, { os: string; arch: string; agents: number }>();
    for (const m of this.membersOf(fleetName)) {
      const key = `${m.agent.osType}/${m.agent.arch}`;
      const p = platforms.get(key) ?? { os: m.agent.osType, arch: m.agent.arch, agents: 0 };
      p.agents++;
      platforms.set(key, p);
    }
    return {
      fleet: fleetName,
      distribution: dist,
      allowed: notAllowedCode === undefined,
      notAllowedCode,
      notAllowedReason,
      target,
      executor,
      versionPolicy: frame?.rolloutPolicy || fleet.rollout.versionPolicy || 'default',
      activeRollout: active ? this.rolloutSummary(active.rollout) : undefined,
      options: [...candidates]
        .sort((a, b) => compareVersions(b, a))
        .map((v) => this.evaluateVersion(fleetName, v)),
      platforms: [...platforms.values()],
    };
  }

  /** What the release page shows per fleet: can this release be selected, and if not why. */
  updatableFleets(version: string): ReleaseFleetOption[] {
    return FLEETS.map((f): ReleaseFleetOption => {
      const o = this.versionOptions(f.name)!;
      const ev = this.evaluateVersion(f.name, version);
      const reasonCode = !o.allowed ? o.notAllowedCode : ev.reasonCode;
      return {
        fleet: f.name,
        displayName: f.displayName,
        target: o.target,
        selectable: o.allowed && ev.selectable,
        reasonCode,
        reason: !o.allowed ? o.notAllowedReason : ev.reason,
        agentsToUpdate: ev.agentsToUpdate,
      };
    }).sort(
      (a, b) => Number(b.selectable) - Number(a.selectable) || a.fleet.localeCompare(b.fleet),
    );
  }

  private audit422(
    actor: Actor,
    fleet: string,
    version: string,
    reason: string | undefined,
    error: string,
  ): void {
    this.recordAudit(
      actor.name,
      'fleet.version.select',
      'fleet',
      fleet,
      { version, reason, error },
      {
        result: 'failure',
      },
    );
  }

  /** POST /fleets/{name}/actions/select-version (validation, audit, rollout creation). */
  selectVersion(fleetName: string, req: SelectFleetVersionRequest, actor: Actor): SelectResult {
    const fleet = this.fleetOf(fleetName);
    if (!fleet) return { ok: false, status: 404, code: 'not_found', detail: 'fleet not found' };
    if (!/^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$/.test(req.version))
      return {
        ok: false,
        status: 400,
        code: 'bad_request',
        detail: 'version must be a semantic version without "v"',
      };
    const target = this.effectiveTarget(fleetName);
    const fail = (r: Extract<SelectResult, { ok: false }>): SelectResult => {
      this.audit422(actor, fleetName, req.version, req.reason, r.detail);
      return r;
    };
    if (
      req.expectedTargetVersion !== undefined &&
      req.expectedTargetVersion !== (target.version ?? '')
    )
      return fail({
        ok: false,
        status: 409,
        code: 'conflict',
        detail: `the target version is ${target.version ?? 'unset'} now, expected ${req.expectedTargetVersion || 'unset'}`,
      });
    const opts = this.versionOptions(fleetName)!;
    if (!opts.allowed)
      return fail({
        ok: false,
        status: 412,
        code: 'precondition_failed',
        detail: opts.notAllowedReason ?? 'version selection is not allowed',
      });
    const frame = fleet.update.selection!;
    const ev = this.evaluateVersion(fleetName, req.version);
    const issues: { severity: 'error'; code: string; message: string }[] = [];
    const issue = (code: string, message: string) =>
      issues.push({ severity: 'error', code, message });
    if (ev.reasonCode === 'prerelease_not_allowed')
      issue('prerelease_not_allowed', `${req.version} is a pre-release`);
    else if (ev.reasonCode === 'unknown_version')
      issue('unknown_version', `${req.version} is not a known release`);
    else if (ev.reasonCode === 'outside_frame')
      issue('version_not_allowed', `${req.version} is outside ${describeFrame(frame)}`);
    else if (ev.reasonCode === 'downgrade_not_allowed')
      issue(
        'downgrade_not_allowed',
        `${req.version} is lower than the current target ${target.version}`,
      );
    if (frame.requireReason && !(req.reason ?? '').trim())
      issue('reason_required', 'a reason is required by the frame');
    if (issues.length > 0)
      return fail({
        ok: false,
        status: 422,
        code: 'validation_failed',
        detail: 'the version cannot be selected',
        issues,
      });
    if (ev.reasonCode === 'current_target')
      return {
        ok: true,
        response: {
          target,
          planned: false,
          message: `${req.version} already is the target version`,
        },
      };

    const nowIso = new Date(this.now()).toISOString();
    const sel: FleetVersionSelection = {
      fleet: fleetName,
      version: req.version,
      state: 'active',
      gitVersion: target.gitVersion,
      gitCommit: COMMIT_APPLIED,
      selectedBy: actor.name,
      selectedAt: nowIso,
      reason: req.reason || undefined,
      updatedAt: nowIso,
    };
    this.selections.set(fleetName, sel);
    this.recordAudit(actor.name, 'fleet.version.select', 'fleet', fleetName, {
      version: req.version,
      reason: req.reason,
      previousVersion: target.version,
      previousSource: target.source,
      gitVersion: target.gitVersion,
    });
    this.applyTargetVersions();
    this.emit('fleet.updated', { name: fleetName });
    this.emit('stats.changed', {});

    if (this.deferPlanning) {
      setTimeout(() => this.planSelection(fleetName), this.deferPlanMs);
      return { ok: true, response: { target: this.effectiveTarget(fleetName), planned: false } };
    }
    const planned = this.planSelection(fleetName);
    return {
      ok: true,
      response: {
        target: this.effectiveTarget(fleetName),
        planned: true,
        rolloutId: planned?.rollout.id,
        targets: planned?.targets.length,
        message: planned ? undefined : 'no collector of the fleet needs an update',
      },
    };
  }

  /** The leader's part: supersede the running version rollout and create the one for the selection. */
  private planSelection(fleetName: string): MockRollout | undefined {
    const sel = this.selections.get(fleetName);
    const fleet = this.fleetOf(fleetName);
    if (!sel || sel.state !== 'active' || !fleet) return undefined;
    const old = this.activeVersionRollout(fleetName);
    if (old && old.rollout.toRef !== sel.version) {
      old.sim = undefined;
      for (const t of old.targets)
        if (['pending', 'waiting_window', 'in_progress', 'verifying'].includes(t.status))
          t.status = 'cancelled';
      this.recount(old);
      this.setRolloutStatus(old, 'superseded', 'the fleet target version was changed');
    }
    const mr = this.createUiRollout(fleet, sel);
    const nowIso = new Date(this.now()).toISOString();
    sel.plannedAt = nowIso;
    sel.rolloutId = mr?.rollout.id;
    sel.updatedAt = nowIso;
    this.emit('fleet.updated', { name: fleetName });
    return mr;
  }

  private createUiRollout(fleet: Fleet, sel: FleetVersionSelection): MockRollout | undefined {
    const needing = this.membersOf(fleet.name)
      .filter((e) => e.agent.version !== sel.version)
      .sort(
        (a, b) =>
          // collectors that really are behind come first, online ones before offline ones
          Number(compareVersions(a.agent.version, sel.version) >= 0) -
            Number(compareVersions(b.agent.version, sel.version) >= 0) ||
          Number(b.agent.connection.status === 'online') -
            Number(a.agent.connection.status === 'online') ||
          a.agent.hostname.localeCompare(b.agent.hostname, 'en', { numeric: true }),
      )
      .slice(0, MOCK_MAX_TARGETS);
    if (needing.length === 0) return undefined;
    const frame = fleet.update.selection;
    const policyName = frame?.rolloutPolicy || fleet.rollout.versionPolicy || 'default';
    const policy = POLICIES.find((p) => p.name === policyName) ?? POLICIES[0]!;
    const rollout: Rollout = {
      id: this.rng.uuid(),
      kind: 'version',
      fleetName: fleet.name,
      policyName: policy.name,
      // The simulated health wait lasts one step (see updateSim.ts): the snapshot says so, and the
      // countdown of the rollout page counts down the same two seconds.
      policy: { ...policy, healthWait: formatWait(STEP_MS) },
      status: 'running',
      fromRef: needing[0]!.agent.version,
      toRef: sel.version,
      executor: fleet.update.executor,
      currentWave: 0,
      waves: policy.waves.map((w, index) => ({
        index,
        name: w.name,
        selector: w.selector,
        percent: w.percent,
        requireApproval: !!w.requireApproval,
        // this mock rolls out to a single collector: later waves have nothing to do
        status: index === 0 ? 'running' : 'skipped',
        startedAt: index === 0 ? new Date(this.now()).toISOString() : undefined,
        counts: {
          total: 0,
          pending: 0,
          waitingWindow: 0,
          inProgress: 0,
          succeeded: 0,
          failed: 0,
          skipped: 0,
          cancelled: 0,
          rolledBack: 0,
        },
      })),
      counts: {
        total: 0,
        pending: 0,
        waitingWindow: 0,
        inProgress: 0,
        succeeded: 0,
        failed: 0,
        skipped: 0,
        cancelled: 0,
        rolledBack: 0,
      },
      createdBy: sel.selectedBy,
      origin: 'ui',
      requestReason: sel.reason,
      createdAt: new Date(this.now()).toISOString(),
      startedAt: new Date(this.now()).toISOString(),
      updatedAt: new Date(this.now()).toISOString(),
      annotations: {
        distribution: fleet.collector.distribution ?? 'otelcol-contrib',
        ...(fleet.update.executorRef ? { executorRef: fleet.update.executorRef } : {}),
        ...(fleet.update.params ? { updateParams: fleet.update.params } : {}),
        selection: {
          version: sel.version,
          selectedBy: sel.selectedBy,
          selectedAt: sel.selectedAt,
          reason: sel.reason,
        },
        ackedFailures: [],
        rollbackSent: false,
      },
    };
    const script: UpdateScript = /rollback/i.test(sel.reason ?? '') ? 'rollback' : 'success';
    const startedAt = this.now();
    const targets: RolloutTarget[] = needing.map((e) => ({
      rolloutId: rollout.id,
      agentId: e.agent.id,
      hostname: e.agent.hostname,
      wave: 0,
      status: 'in_progress',
      fromRef: e.agent.version,
      toRef: sel.version,
      attempts: 1,
      startedAt: new Date(startedAt).toISOString(),
      executorRef: e.agent.updater?.agentId,
      updatedAt: new Date(startedAt).toISOString(),
    }));
    const mr: MockRollout = {
      rollout,
      targets,
      sim: { agentId: targets[0]!.agentId, script, startedAt, attempt: 1 },
      simPlan: { agentId: targets[0]!.agentId, from: rollout.fromRef!, to: sel.version },
    };
    this.rollouts.push(mr);
    this.recount(mr);
    this.syncSimulations();
    this.emit('rollout.updated', this.rolloutSummary(rollout));
    this.ensureSimTimer();
    this.recordAudit(sel.selectedBy, 'rollout.create', 'rollout', rollout.id, {
      origin: 'ui',
      selectedBy: sel.selectedBy,
      reason: sel.reason,
    });
    return mr;
  }

  /** POST /fleets/{name}/actions/clear-version-selection */
  clearSelection(
    fleetName: string,
    reason: string | undefined,
    actor: Actor,
  ): { ok: true; target: FleetVersionTarget } | Extract<SelectResult, { ok: false }> {
    const fleet = this.fleetOf(fleetName);
    if (!fleet) return { ok: false, status: 404, code: 'not_found', detail: 'fleet not found' };
    const sel = this.selections.get(fleetName);
    if (!sel) return { ok: true, target: this.effectiveTarget(fleetName) };
    if (fleet.update.selection?.requireReason && !(reason ?? '').trim())
      return {
        ok: false,
        status: 422,
        code: 'validation_failed',
        detail: 'a reason is required by the frame',
        issues: [
          {
            severity: 'error',
            code: 'reason_required',
            message: 'a reason is required by the frame',
          },
        ],
      };
    this.selections.delete(fleetName);
    this.recordAudit(actor.name, 'fleet.version.clear', 'fleet', fleetName, {
      version: sel.version,
      reason,
      gitVersion: fleet.collector.version,
    });
    const old = this.activeVersionRollout(fleetName);
    if (old && old.rollout.origin === 'ui' && old.rollout.toRef === sel.version) {
      old.sim = undefined;
      for (const t of old.targets)
        if (['pending', 'waiting_window', 'in_progress', 'verifying'].includes(t.status))
          t.status = 'cancelled';
      this.recount(old);
      this.setRolloutStatus(old, 'superseded', 'the UI selection was cleared');
    }
    this.applyTargetVersions();
    this.emit('fleet.updated', { name: fleetName });
    this.emit('stats.changed', {});
    return { ok: true, target: this.effectiveTarget(fleetName) };
  }

  /* ------------------------------------------------------------------ update simulation */

  private ensureSimTimer(): void {
    if (this.simTimer) return;
    this.simTimer = setInterval(() => {
      this.syncSimulations();
      if (!this.rollouts.some((r) => r.sim)) this.dispose();
    }, 1000);
  }

  /** Advances every running simulated update to `now()` and emits the live events for changes. */
  syncSimulations(): void {
    const now = this.now();
    for (const mr of this.rollouts) {
      const sim = mr.sim;
      if (!sim) continue;
      const t = mr.targets.find((x) => x.agentId === sim.agentId);
      const plan = mr.simPlan;
      if (!t || !plan) continue;
      const res = simulateUpdate({
        startedAt: sim.startedAt,
        now,
        script: sim.script,
        from: plan.from,
        to: plan.to,
        attempt: sim.attempt,
      });
      const before = JSON.stringify([t.progress, t.status]);
      t.progress = res.progress;
      t.status = res.targetStatus;
      // like the engine: the collector reported the version when the step health_wait began
      const waitStart = res.progress.steps.find((s) => s.name === 'health_wait')?.startedAt;
      if (res.targetStatus === 'verifying' || res.targetStatus === 'succeeded')
        t.verifiedAt = waitStart ?? t.verifiedAt;
      t.updatedAt = new Date(now).toISOString();
      t.attempts = sim.attempt;
      if (JSON.stringify([t.progress, t.status]) === before) continue;
      if (res.final) {
        mr.sim = undefined;
        const finishedAt = new Date(now).toISOString();
        t.finishedAt = finishedAt;
        const wave = mr.rollout.waves[t.wave];
        const e = this.entries.get(t.agentId);
        if (res.targetStatus === 'succeeded') {
          t.verifiedAt ??= finishedAt;
          t.lastError = undefined;
          if (e) {
            e.agent.version = plan.to;
            e.agent.identifyingAttributes['service.version'] = plan.to;
            this.addEvent(e.agent.id, {
              type: 'version_changed',
              severity: 'info',
              message: `Version changed to ${plan.to}`,
              details: { from: plan.from, to: plan.to },
            });
            this.emit('agent.updated', summarize(e, this.latestVersion()));
          }
          if (wave) {
            wave.status = 'completed';
            wave.finishedAt = finishedAt;
          }
          this.recount(mr);
          this.setRolloutStatus(mr, 'completed');
        } else {
          t.lastError = res.progress.error;
          if (wave) {
            wave.status = 'failed';
            wave.finishedAt = finishedAt;
          }
          this.recount(mr);
          this.setRolloutStatus(
            mr,
            'paused',
            'failure threshold reached: 1 target failed (onFailure: pause)',
          );
        }
        this.emit('fleet.updated', { name: mr.rollout.fleetName });
      } else {
        this.recount(mr);
        this.emit('rollout.updated', this.rolloutSummary(mr.rollout));
      }
    }
  }

  /** "Retry failed" of a simulated update: the second attempt succeeds. */
  restartSimulation(mr: MockRollout): void {
    const plan = mr.simPlan;
    if (!plan) return;
    const t = mr.targets.find((x) => x.agentId === plan.agentId);
    if (!t || t.status !== 'pending') return;
    t.status = 'in_progress';
    t.lastError = undefined;
    t.finishedAt = undefined;
    t.verifiedAt = undefined;
    const attempt = (mr.sim?.attempt ?? t.attempts) + 1;
    mr.sim = { agentId: plan.agentId, script: 'success', startedAt: this.now(), attempt };
    const wave = mr.rollout.waves[t.wave];
    if (wave) {
      wave.status = 'running';
      wave.finishedAt = undefined;
    }
    this.syncSimulations();
    this.ensureSimTimer();
  }

  /**
   * Targets as the API returns them: simulated ones carry live progress; the fixtures of the older
   * rollouts get a static snapshot derived from their status; bulk-cancelled targets are normalized.
   */
  targetsOf(mr: MockRollout): RolloutTarget[] {
    this.syncSimulations();
    if (mr.rollout.kind !== 'version') return mr.targets;
    return mr.targets.map((t) => {
      const progress = t.progress ?? this.staticProgress(t);
      if (!progress) return t;
      return { ...t, progress: this.normalizeProgress(t, progress) };
    });
  }

  private staticProgress(t: RolloutTarget): TargetProgress | undefined {
    if (!t.startedAt || !t.fromRef) return undefined;
    const startedAt = Date.parse(t.startedAt);
    const base = { startedAt, from: t.fromRef, to: t.toRef, attempt: Math.max(1, t.attempts) };
    switch (t.status) {
      case 'succeeded':
        return simulateUpdate({ ...base, now: startedAt + 99 * STEP_MS, script: 'success' })
          .progress;
      case 'failed':
        return simulateUpdate({ ...base, now: startedAt + 99 * STEP_MS, script: 'verify_fail' })
          .progress;
      case 'in_progress':
      case 'verifying': {
        // The status has the last word (docs/design/version-selection.md, Target progress): the
        // health wait runs exactly while the target is verifying.
        const stage = t.status === 'verifying' ? 5 : hashString(t.agentId) % 5;
        const progress = simulateUpdate({
          ...base,
          now: startedAt + stage * STEP_MS + STEP_MS / 2,
          script: 'success',
        }).progress;
        const verifiedAt = t.verifiedAt;
        if (t.status !== 'verifying' || !verifiedAt) return progress;
        // the engine starts the step with the time the collector reported the version
        return {
          ...progress,
          steps: progress.steps.map((st) =>
            st.name === 'health_wait' ? { ...st, startedAt: verifiedAt } : st,
          ),
        };
      }
      default:
        return undefined;
    }
  }

  /** Mirrors the read API: a final target status with a non-final phase gets a consistent phase. */
  private normalizeProgress(t: RolloutTarget, p: TargetProgress): TargetProgress {
    const finalStatus = ['succeeded', 'failed', 'skipped', 'cancelled', 'rolled_back'].includes(
      t.status,
    );
    const finalPhase = ['succeeded', 'failed', 'rolled_back', 'cancelled'].includes(p.phase);
    if (!finalStatus || finalPhase) return p;
    const phase =
      t.status === 'cancelled' || t.status === 'skipped'
        ? 'cancelled'
        : t.status === 'rolled_back'
          ? 'rolled_back'
          : 'failed';
    return {
      ...p,
      phase,
      steps: p.steps.map((s) =>
        s.status === 'running' || s.status === 'pending'
          ? { ...s, status: phase === 'failed' && s.status === 'running' ? 'failed' : 'skipped' }
          : s,
      ),
    };
  }

  /** Counts of all sync states (used by tests). */
  syncCounts(): Map<ConfigSyncState, number> {
    const m = new Map<ConfigSyncState, number>();
    for (const e of this.entries.values()) m.set(e.sync, (m.get(e.sync) ?? 0) + 1);
    return m;
  }

  agentOf(id: string): Agent | undefined {
    return this.entries.get(id)?.agent;
  }
}

let instance: MockDb | null = null;
export function getDb(): MockDb {
  if (!instance) instance = new MockDb();
  return instance;
}
export function resetDb(): MockDb {
  instance?.dispose();
  instance = new MockDb();
  return instance;
}
