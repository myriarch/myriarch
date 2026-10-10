// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/** Deterministic agent fleet generator for mock mode (1,000 collectors + a sample of updaters). */
import type {
  Agent,
  AgentEvent,
  AgentHealth,
  AgentSummary,
  ComponentHealth,
  ComponentSet,
  ConfigSyncState,
  Fleet,
} from '@/api/types';
import { ATTR_COLLECTOR_INSTANCE_UID, UPDATER_BINARY } from '@/brand';
import { compareVersions } from '@/lib/format';
import { fakeHash, fleetByName, renderMock } from './render';
import { Rng, hashString } from './rng';
import { DAY, HOUR, iso, LATEST_VERSION, MIN } from './static';

export const CAPABILITY_BITS: [string, number][] = [
  ['ReportsStatus', 0x1],
  ['AcceptsRemoteConfig', 0x2],
  ['ReportsEffectiveConfig', 0x4],
  ['AcceptsPackages', 0x8],
  ['ReportsPackageStatuses', 0x10],
  ['ReportsOwnTraces', 0x20],
  ['ReportsOwnMetrics', 0x40],
  ['ReportsOwnLogs', 0x80],
  ['AcceptsOpAMPConnectionSettings', 0x100],
  ['AcceptsOtherConnectionSettings', 0x200],
  ['AcceptsRestartCommand', 0x400],
  ['ReportsHealth', 0x800],
  ['ReportsRemoteConfig', 0x1000],
  ['ReportsHeartbeat', 0x2000],
  ['ReportsAvailableComponents', 0x4000],
];

export function capabilityNames(bits: number): string[] {
  return CAPABILITY_BITS.filter(([, b]) => (bits & b) !== 0).map(([n]) => n);
}

const COLLECTOR_CAPS = 0x1 | 0x2 | 0x4 | 0x40 | 0x400 | 0x800 | 0x1000 | 0x2000 | 0x4000;
const UPDATER_CAPS = 0x1 | 0x8 | 0x10 | 0x800;

const REGIONS = ['north', 'east', 'south', 'west'];
const DC_SITES = [
  'North',
  'South',
  'West',
  'East',
  'Central',
  'Harbor',
  'Ridge',
  'Valley',
  'Plains',
  'Coast',
  'Alpine',
  'Lakes',
];
const OFFICE_ROLES = ['app', 'db', 'web', 'ldap', 'mq', 'cache', 'batch', 'proxy', 'search', 'api'];
const GW_REGIONS = ['nl-north', 'nl-south', 'nl-east', 'nl-west', 'be-north', 'be-south'];

export interface GeneratedFleet {
  fleet: string;
  agents: Agent[];
}

interface Site {
  id: string;
  name: string;
  region: string;
  country: string;
  type: string;
  tz: string;
}

/**
 * Generates `count` edge sites with random but unique four-digit ids. Ids in `reserved` are used for
 * the first sites, so the example inventory (sites 1001/1002/1003 in windows-hosts) matches generated agents.
 */
function edgeSites(
  rng: Rng,
  count: number,
  country: string,
  tz: string,
  used: Set<string>,
  reserved: string[] = [],
): Site[] {
  const sites: Site[] = [];
  for (const id of reserved) used.add(id);
  for (let i = 0; i < count; i++) {
    let id = reserved[i];
    if (id === undefined) {
      id = String(rng.int(1000, 9899)).padStart(4, '0');
      while (used.has(id)) id = String(rng.int(1000, 9899)).padStart(4, '0');
      used.add(id);
    }
    sites.push({
      id,
      name: `Site ${id}`,
      region: REGIONS[i % REGIONS.length]!,
      country,
      type: 'edge',
      tz,
    });
  }
  return sites;
}

function pickVersion(rng: Rng, weights: [string, number][]): string {
  return rng.weighted(weights);
}

