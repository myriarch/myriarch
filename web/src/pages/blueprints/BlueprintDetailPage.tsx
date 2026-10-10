// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Alert, Badge, Code, Group, Skeleton, Stack, Table, Tabs, Text } from '@mantine/core';
import { IconFileCode, IconGitBranch, IconInfoCircle, IconPlayerPlay } from '@tabler/icons-react';
import { Fragment } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams, useSearchParams } from 'react-router';
import { isApiError } from '@/api/errors';
import { useBlueprint } from '@/api/hooks/fleets';
import type { BlueprintDetail } from '@/api/types';
import { IssueList } from '@/components/IssueList';
import { GitLink } from '@/components/misc';
import { PageHeader } from '@/components/PageHeader';
import { SectionCard } from '@/components/SectionCard';
import { EmptyState, ErrorState } from '@/components/states';
import { YamlView } from '@/components/Yaml';
import { blueprintLink, fleetLink } from '@/lib/links';
import { toYaml } from '@/lib/yaml';
import { formatValue } from '@/lib/format';
import { RenderPreview } from './RenderPreview';

const TABS = ['overview', 'config', 'preview'] as const;
type TabId = (typeof TABS)[number];

function Chain({ chain, current }: { chain: string[]; current: string }) {
  return (
    <Group gap={6} wrap="wrap" aria-label="chain">
      {chain.map((b, i) => (
        <Fragment key={b}>
          {i > 0 && <Text c="dimmed">→</Text>}
          {b === current ? (
            <Badge size="lg" variant="filled" style={{ textTransform: 'none' }}>
              {b}
            </Badge>
          ) : (
            <Badge
              size="lg"
              variant="light"
              component={Link}
              to={blueprintLink(b)}
              style={{ textTransform: 'none', cursor: 'pointer' }}
            >
              {b}
            </Badge>
          )}
        </Fragment>
      ))}
    </Group>
  );
}

