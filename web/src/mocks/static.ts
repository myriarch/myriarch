// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * Static fixtures modeled on internal/testdata/gitops (fleets, blueprints, policies, executors) and a
 * plausible set of upstream releases. All shapes are the generated OpenAPI types.
 */
import type {
  Blueprint,
  ChangelogEntry,
  ExecutorSummary,
  Fleet,
  Issue,
  ParameterSpec,
  Release,
  RolloutPolicy,
} from '@/api/types';

/** Fixture timestamps are offsets from the moment the mock is loaded, so they always look fresh. */
export const NOW = Date.now();
export const iso = (offsetMs: number) => new Date(NOW + offsetMs).toISOString();
export const MIN = 60_000;
export const HOUR = 60 * MIN;
export const DAY = 24 * HOUR;

export const LATEST_VERSION = '0.163.0';
export const GIT_WEB = 'https://git.example.internal/observability/myriarch-config/-/blob/main';
export const COMMIT_APPLIED = '9f3c2a17d8e44b60a5c1d2e3f4a5b6c7d8e9f001';
export const COMMIT_LATEST = '4be81d09c7a2f3e5d6c7b8a9f0e1d2c3b4a59988';
export const COMMIT_PREV = '1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d';

const src = (path: string, commit = COMMIT_APPLIED) => ({
  path,
  commit,
  contentHash: 'a'.repeat(8) + path.length.toString(16).padStart(8, '0') + 'c'.repeat(48),
});

/* -------------------------------------------------------------------------- policies */