function componentTree(
  rng: Rng,
  healthy: boolean,
  failing: 'exporter' | 'receiver' | 'none',
  now: number,
): Record<string, ComponentHealth> {
  const t = (offset: number) => iso(offset);
  const ok = (extra: Partial<ComponentHealth> = {}): ComponentHealth => ({
    healthy: true,
    status: 'StatusOK',
    statusTime: t(-rng.int(5, 300) * MIN),
    ...extra,
  });
  const pipelines: Record<string, ComponentHealth> = {};
  for (const sig of ['traces', 'metrics', 'logs']) {
    const exporterBad = !healthy && failing === 'exporter' && sig !== 'traces';
    const receiverBad = !healthy && failing === 'receiver' && sig === 'logs';
    const exporter: ComponentHealth = exporterBad
      ? {
          healthy: false,
          status: 'StatusRecoverableError',
          lastError:
            'rpc error: code = Unavailable desc = connection error: desc = "transport: Error while dialing: dial tcp 10.20.0.14:4317: i/o timeout"',
          statusTime: t(-rng.int(1, 20) * MIN),
        }
      : ok();
    const receiver: ComponentHealth = receiverBad
      ? {
          healthy: false,
          status: 'StatusPermanentError',
          lastError:
            'failed to open file C:\\ProgramData\\EdgeApp\\logs\\app.log: The process cannot access the file because it is being used by another process.',
          statusTime: t(-rng.int(1, 60) * MIN),
        }
      : ok();
    pipelines[`pipeline:${sig}`] = {
      healthy: !exporterBad && !receiverBad,
      status: exporterBad || receiverBad ? 'StatusRecoverableError' : 'StatusOK',
      statusTime: t(-rng.int(1, 300) * MIN),
      components: {
        'receiver:otlp': ok(),
        ...(sig === 'logs' ? { 'receiver:file_log/app': receiver } : {}),
        'processor:memory_limiter': ok(),
        'processor:batch': ok(),
        'exporter:otlp_grpc/gateway': exporter,
      },
    };
  }
  void now;
  return {
    extensions: {
      healthy: true,
      status: 'StatusOK',
      statusTime: t(-DAY),
      components: { 'extension:health_check': ok(), 'extension:file_storage/queue': ok() },
    },
    ...pipelines,
  };
}

function makeHealth(rng: Rng, kind: 'ok' | 'bad' | 'none', now: number): AgentHealth | undefined {
  if (kind === 'none') return undefined;
  const bad = kind === 'bad';
  const failing = bad ? (rng.chance(0.75) ? 'exporter' : 'receiver') : 'none';
  return {
    healthy: !bad,
    status: bad ? 'StatusRecoverableError' : 'StatusOK',
    lastError: bad
      ? failing === 'exporter'
        ? 'exporter otlp_grpc/gateway: connection refused'
        : 'receiver file_log/app: access denied'
      : undefined,
    startTime: iso(-rng.int(1, 40) * DAY),
    components: componentTree(rng, !bad, failing, now),
    updatedAt: iso(-rng.int(10, 600) * 1000),
  };
}

export interface MockAgentEntry {
  agent: Agent;
  sync: ConfigSyncState;
}

export interface AgentPlan {
  version?: string;
  pilot?: boolean;
}