function ParametersSection({ detail }: { detail: BlueprintDetail }) {
  const { t } = useTranslation();
  const own = new Set((detail.blueprint.parameters ?? []).map((p) => p.name));
  return (
    <SectionCard
      title={t('blueprints.parameters')}
      description={t('blueprints.parametersHint')}
      padded={false}
    >
      {detail.parameters.length === 0 ? (
        <EmptyState compact title={t('blueprints.noParameters')} />
      ) : (
        <div className="myr-scroll-x">
          <Table fz="sm" verticalSpacing="xs">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('common.name')}</Table.Th>
                <Table.Th>{t('common.type')}</Table.Th>
                <Table.Th>{t('common.default')}</Table.Th>
                <Table.Th>{t('blueprints.constraints')}</Table.Th>
                <Table.Th>{t('blueprints.fromAttribute')}</Table.Th>
                <Table.Th>{t('common.description')}</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {detail.parameters.map((p) => (
                <Table.Tr key={p.name}>
                  <Table.Td>
                    <Group gap={6} wrap="nowrap">
                      <Text className="myr-mono" fw={600} fz="sm">
                        {p.name}
                      </Text>
                      {p.required && (
                        <Badge size="xs" color="red" variant="light">
                          {t('common.required')}
                        </Badge>
                      )}
                      {!own.has(p.name) && (
                        <Badge size="xs" color="gray" variant="outline">
                          {t('blueprints.inherited')}
                        </Badge>
                      )}
                    </Group>
                  </Table.Td>
                  <Table.Td>
                    <Badge variant="light" color="gray" size="sm">
                      {t(`enum.paramType.${p.type}`)}
                    </Badge>
                  </Table.Td>
                  <Table.Td style={{ wordBreak: 'break-all' }}>
                    {p.default === undefined ? (
                      <Text c="dimmed" span>
                        –
                      </Text>
                    ) : (
                      <Code>{formatValue(p.default)}</Code>
                    )}
                  </Table.Td>
                  <Table.Td>
                    <Group gap={4}>
                      {p.enum && (
                        <Badge
                          size="xs"
                          variant="outline"
                          color="gray"
                          style={{ textTransform: 'none' }}
                        >
                          ∈ {p.enum.map(formatValue).join(' | ')}
                        </Badge>
                      )}
                      {p.min !== undefined && (
                        <Badge size="xs" variant="outline" color="gray">
                          min {p.min}
                        </Badge>
                      )}
                      {p.max !== undefined && (
                        <Badge size="xs" variant="outline" color="gray">
                          max {p.max}
                        </Badge>
                      )}
                      {p.pattern && (
                        <Badge
                          size="xs"
                          variant="outline"
                          color="gray"
                          style={{ textTransform: 'none' }}
                        >
                          /{p.pattern}/
                        </Badge>
                      )}
                    </Group>
                  </Table.Td>
                  <Table.Td>
                    {p.fromAttribute ? (
                      <Code>{p.fromAttribute}</Code>
                    ) : (
                      <Text c="dimmed" span>
                        –
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td maw={360}>
                    <Text fz="sm" c="dimmed">
                      {p.description ?? ''}
                    </Text>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </div>
      )}
    </SectionCard>
  );
}

export default function BlueprintDetailPage() {
  const { t } = useTranslation();
  const { name } = useParams();
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const tab: TabId = (TABS as readonly string[]).includes(raw ?? '') ? (raw as TabId) : 'overview';
  const { data, isPending, error, refetch } = useBlueprint(name);
  const crumbs = [{ label: t('nav.blueprints'), to: '/blueprints' }, { label: name ?? '' }];

  if (isPending) {
    return (
      <>
        <PageHeader title={name ?? ''} breadcrumbs={crumbs} />
        <Stack>
          <Skeleton height={80} />
          <Skeleton height={260} />
        </Stack>
      </>
    );
  }
  if (error || !data) {
    const notFound = isApiError(error) && error.status === 404;
    return (
      <>
        <PageHeader title={name ?? ''} breadcrumbs={crumbs} />
        <ErrorState
          error={error}
          title={notFound ? t('blueprints.notFound') : undefined}
          onRetry={notFound ? undefined : () => void refetch()}
        />
      </>
    );
  }
  const bp = data.blueprint;
  const configYaml = bp.config && Object.keys(bp.config).length > 0 ? toYaml(bp.config) : '';

  return (
    <>
      <PageHeader
        title={bp.name}
        subtitle={bp.description}
        breadcrumbs={crumbs}
        actions={<GitLink url={data.webUrl} />}
      />
      <Alert variant="light" color="gray" mb="md" icon={<IconGitBranch size={16} />} py="xs">
        {t('common.readOnlyGit')}{' '}
        <Text span className="myr-mono" fz="xs">
          {bp.source.path}
        </Text>
      </Alert>
      <Tabs
        value={tab}
        onChange={(v) => setParams(v && v !== 'overview' ? { tab: v } : {}, { replace: true })}
        keepMounted={false}
      >
        <Tabs.List mb="md">
          <Tabs.Tab value="overview" leftSection={<IconInfoCircle size={16} />}>
            {t('blueprints.tabs.overview')}
          </Tabs.Tab>
          <Tabs.Tab value="config" leftSection={<IconFileCode size={16} />}>
            {t('blueprints.tabs.config')}
          </Tabs.Tab>
          <Tabs.Tab value="preview" leftSection={<IconPlayerPlay size={16} />}>
            {t('blueprints.tabs.preview')}
          </Tabs.Tab>
        </Tabs.List>

        <Tabs.Panel value="overview">
          <Stack gap="md">
            <SectionCard title={t('blueprints.chain')} description={t('blueprints.chainHint')}>
              <Chain chain={data.chain.length ? data.chain : [bp.name]} current={bp.name} />
            </SectionCard>
            <ParametersSection detail={data} />
            <SectionCard
              title={t('blueprints.fragments')}
              description={t('blueprints.fragmentsHint')}
            >
              {(bp.fragments ?? []).length === 0 ? (
                <Text c="dimmed" fz="sm">
                  {t('blueprints.noFragments')}
                </Text>
              ) : (
                <Stack gap="md">
                  {(bp.fragments ?? []).map((f) => (
                    <Stack
                      key={f.name}
                      gap={6}
                      p="sm"
                      style={{
                        border: '1px solid var(--myr-border)',
                        borderRadius: 'var(--mantine-radius-md)',
                      }}
                    >
                      <Group gap="sm" wrap="wrap">
                        <Text fw={600} className="myr-mono">
                          {f.name}
                        </Text>
                        {f.when ? (
                          <Group gap={6}>
                            <Text fz="xs" c="dimmed">
                              {t('blueprints.when')}
                            </Text>
                            <Code>{f.when}</Code>
                          </Group>
                        ) : (
                          <Badge size="xs" variant="light" color="gray">
                            {t('blueprints.always')}
                          </Badge>
                        )}
                      </Group>
                      {f.remove && f.remove.length > 0 && (
                        <Group gap={6}>
                          <Text fz="xs" c="dimmed">
                            {t('blueprints.removes')}
                          </Text>
                          {f.remove.map((r) => (
                            <Code key={r}>{r}</Code>
                          ))}
                        </Group>
                      )}
                      {f.config && Object.keys(f.config).length > 0 && (
                        <YamlView value={toYaml(f.config)} maxHeight="280px" />
                      )}
                    </Stack>
                  ))}
                </Stack>
              )}
            </SectionCard>
            <SectionCard title={t('blueprints.usedBy')}>
              <Group gap={6}>
                {data.usedBy.map((f) => (
                  <Badge
                    key={f}
                    variant="light"
                    size="lg"
                    component={Link}
                    to={fleetLink(f)}
                    style={{ textTransform: 'none', cursor: 'pointer' }}
                  >
                    {f}
                  </Badge>
                ))}
                {data.usedBy.length === 0 && (
                  <Text c="dimmed" fz="sm">
                    {t('blueprints.unused')}
                  </Text>
                )}
              </Group>
            </SectionCard>
            <SectionCard title={t('common.issues')}>
              <IssueList issues={data.issues} />
            </SectionCard>
          </Stack>
        </Tabs.Panel>

        <Tabs.Panel value="config">
          <SectionCard
            title={t('blueprints.ownConfig')}
            description={t('blueprints.ownConfigHint')}
          >
            {configYaml ? (
              <Stack gap="sm">
                {bp.remove && bp.remove.length > 0 && (
                  <Group gap={6}>
                    <Text fz="xs" c="dimmed">
                      {t('blueprints.removes')}
                    </Text>
                    {bp.remove.map((r) => (
                      <Code key={r}>{r}</Code>
                    ))}
                  </Group>
                )}
                <YamlView value={configYaml} maxHeight="640px" />
              </Stack>
            ) : (
              <EmptyState
                compact
                title={t('blueprints.noConfig')}
                text={t('blueprints.noConfigHint')}
              />
            )}
          </SectionCard>
        </Tabs.Panel>

        <Tabs.Panel value="preview">
          <RenderPreview blueprint={bp.name} parameters={data.parameters} />
        </Tabs.Panel>
      </Tabs>
    </>
  );
}