export const POLICIES: RolloutPolicy[] = [
  {
    name: 'default',
    description: 'Built-in default: 10 % canary, then all',
    waves: [
      { name: 'canary', percent: 10 },
      { name: 'all', percent: 100 },
    ],
    maxConcurrent: 10,
    healthWait: '2m',
    applyTimeout: '15m',
    failureThreshold: { maxFailed: 1 },
    onFailure: 'pause',
    source: { path: '(built-in)', commit: '' },
  },
  {
    name: 'standard',
    description: '10 % -> all, nights between 01:00 and 05:00 (Europe/Amsterdam)',
    waves: [
      { name: 'canary', percent: 10 },
      { name: 'all', percent: 100 },
    ],
    maxConcurrent: 20,
    healthWait: '2m',
    applyTimeout: '15m',
    failureThreshold: { maxFailed: 1 },
    onFailure: 'pause',
    maintenanceWindows: [{ start: '01:00', end: '05:00', timezone: 'Europe/Amsterdam' }],
    source: src('policies/standard.yaml'),
  },
  {
    name: 'edge-careful',
    description: 'Pilot sites -> approval -> 10 % -> 50 % -> all; nights and Sundays only',
    waves: [
      { name: 'pilot', selector: { matchAttributes: { 'labels.pilot': 'true' } } },
      { name: 'early', percent: 10, requireApproval: true },
      { name: 'half', percent: 50 },
      { name: 'all', percent: 100 },
    ],
    maxConcurrent: 50,
    healthWait: '10m',
    applyTimeout: '30m',
    offlineGracePeriod: '2h',
    failureThreshold: { maxFailed: 3, maxFailedPercent: 5 },
    onFailure: 'pause',
    timezoneAttribute: 'site.timezone',
    defaultTimezone: 'Europe/Amsterdam',
    maintenanceWindows: [
      { days: ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'], start: '21:30', end: '06:30' },
      { days: ['Sun'], start: '00:00', end: '00:00' },
    ],
    source: src('policies/edge-careful.yaml'),
  },
  {
    name: 'gateway-version',
    description: 'Gateway update: canary gateway first, then the rest; pauses on failures',
    waves: [
      { name: 'canary', selector: { matchAttributes: { 'site.id': 'gw-canary-01' } } },
      { name: 'rest', percent: 100 },
    ],
    maxConcurrent: 1,
    healthWait: '60s',
    applyTimeout: '15m',
    failureThreshold: { maxFailed: 1 },
    onFailure: 'pause',
    source: src('policies/gateway-version.yaml'),
  },
  {
    name: 'gateway',
    description: 'Gateways one after the other, automatic rollback on failure',
    waves: [
      { name: 'first', percent: 34 },
      { name: 'rest', percent: 100, requireApproval: true },
    ],
    maxConcurrent: 1,
    healthWait: '5m',
    applyTimeout: '20m',
    failureThreshold: { maxFailed: 1 },
    onFailure: 'rollback',
    source: src('policies/gateway.yaml'),
  },
];

/* -------------------------------------------------------------------------- executors */

export const EXECUTORS: ExecutorSummary[] = [
  {
    name: 'awx-linux',
    type: 'awx',
    description: 'Ansible AWX job template "otelcol-upgrade"',
    target: 'https://awx.example.internal/api/v2/job_templates/42/launch/',
    source: src('executors/awx-linux.yaml'),
    usedBy: ['linux-hosts'],
  },
  {
    name: 'k8s-image-bump',
    type: 'webhook',
    description: 'Webhook to the deployment pipeline (image tag bump)',
    target: 'https://ci-hooks.example.internal/myriarch/k8s-image-bump',
    source: src('executors/k8s-image-bump.yaml'),
    usedBy: [],
  },
];

/* -------------------------------------------------------------------------- fleets */

export const FLEETS: Fleet[] = [
  {
    name: 'k8s-gateway',
    description: 'Regional OTLP gateways in Kubernetes; version changes via image tag pipeline',
    displayName: 'Gateways (Kubernetes)',
    priority: 200,
    selector: { matchAttributes: { 'site.type': 'k8s-gateway' } },
    blueprint: 'k8s-gateway',
    parameters: { backendEndpoint: 'https://otlp.observability.example.com' },
    collector: { distribution: 'otelcol-contrib', version: '0.161.0' },
    update: {
      executor: 'updater',
      params: { workload: 'statefulset/otel-gateway' },
      // Git sets the frame, the UI chooses the target version within it.
      selection: {
        enabled: true,
        versionRange: '>=0.161.0, <=0.162.0',
        allowDowngrade: false,
        requireReason: false,
      },
    },
    rollout: { configPolicy: 'gateway', versionPolicy: 'gateway-version' },
    source: src('fleets/k8s-gateway.yaml'),
  },
  {
    name: 'windows-hosts',
    description: 'Windows hosts at edge sites in NL',
    displayName: 'Windows hosts',
    labels: { team: 'edge-ops' },
    priority: 100,
    selector: {
      matchAttributes: { 'site.type': 'edge', 'site.country': 'NL' },
      matchExpressions: [{ key: 'os.type', operator: 'In', values: ['windows'] }],
    },
    blueprint: 'edge-windows',
    parameters: { gatewayEndpoint: 'otel-gw-1.example.internal:4317' },
    collector: { distribution: 'otelcol-contrib', version: '0.162.0' },
    update: { executor: 'updater' },
    rollout: { configPolicy: 'edge-careful', versionPolicy: 'edge-careful' },
    source: src('fleets/windows-hosts.yaml'),
  },
  {
    name: 'edge-sites',
    description: 'Windows hosts at edge sites in BE - own gateway, own time zone',
    displayName: 'Edge sites',
    priority: 100,
    selector: {
      matchAttributes: { 'site.type': 'edge', 'site.country': 'BE' },
      matchExpressions: [{ key: 'os.type', operator: 'In', values: ['windows'] }],
    },
    blueprint: 'edge-windows',
    parameters: {
      gatewayEndpoint: 'otel-gw-2.example.internal:4317',
      appLogPaths: ['C:\\ProgramData\\EdgeApp\\logs\\*.log', 'D:\\EdgeApp\\journal\\*.log'],
    },
    collector: { distribution: 'otelcol-contrib', version: '0.161.0' },
    update: {
      executor: 'updater',
      // A later Git change narrowed the frame: the UI selection 0.162.0 is on hold (see MockDb).
      selection: {
        enabled: true,
        versionRange: '>=0.160.0, <0.162.0',
        allowDowngrade: false,
        requireReason: false,
      },
    },
    rollout: { configPolicy: 'edge-careful', versionPolicy: 'edge-careful' },
    source: src('fleets/edge-sites.yaml'),
  },
  {
    name: 'linux-hosts',
    description: 'Linux hosts at office sites, updates via Ansible AWX',
    displayName: 'Linux hosts',
    priority: 50,
    selector: { matchAttributes: { 'site.type': 'office', 'os.type': 'linux' } },
    blueprint: 'office-linux',
    parameters: {
      gatewayEndpoint: 'otel-gw-1.example.internal:4317',
      journaldUnits: ['sshd.service', 'nginx.service', 'postgresql.service'],
    },
    collector: { distribution: 'otelcol-contrib', version: '0.162.0' },
    update: { executor: 'awx', executorRef: 'awx-linux' },
    rollout: { configPolicy: 'standard', versionPolicy: 'standard' },
    source: src('fleets/linux-hosts.yaml'),
  },
  {
    name: 'datacenter',
    description: 'Data centers (Linux) with myriarch-updater',
    displayName: 'Data centers',
    priority: 50,
    selector: { matchAttributes: { 'site.type': 'datacenter' } },
    blueprint: 'datacenter-linux',
    parameters: {
      gatewayEndpoint: 'otel-gw-1.example.internal:4317',
      appMetricsTarget: 'localhost:9464',
    },
    collector: { distribution: 'otelcol-contrib', version: '0.161.0' },
    update: {
      executor: 'updater',
      selection: {
        enabled: true,
        versions: ['0.161.0', '0.162.0', '0.163.0'],
        allowDowngrade: false,
        requireReason: true,
      },
    },
    rollout: { configPolicy: 'standard', versionPolicy: 'standard' },
    source: src('fleets/datacenter.yaml'),
  },
];

/* -------------------------------------------------------------------------- blueprints */

const p = (
  name: string,
  type: ParameterSpec['type'],
  rest: Partial<ParameterSpec> = {},
): ParameterSpec => ({ name, type, ...rest });

const BASE_PARAMS: ParameterSpec[] = [
  p('gatewayEndpoint', 'string', {
    required: true,
    description: 'OTLP/gRPC endpoint of the regional gateway (host:port)',
  }),
  p('gatewayInsecure', 'bool', {
    default: false,
    description: 'Disable TLS for the gateway export (test only)',
  }),
  p('gatewayAuth', 'bool', {
    default: true,
    description: 'Send a bearer token from the environment variable OTEL_GATEWAY_TOKEN',
  }),
  p('environment', 'string', { default: 'prod', enum: ['prod', 'test', 'dev'] }),
  p('logLevel', 'string', { default: 'info', enum: ['debug', 'info', 'warn', 'error'] }),
  p('memoryLimitPercent', 'int', { default: 75, min: 20, max: 90 }),
  p('storageDir', 'string', {
    default: '/var/lib/otelcol/storage',
    description: 'Directory of the persistent sending queue',
  }),
  p('siteId', 'string', { fromAttribute: 'site.id', default: 'unknown' }),
  p('siteType', 'string', { fromAttribute: 'site.type', default: 'unknown' }),
  p('siteCountry', 'string', { fromAttribute: 'site.country', default: 'unknown' }),
];

const BASE_CONFIG = {
  extensions: {
    health_check: { endpoint: 'localhost:13133' },
    'file_storage/queue': { directory: '[[ .params.storageDir ]]', create_directory: true },
  },
  receivers: {
    otlp: {
      protocols: { grpc: { endpoint: 'localhost:4317' }, http: { endpoint: 'localhost:4318' } },
    },
  },
  processors: {
    memory_limiter: {
      check_interval: '1s',
      limit_percentage: '[[ .params.memoryLimitPercent ]]',
      spike_limit_percentage: 15,
    },
    resource_detection: { detectors: ['env', 'system'] },
    'resource/site': {
      attributes: [
        { key: 'site.id', value: '[[ .params.siteId ]]', action: 'upsert' },
        { key: 'site.type', value: '[[ .params.siteType ]]', action: 'upsert' },
        {
          key: 'deployment.environment.name',
          value: '[[ .params.environment ]]',
          action: 'upsert',
        },
      ],
    },
    batch: { send_batch_size: 2048, timeout: '5s' },
  },
  exporters: {
    'otlp_grpc/gateway': {
      endpoint: '[[ .params.gatewayEndpoint ]]',
      tls: { insecure: '[[ .params.gatewayInsecure ]]' },
      sending_queue: { enabled: true, storage: 'file_storage/queue' },
      retry_on_failure: { enabled: true, max_elapsed_time: '0s' },
    },
  },
  service: {
    extensions: ['health_check', 'file_storage/queue'],
    pipelines: {
      traces: {
        receivers: ['otlp'],
        processors: ['memory_limiter', 'resource_detection', 'resource/site', 'batch'],
        exporters: ['otlp_grpc/gateway'],
      },
      metrics: {
        receivers: ['otlp'],
        processors: ['memory_limiter', 'resource_detection', 'resource/site', 'batch'],
        exporters: ['otlp_grpc/gateway'],
      },
      logs: {
        receivers: ['otlp'],
        processors: ['memory_limiter', 'resource_detection', 'resource/site', 'batch'],
        exporters: ['otlp_grpc/gateway'],
      },
    },
  },
};

export const BLUEPRINTS: Blueprint[] = [
  {
    name: 'base',
    description: 'Common base for all collectors (health, queue, export to the gateway)',
    parameters: BASE_PARAMS,
    config: BASE_CONFIG,
    fragments: [
      {
        name: 'gateway-auth',
        when: '[[ .params.gatewayAuth ]]',
        config: {
          exporters: {
            'otlp_grpc/gateway': { headers: { authorization: 'Bearer ${env:OTEL_GATEWAY_TOKEN}' } },
          },
        },
      },
    ],
    source: src('blueprints/base.yaml'),
  },
  {
    name: 'edge-windows',
    description: 'Edge nodes on Windows - host metrics, event log, application logs',
    extends: 'base',
    parameters: [
      p('storageDir', 'string', { default: 'C:\\ProgramData\\Myriarch\\storage' }),
      p('appLogPaths', 'list', {
        default: ['C:\\ProgramData\\EdgeApp\\logs\\*.log'],
        description: 'Glob patterns of the application log files',
      }),
      p('collectEventLog', 'bool', { default: true }),
      p('hostMetricsInterval', 'duration', { default: '60s' }),
    ],
    config: {
      receivers: {
        host_metrics: {
          collection_interval: '[[ .params.hostMetricsInterval ]]',
          scrapers: { cpu: {}, memory: {}, disk: {}, filesystem: {}, network: {}, paging: {} },
        },
        'windows_event_log/application': { channel: 'application', start_at: 'end' },
        'windows_event_log/system': { channel: 'system', start_at: 'end' },
        'file_log/app': {
          include: '[[ .params.appLogPaths ]]',
          start_at: 'end',
          storage: 'file_storage/queue',
        },
      },
      service: {
        pipelines: {
          metrics: { receivers: ['otlp', 'host_metrics'] },
          logs: {
            receivers: [
              'otlp',
              'windows_event_log/application',
              'windows_event_log/system',
              'file_log/app',
            ],
          },
        },
      },
    },
    fragments: [
      {
        name: 'no-eventlog',
        when: '[[ not .params.collectEventLog ]]',
        remove: ['receivers::windows_event_log/application', 'receivers::windows_event_log/system'],
        config: { service: { pipelines: { logs: { receivers: ['otlp', 'file_log/app'] } } } },
      },
    ],
    source: src('blueprints/edge-windows.yaml'),
  },
  {
    name: 'office-linux',
    description: 'Office - Linux servers with journald and syslog',
    extends: 'base',
    parameters: [
      p('journaldUnits', 'list', {
        default: [],
        description: 'Read only these systemd units (empty = all)',
      }),
      p('syslogPaths', 'list', { default: ['/var/log/syslog', '/var/log/messages'] }),
    ],
    config: {
      receivers: {
        host_metrics: {
          collection_interval: '30s',
          scrapers: {
            cpu: {},
            load: {},
            memory: {},
            disk: {},
            filesystem: {},
            network: {},
            paging: {},
            processes: {},
          },
        },
        journald: {
          directory: '/var/log/journal',
          units: '[[ .params.journaldUnits ]]',
          start_at: 'end',
          priority: 'info',
        },
        'file_log/syslog': {
          include: '[[ .params.syslogPaths ]]',
          start_at: 'end',
          storage: 'file_storage/queue',
        },
      },
      service: {
        pipelines: {
          metrics: { receivers: ['otlp', 'host_metrics'] },
          logs: { receivers: ['otlp', 'journald', 'file_log/syslog'] },
        },
      },
    },
    source: src('blueprints/office-linux.yaml'),
  },
  {
    name: 'datacenter-linux',
    description: 'Data centers - Linux with application Prometheus metrics',
    extends: 'base',
    parameters: [
      p('appMetricsTarget', 'string', {
        default: 'localhost:9464',
        description: 'Prometheus endpoint of the application',
      }),
    ],
    config: {
      receivers: {
        host_metrics: {
          collection_interval: '30s',
          scrapers: { cpu: {}, memory: {}, disk: {}, filesystem: {}, network: {} },
        },
        'prometheus/app': {
          config: {
            scrape_configs: [
              {
                job_name: 'app',
                scrape_interval: '30s',
                static_configs: [{ targets: ['[[ .params.appMetricsTarget ]]'] }],
              },
            ],
          },
        },
      },
      service: {
        pipelines: { metrics: { receivers: ['otlp', 'host_metrics', 'prometheus/app'] } },
      },
    },
    source: src('blueprints/datacenter-linux.yaml'),
  },
  {
    name: 'k8s-gateway',
    description: 'OTLP gateway in Kubernetes (deployment, supervisor in the pod)',
    extends: 'base',
    parameters: [
      p('gatewayEndpoint', 'string', {
        default: 'unused',
        description: 'Not used in the gateway (export goes directly to the backend)',
      }),
      p('backendEndpoint', 'string', {
        required: true,
        description: 'OTLP/HTTP endpoint of the backend (https://...)',
      }),
      p('storageDir', 'string', { default: '/var/lib/otelcol/storage' }),
      p('dropHealthChecks', 'bool', { default: true }),
    ],
    remove: [
      'exporters::otlp_grpc/gateway',
      'extensions::file_storage/queue',
      'processors::resource/site',
      'processors::resource_detection',
    ],
    config: {
      extensions: { health_check: { endpoint: '0.0.0.0:13133' } },
      receivers: { otlp: { protocols: { grpc: { endpoint: '0.0.0.0:4317' } } } },
      exporters: { 'otlphttp/backend': { endpoint: '[[ .params.backendEndpoint ]]' } },
    },
    source: src('blueprints/k8s-gateway.yaml'),
  },
];

/* -------------------------------------------------------------------------- releases */

function entry(
  source: ChangelogEntry['source'],
  version: string,
  component: string | undefined,
  text: string,
  issues: number[],
): ChangelogEntry {
  return {
    source,
    file: source === 'core' ? '.chloggen' : 'CHANGELOG.md',
    version,
    component,
    text,
    issues,
  };
}

const BREAKING: Record<string, ChangelogEntry[]> = {
  '0.163.0': [
    entry(
      'contrib',
      '0.163.0',
      'receiver/filelog',
      'Remove the deprecated `retry_on_failure.max_elapsed_time` default of 5m; the operator now retries indefinitely unless configured.',
      [44120],
    ),
    entry(
      'contrib',
      '0.163.0',
      'exporter/otlphttp',
      'Change the default `compression` to `zstd`. Backends that do not support zstd must set `compression: gzip` explicitly.',
      [44311],
    ),
    entry(
      'core',
      '0.163.0',
      'service/telemetry',
      'Remove the `service.telemetry.metrics.address` setting; use `service.telemetry.metrics.readers` instead.',
      [13902],
    ),
    entry(
      'contrib',
      '0.163.0',
      'processor/resourcedetection',
      'The `cloud.platform` attribute value for AWS ECS Fargate changed from `aws_ecs` to `aws_ecs_fargate`.',
      [44257],
    ),
  ],
  '0.162.0': [
    entry(
      'contrib',
      '0.162.0',
      'receiver/hostmetrics',
      'Process scraper: the `process.cpu.utilization` metric is no longer enabled by default.',
      [43901],
    ),
    entry(
      'core',
      '0.162.0',
      'exporterhelper',
      'The `sending_queue.queue_size` is now counted in requests by default (was items).',
      [13744],
    ),
  ],
  '0.161.0': [
    entry(
      'contrib',
      '0.161.0',
      'exporter/kafka',
      'Rename `topic_from_attribute` to `topic.from_attribute`.',
      [43512],
    ),
  ],
  '0.159.0': [
    entry(
      'contrib',
      '0.159.0',
      'receiver/windowseventlog',
      'The `raw` option now defaults to `true`; set `raw: false` to keep the parsed output.',
      [42870],
    ),
    entry(
      'core',
      '0.159.0',
      'confmap',
      'Drop support for the `expand-env-vars` feature gate.',
      [13501],
    ),
  ],
  '0.157.0': [
    entry(
      'contrib',
      '0.157.0',
      'processor/transform',
      'Remove deprecated OTTL function `IsMatch` flag handling.',
      [41990],
    ),
  ],
  '0.155.0': [
    entry('core', '0.155.0', 'otelcol', 'Remove the `--feature-gates` shorthand `-g`.', [13222]),
  ],
  '0.153.0': [
    entry(
      'contrib',
      '0.153.0',
      'receiver/prometheus',
      'Rename the `report_extra_scrape_metrics` option.',
      [41033],
    ),
  ],
};

const DEPRECATIONS: Record<string, ChangelogEntry[]> = {
  '0.163.0': [
    entry(
      'contrib',
      '0.163.0',
      'exporter/loadbalancing',
      'Deprecate the `routing_key: service` alias, use `routing_key: service.name`.',
      [44001],
    ),
    entry('core', '0.163.0', 'otelcol', 'Deprecate `service.telemetry.logs.sampling`.', [13950]),
    entry(
      'contrib',
      '0.163.0',
      'extension/opamp',
      'Deprecate `server.http.polling_interval` in favour of `server.http.heartbeat_interval`.',
      [44199],
    ),
  ],
  '0.162.0': [
    entry(
      'contrib',
      '0.162.0',
      'receiver/k8s_cluster',
      'Deprecate the `distribution` option.',
      [43800],
    ),
    entry('core', '0.162.0', 'pdata', 'Deprecate `plog.LogRecordSlice.RemoveIf` helpers.', [13700]),
  ],
  '0.160.0': [
    entry(
      'contrib',
      '0.160.0',
      'processor/attributes',
      'Deprecate `from_context` prefix `metadata.`',
      [43210],
    ),
  ],
  '0.158.0': [
    entry(
      'contrib',
      '0.158.0',
      'exporter/awss3',
      'Deprecate `s3_partition` in favour of `s3_partition_format`.',
      [42500],
    ),
  ],
};

const OTEL_VERSIONS = [
  '0.163.0',
  '0.162.0',
  '0.161.0',
  '0.160.0',
  '0.159.0',
  '0.158.0',
  '0.157.0',
  '0.156.0',
  '0.155.0',
  '0.154.0',
  '0.153.0',
  '0.152.0',
  '0.151.0',
  '0.150.0',
];
const SUP_VERSIONS = ['0.163.0', '0.162.0', '0.161.0', '0.160.0', '0.159.0'];

function assetsFor(distribution: string, version: string): Release['assets'] {
  const exts: [string, string, 'tar.gz' | 'zip' | 'msi' | 'binary', number][] =
    distribution === 'otelcol-contrib'
      ? [
          ['linux', 'amd64', 'tar.gz', 230_000_000],
          ['linux', 'arm64', 'tar.gz', 215_000_000],
          ['windows', 'amd64', 'tar.gz', 240_000_000],
          ['darwin', 'arm64', 'tar.gz', 222_000_000],
        ]
      : [
          ['linux', 'amd64', 'binary', 24_000_000],
          ['windows', 'amd64', 'binary', 25_000_000],
          ['windows', 'amd64', 'msi', 26_000_000],
        ];
  return exts.map(([os, arch, format, size]) => {
    const ext =
      format === 'tar.gz' ? '.tar.gz' : format === 'msi' ? '.msi' : os === 'windows' ? '.exe' : '';
    const name = `${distribution}_${version}_${os}_${arch}${ext}`;
    return {
      name,
      url: `https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v${version}/${name}`,
      size,
      os,
      arch,
      format,
    };
  });
}

function releaseOf(distribution: string, version: string, index: number): Release {
  const minor = Number(version.split('.')[1]);
  return {
    distribution,
    version,
    tag: distribution === 'opampsupervisor' ? `cmd/opampsupervisor/v${version}` : `v${version}`,
    publishedAt: iso(-(index * 9 + 1) * DAY - 3 * HOUR),
    prerelease: false,
    htmlUrl: `https://github.com/open-telemetry/opentelemetry-collector-releases/releases/tag/v${version}`,
    notes: `## Release ${version}\n\n### Changelog\n\n* ${minor} dependency updates\n* See the linked breaking changes and deprecations.\n`,
    breakingChanges: distribution === 'otelcol-contrib' ? (BREAKING[version] ?? []) : [],
    deprecations: distribution === 'otelcol-contrib' ? (DEPRECATIONS[version] ?? []) : [],
    assets: assetsFor(distribution, version),
    detectedAt: iso(-(index * 9 + 1) * DAY - 2 * HOUR),
    acknowledgedBy: index > 0 ? 'admin' : undefined,
    acknowledgedAt: index > 0 ? iso(-(index * 9) * DAY) : undefined,
  };
}

export const RELEASES: Release[] = [
  ...OTEL_VERSIONS.map((v, i) => releaseOf('otelcol-contrib', v, i)),
  ...SUP_VERSIONS.map((v, i) => releaseOf('opampsupervisor', v, i)),
];

/* -------------------------------------------------------------------------- gitops issues */

export const GITOPS_ISSUES: Issue[] = [
  {
    severity: 'warning',
    code: 'unused_blueprint_parameter',
    message:
      'Parameter "journaldUnits" of blueprint office-linux is not set by any fleet except linux-hosts',
    file: 'blueprints/office-linux.yaml',
    line: 12,
    object: 'Blueprint/office-linux',
  },
];

export const FLEET_BY_NAME = new Map(FLEETS.map((f) => [f.name, f]));
export const POLICY_BY_NAME = new Map(POLICIES.map((x) => [x.name, x]));