export function generateAgents(): { agents: MockAgentEntry[]; byFleet: Map<string, string[]> } {
  const rng = new Rng(20261006);
  const now = Date.now();
  const entries: MockAgentEntry[] = [];
  const byFleet = new Map<string, string[]>();

  const track = (fleet: string, id: string) => {
    const list = byFleet.get(fleet) ?? [];
    list.push(id);
    byFleet.set(fleet, list);
  };

  const build = (
    fleet: Fleet | undefined,
    host: string,
    site: Site,
    os: 'windows' | 'linux',
    version: string,
    opts: { pilot?: boolean; updater?: boolean } = {},
  ): MockAgentEntry => {
    const id = rng.uuid();
    const online = rng.chance(0.945);
    const healthKind: 'ok' | 'bad' | 'none' = !online
      ? rng.chance(0.5)
        ? 'ok'
        : 'none'
      : rng.weighted([
          ['ok', 94],
          ['bad', 4.5],
          ['none', 1.5],
        ] as const);
    const lastSeen = online ? now - rng.int(1, 55) * 1000 : now - rng.int(20, 60 * 36) * MIN;
    const firstSeen = now - rng.int(20, 400) * DAY;
    let sync: ConfigSyncState;
    if (!fleet) sync = 'none';
    else if (!online) sync = rng.chance(0.7) ? 'in_sync' : 'pending';
    else
      sync = rng.weighted([
        ['in_sync', 93],
        ['pending', 3.5],
        ['failed', 1.8],
        ['drift', 1.7],
      ] as const);

    const rev = (n: number) => fakeHash(`${fleet?.name ?? 'none'}:rev${n}:${site.id}`);
    const desired = fleet ? rev(2) : undefined;
    const assigned = fleet ? (sync === 'drift' ? rev(1) : rev(2)) : undefined;
    const failedHash = sync === 'failed' ? desired : undefined;
    const remoteStatus = !fleet
      ? 'unset'
      : sync === 'failed'
        ? 'failed'
        : sync === 'pending'
          ? online
            ? 'applying'
            : 'applied'
          : 'applied';
    const arch = os === 'windows' ? 'amd64' : fleet?.name === 'k8s-gateway' ? 'arm64' : 'amd64';
    const identifying = {
      'service.name': 'otelcol-contrib',
      'service.version': version,
      'service.instance.id': id,
    };
    const nonIdentifying: Record<string, string> = {
      'os.type': os,
      'os.description':
        os === 'windows' ? 'Windows 11 IoT Enterprise LTSC 24H2' : 'Ubuntu 24.04.3 LTS',
      'host.name': host,
      'host.arch': arch,
      'site.type': site.type,
      'site.id': site.id,
      'site.name': site.name,
      'site.country': site.country,
      'site.region': site.region,
      'site.timezone': site.tz,
    };
    const labels: Record<string, string> = {};
    if (opts.pilot) labels['pilot'] = 'true';
    const agent: Agent = {
      id,
      kind: 'collector',
      serviceName: 'otelcol-contrib',
      version,
      hostname: host,
      osType: os,
      osDescription: nonIdentifying['os.description']!,
      arch,
      identifyingAttributes: identifying,
      nonIdentifyingAttributes: nonIdentifying,
      labels,
      capabilities: COLLECTOR_CAPS,
      connection: {
        status: online ? 'online' : 'offline',
        replicaId: online ? `myriarchd-${rng.int(0, 2)}` : undefined,
        transport: online ? 'websocket' : undefined,
        remoteAddr: `10.${rng.int(10, 99)}.${rng.int(0, 250)}.${rng.int(2, 250)}:${rng.int(30000, 60000)}`,
        connectedAt: online ? iso(-rng.int(1, 3000) * MIN) : undefined,
        disconnectedAt: online ? undefined : iso(lastSeen - now),
        lastSeenAt: iso(lastSeen - now),
      },
      health: makeHealth(rng, healthKind, now),
      remoteConfig: {
        status: remoteStatus,
        lastHash: remoteStatus === 'failed' ? failedHash : assigned,
        errorMessage:
          remoteStatus === 'failed'
            ? 'cannot start pipelines: failed to build pipelines: failed to create "file_log/app" receiver for data type "logs": invalid include pattern "D:\\EdgeApp\\journal\\*.log": directory does not exist'
            : undefined,
        updatedAt: iso(-rng.int(1, 5000) * MIN),
      },
      assignment: fleet
        ? {
            fleetName: fleet.name,
            assignedConfigHash: assigned,
            desiredConfigHash: desired,
            failedConfigHash: failedHash,
            targetVersion: fleet.collector.version,
            error: undefined,
            assignedAt: iso(-rng.int(2, 300) * HOUR),
          }
        : {},
      effectiveConfigHash: online || rng.chance(0.5) ? fakeHash(`effective:${id}`) : undefined,
      effectiveConfigUpdatedAt: iso(-rng.int(1, 5000) * MIN),
      availableComponentsHash: fakeHash(`components:${version}`),
      firstSeenAt: iso(firstSeen - now),
      updatedAt: iso(lastSeen - now),
    };
    return { agent, sync };
  };

  const FLEET = (n: string) => fleetByName(n)!;
  const add = (fleetName: string, e: MockAgentEntry) => {
    entries.push(e);
    track(fleetName, e.agent.id);
  };

  // --- windows-hosts: 120 sites x 4 nodes = 480
  const usedSiteIds = new Set<string>();
  {
    const f = FLEET('windows-hosts');
    const sites = edgeSites(rng, 120, 'NL', 'Europe/Amsterdam', usedSiteIds, [
      '1001',
      '1002',
      '1003',
    ]);
    sites.forEach((site, si) => {
      const pilot = si < 12;
      for (let n = 1; n <= 4; n++) {
        const v = pilot
          ? '0.162.0'
          : pickVersion(rng, [
              ['0.162.0', 3],
              ['0.161.0', 72],
              ['0.160.0', 20],
              ['0.163.0', 5],
            ]);
        add('windows-hosts', build(f, `edge-${site.id}-0${n}`, site, 'windows', v, { pilot }));
      }
    });
  }
  // --- edge-sites: 60 sites x 4 nodes = 240
  {
    const f = FLEET('edge-sites');
    const sites = edgeSites(rng, 60, 'BE', 'Europe/Brussels', usedSiteIds, ['2001']);
    sites.forEach((site) => {
      for (let n = 1; n <= 4; n++) {
        const v = pickVersion(rng, [
          ['0.161.0', 96],
          ['0.160.0', 4],
        ]);
        add('edge-sites', build(f, `edge-${site.id}-0${n}`, site, 'windows', v));
      }
    });
  }
  // --- linux-hosts: 120 servers
  {
    const f = FLEET('linux-hosts');
    const site: Site = {
      id: 'OFFICE1',
      name: 'Office 1',
      region: 'north',
      country: 'NL',
      type: 'office',
      tz: 'Europe/Amsterdam',
    };
    for (let n = 0; n < 120; n++) {
      const role = OFFICE_ROLES[n % OFFICE_ROLES.length]!;
      const v = pickVersion(rng, [
        ['0.162.0', 82],
        ['0.161.0', 12],
        ['0.160.0', 3],
        ['0.163.0', 3],
      ]);
      add(
        'linux-hosts',
        build(
          f,
          `office-${role}-${String(Math.floor(n / OFFICE_ROLES.length) + 1).padStart(2, '0')}`,
          site,
          'linux',
          v,
        ),
      );
    }
  }
  // --- datacenter: 12 sites x 10 = 120 ; version rollout in progress (see db.ts)
  {
    const f = FLEET('datacenter');
    DC_SITES.forEach((name, di) => {
      const site: Site = {
        id: `DC-${name.toUpperCase().replace(/[^A-Z]/g, '')}`,
        name: `Data center ${name}`,
        region: REGIONS[di % REGIONS.length]!,
        country: di >= 10 ? 'BE' : 'NL',
        type: 'datacenter',
        tz: di >= 10 ? 'Europe/Brussels' : 'Europe/Amsterdam',
      };
      for (let n = 1; n <= 10; n++) {
        add(
          'datacenter',
          build(
            f,
            `dc-${name.toLowerCase().replace(/[^a-z]/g, '')}-${String(n).padStart(2, '0')}`,
            site,
            'linux',
            '0.161.0',
          ),
        );
      }
    });
  }
  // --- k8s-gateway: 6 regions, 7 pods in the first four and 6 in the last two = 40
  {
    const f = FLEET('k8s-gateway');
    GW_REGIONS.forEach((region, ri) => {
      const site: Site = {
        id: `gw-${region}`,
        name: `Gateway ${region}`,
        region: region.split('-')[1]!,
        country: region.startsWith('be') ? 'BE' : 'NL',
        type: 'k8s-gateway',
        tz: 'UTC',
      };
      for (let n = 0; n < (ri < 4 ? 7 : 6); n++) {
        const v = pickVersion(rng, [
          ['0.162.0', 85],
          ['0.161.0', 12],
          ['0.163.0', 3],
        ]);
        const e = build(f, `otel-gw-${region}-${n}`, site, 'linux', v);
        e.agent.nonIdentifyingAttributes['k8s.namespace.name'] = 'observability';
        e.agent.nonIdentifyingAttributes['k8s.pod.name'] = `otel-gw-${region}-${n}`;
        add('k8s-gateway', e);
      }
    });
  }
  // --- 4 collectors that match no fleet (lab, edge site in a country without a fleet)
  const lab: Site[] = [
    {
      id: '9001',
      name: 'Development lab',
      region: 'north',
      country: 'NL',
      type: 'lab',
      tz: 'Europe/Amsterdam',
    },
    {
      id: '8001',
      name: 'Site 8001',
      region: 'west',
      country: 'CH',
      type: 'edge',
      tz: 'Europe/Zurich',
    },
  ];
  for (let n = 0; n < 4; n++) {
    const site = lab[n % 2]!;
    const e = build(
      undefined,
      n < 2 ? `lab-collector-0${n + 1}` : `edge-${site.id}-0${n - 1}`,
      site,
      n < 2 ? 'linux' : 'windows',
      '0.162.0',
    );
    entries.push(e);
  }

  // --- myriarch-updater agents for a sample of updater-managed collectors (every 10th)
  const updaterFleets = new Set(['windows-hosts', 'edge-sites', 'datacenter']);
  for (const e of [...entries]) {
    const fleetName = e.agent.assignment.fleetName;
    if (!fleetName || !updaterFleets.has(fleetName)) continue;
    if (hashString(e.agent.id) % 10 !== 0) continue;
    const uid = rng.uuid();
    const online = e.agent.connection.status === 'online' && rng.chance(0.97);
    const target = fleetByName(fleetName)?.collector.version;
    const updater: Agent = {
      id: uid,
      kind: 'updater',
      serviceName: UPDATER_BINARY,
      version: '0.1.0-alpha.1',
      hostname: e.agent.hostname,
      osType: e.agent.osType,
      osDescription: e.agent.osDescription,
      arch: e.agent.arch,
      identifyingAttributes: {
        'service.name': UPDATER_BINARY,
        'service.version': '0.1.0-alpha.1',
        'service.instance.id': uid,
      },
      nonIdentifyingAttributes: {
        ...e.agent.nonIdentifyingAttributes,
        [ATTR_COLLECTOR_INSTANCE_UID]: e.agent.id,
      },
      labels: {},
      capabilities: UPDATER_CAPS,
      connection: {
        ...e.agent.connection,
        status: online ? 'online' : 'offline',
        transport: online ? 'websocket' : undefined,
      },
      health: {
        healthy: online,
        status: online ? 'StatusOK' : 'StatusStopped',
        updatedAt: iso(-rng.int(5, 120) * 1000),
      },
      remoteConfig: { status: 'unset' },
      assignment: {},
      updater: { agentId: e.agent.id, online: e.agent.connection.status === 'online' },
      packageStatus: {
        packages: {
          '': {
            name: '',
            agentHasVersion: `v${e.agent.version}`,
            serverOfferedVersion: target ? `v${target}` : undefined,
            status: 'installed',
          },
        },
        updatedAt: iso(-rng.int(5, 500) * MIN),
      },
      firstSeenAt: e.agent.firstSeenAt,
      updatedAt: e.agent.updatedAt,
    };
    e.agent.updater = { agentId: uid, online };
    entries.push({ agent: updater, sync: 'none' });
  }

  return { agents: entries, byFleet };
}

/** Summary view, as returned by listAgents and carried by agent.updated events. */
export function summarize(e: MockAgentEntry, latest: string = LATEST_VERSION): AgentSummary {
  const a = e.agent;
  const site: Record<string, string> = {};
  for (const [k, v] of Object.entries(a.nonIdentifyingAttributes))
    if (k.startsWith('site.')) site[k] = v;
  const hasUpdater = a.kind === 'collector' && !!a.updater;
  return {
    id: a.id,
    kind: a.kind,
    hostname: a.hostname,
    serviceName: a.serviceName,
    version: a.version,
    outdated: a.kind === 'collector' && compareVersions(a.version, latest) < 0,
    osType: a.osType,
    arch: a.arch,
    status: a.connection.status,
    lastSeenAt: a.connection.lastSeenAt,
    healthy: a.health?.healthy,
    healthStatus: a.health?.status,
    fleetName: a.assignment.fleetName,
    configSync: e.sync,
    targetVersion: a.assignment.targetVersion,
    site: Object.keys(site).length ? site : undefined,
    labels: Object.keys(a.labels).length ? a.labels : undefined,
    hasUpdater,
  };
}

/* ------------------------------------------------------------------ lazily generated details */

const KIND_COMPONENTS: Record<string, string[]> = {
  receivers: [
    'otlp',
    'prometheus',
    'hostmetrics',
    'filelog',
    'journald',
    'windowseventlog',
    'kafka',
    'syslog',
    'k8s_cluster',
    'k8sobjects',
    'filestats',
    'jaeger',
    'zipkin',
  ],
  processors: [
    'batch',
    'memory_limiter',
    'resourcedetection',
    'resource',
    'attributes',
    'transform',
    'filter',
    'k8sattributes',
    'tail_sampling',
    'probabilistic_sampler',
    'groupbyattrs',
    'cumulativetodelta',
  ],
  exporters: [
    'otlp',
    'otlphttp',
    'debug',
    'file',
    'kafka',
    'prometheus',
    'loadbalancing',
    'awss3',
    'elasticsearch',
  ],
  extensions: [
    'health_check',
    'file_storage',
    'pprof',
    'zpages',
    'bearertokenauth',
    'oauth2client',
    'opamp',
  ],
  connectors: ['spanmetrics', 'forward', 'routing', 'count'],
};

export function componentSet(agent: Agent): ComponentSet {
  const rng = new Rng(hashString(`components:${agent.version}:${agent.osType}`));
  const components: ComponentSet['components'] = {};
  for (const [kind, types] of Object.entries(KIND_COMPONENTS)) {
    const byType: Record<string, { module?: string; metadata?: Record<string, string> }> = {};
    for (const t of types) {
      if (t === 'journald' && agent.osType === 'windows') continue;
      if (t === 'windowseventlog' && agent.osType !== 'windows') continue;
      const singular = kind.slice(0, -1);
      byType[t] = {
        module: `github.com/open-telemetry/opentelemetry-collector-contrib/${singular}/${t}${t === 'otlp' && kind !== 'exporters' ? 'receiver' : ''} v${agent.version}`,
        metadata: { stability: rng.pick(['stable', 'beta', 'beta', 'alpha']) },
      };
    }
    components[kind] = byType;
  }
  return { hash: fakeHash(`components:${agent.version}:${agent.osType}`), components };
}

export function generateEvents(agent: Agent, baseId: number): AgentEvent[] {
  const rng = new Rng(hashString(`events:${agent.id}`));
  const events: AgentEvent[] = [];
  let t = Date.now() - rng.int(2, 30) * MIN;
  const n = rng.int(14, 60);
  type Spec = Pick<AgentEvent, 'type' | 'severity' | 'message'> & {
    details?: Record<string, unknown>;
  };
  const make = (): Spec => {
    const r = rng.next();
    if (r < 0.2)
      return {
        type: 'connected',
        severity: 'info',
        message: `Connected via websocket from ${agent.connection.remoteAddr ?? '10.0.0.1'}`,
        details: { replica: 'myriarchd-1' },
      };
    if (r < 0.38)
      return { type: 'disconnected', severity: 'warning', message: 'Connection closed by peer' };
    if (r < 0.52)
      return {
        type: 'config_offered',
        severity: 'info',
        message: `Remote config ${(agent.assignment.desiredConfigHash ?? '').slice(0, 8)} offered`,
        details: { hash: agent.assignment.desiredConfigHash },
      };
    if (r < 0.66)
      return {
        type: 'config_applied',
        severity: 'info',
        message: `Remote config ${(agent.assignment.assignedConfigHash ?? '').slice(0, 8)} applied`,
      };
    if (r < 0.72 && agent.remoteConfig.status === 'failed')
      return {
        type: 'config_failed',
        severity: 'error',
        message: 'Remote config rejected by agent',
        details: { error: agent.remoteConfig.errorMessage },
      };
    if (r < 0.8)
      return {
        type: 'health_changed',
        severity: rng.chance(0.3) ? 'warning' : 'info',
        message: rng.chance(0.3)
          ? 'Health changed to unhealthy: exporter otlp_grpc/gateway: connection refused'
          : 'Health changed to healthy',
      };
    if (r < 0.88)
      return {
        type: 'version_changed',
        severity: 'info',
        message: `Version changed to ${agent.version}`,
        details: { from: '0.160.0', to: agent.version },
      };
    if (r < 0.93)
      return { type: 'package_status', severity: 'info', message: 'Package status: installed' };
    return {
      type: 'description_changed',
      severity: 'info',
      message: 'Agent description changed (host.name, os.description)',
    };
  };
  for (let i = 0; i < n; i++) {
    const s = make();
    events.push({
      id: baseId - i,
      agentId: agent.id,
      time: new Date(t).toISOString(),
      type: s.type,
      severity: s.severity,
      message: s.message,
      details: s.details,
    });
    t -= rng.int(3, 600) * MIN;
  }
  events.push({
    id: baseId - n,
    agentId: agent.id,
    time: agent.firstSeenAt,
    type: 'registered',
    severity: 'info',
    message: 'Agent registered',
  });
  return events;
}

export function configBody(
  agent: Agent,
  which: 'desired' | 'assigned' | 'effective',
): { body: string; hash: string } {
  const fleet = fleetByName(agent.assignment.fleetName);
  if (!fleet) return { body: '', hash: '' };
  const attrs = { ...agent.nonIdentifyingAttributes };
  const r = renderMock({ blueprint: fleet.blueprint, fleet, attributes: attrs, validate: false });
  let body = r.body;
  if (
    which === 'assigned' &&
    agent.assignment.assignedConfigHash !== agent.assignment.desiredConfigHash
  ) {
    body = body.replace(
      /memory_limiter:\n {4}check_interval: 1s/,
      'memory_limiter:\n    check_interval: 5s',
    );
  }
  if (which === 'effective') {
    body = body
      .replace(/(\n {2})batch:\n/, '$1batch:\n    send_batch_max_size: 0\n')
      .replace(/\n( {4})endpoint: (otel-gw[^\n]+)/, '\n$1endpoint: $2\n$1timeout: 5s')
      .replace('${env:OTEL_GATEWAY_TOKEN}', '[REDACTED]');
  }
  return {
    body,
    hash:
      which === 'desired'
        ? (agent.assignment.desiredConfigHash ?? r.hash)
        : which === 'assigned'
          ? (agent.assignment.assignedConfigHash ?? r.hash)
          : fakeHash(body),
  };
}
