// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  ActionIcon,
  Anchor,
  Box,
  Group,
  Paper,
  Stack,
  Table,
  Text,
  Tooltip,
  UnstyledButton,
} from '@mantine/core';
import {
  IconArrowDown,
  IconArrowUp,
  IconArrowsSort,
  IconExternalLink,
  IconGitBranch,
} from '@tabler/icons-react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { Selector } from '@/api/types';
import { toneColor, type Tone } from '@/theme/status';

/** Label/value rows for detail views. */
export function KeyValue({
  items,
  labelWidth = 170,
}: {
  items: { label: ReactNode; value: ReactNode; hidden?: boolean }[];
  labelWidth?: number;
}) {
  return (
    <Stack gap={0}>
      {items
        .filter((i) => !i.hidden)
        .map((i, idx) => (
          <Group
            key={idx}
            align="flex-start"
            wrap="nowrap"
            gap="md"
            py={8}
            style={{ borderBottom: '1px solid var(--myr-border)' }}
          >
            <Text fz="sm" c="dimmed" w={labelWidth} style={{ flex: `0 0 ${labelWidth}px` }}>
              {i.label}
            </Text>
            <Box fz="sm" style={{ minWidth: 0, flex: 1, wordBreak: 'break-word' }}>
              {i.value ?? '–'}
            </Box>
          </Group>
        ))}
    </Stack>
  );
}

export interface Segment {
  key: string;
  value: number;
  tone: Tone;
  label: string;
  /** `track`: no fill, the empty track shows through (e.g. targets that have not started). */
  fill?: 'solid' | 'track';
}

/** Accessible summary of a bar: only the non-empty segments, "label: value" each. */
function segmentSummary(segments: Segment[]): string {
  const present = segments.filter((s) => s.value > 0);
  return (present.length > 0 ? present : segments).map((s) => `${s.label}: ${s.value}`).join(', ');
}

const segmentColor = (s: Segment) =>
  `var(--mantine-color-${toneColor[s.tone]}-${s.tone === 'neutral' ? 4 : 6})`;

/** Stacked horizontal bar for counts (e.g. targets per status); the legend is optional. */
export function SegmentBar({
  segments,
  height = 10,
  legend = false,
  ariaLabel,
}: {
  segments: Segment[];
  height?: number;
  legend?: boolean;
  ariaLabel?: string;
}) {
  const total = segments.reduce((s, x) => s + x.value, 0);
  return (
    <Stack gap={6}>
      <Box
        role="img"
        aria-label={ariaLabel ?? segmentSummary(segments)}
        style={{
          display: 'flex',
          height,
          borderRadius: height,
          overflow: 'hidden',
          background: 'var(--myr-track)',
        }}
        className="myr-segment-track"
      >
        {total > 0 &&
          segments
            .filter((s) => s.value > 0)
            .map((s) => (
              <Tooltip key={s.key} label={`${s.label}: ${s.value}`}>
                <Box
                  data-segment={s.key}
                  data-fill={s.fill ?? 'solid'}
                  data-tone={s.tone}
                  style={{
                    width: `${(s.value / total) * 100}%`,
                    background: s.fill === 'track' ? 'transparent' : segmentColor(s),
                    minWidth: 2,
                    transition: 'width 300ms ease',
                  }}
                />
              </Tooltip>
            ))}
      </Box>
      {legend && (
        <Group gap="md" wrap="wrap">
          {segments
            .filter((s) => s.value > 0)
            .map((s) => (
              <Group key={s.key} gap={6} wrap="nowrap" data-legend={s.key}>
                <Box
                  w={8}
                  h={8}
                  data-swatch
                  data-fill={s.fill ?? 'solid'}
                  style={{
                    borderRadius: 2,
                    background: s.fill === 'track' ? 'var(--myr-track)' : segmentColor(s),
                    border:
                      s.fill === 'track' ? '1px solid var(--mantine-color-gray-5)' : undefined,
                    boxSizing: 'border-box',
                  }}
                />
                <Text fz="xs" c="dimmed" className="myr-tabular">
                  {s.label} {s.value}
                </Text>
              </Group>
            ))}
        </Group>
      )}
    </Stack>
  );
}

/** Sortable table header cell: click toggles asc -> desc (server-side sort field). */
export function SortableTh({
  field,
  sort,
  onSort,
  children,
  w,
  align,
}: {
  field: string;
  sort: string;
  onSort: (next: string) => void;
  children: ReactNode;
  w?: number | string;
  align?: 'left' | 'right';
}) {
  const active = sort === field || sort === `-${field}`;
  const desc = sort === `-${field}`;
  const Icon = !active ? IconArrowsSort : desc ? IconArrowDown : IconArrowUp;
  return (
    <Table.Th
      w={w}
      className="myr-sticky-th"
      aria-sort={active ? (desc ? 'descending' : 'ascending') : 'none'}
      style={{ textAlign: align }}
    >
      <UnstyledButton
        onClick={() => onSort(active && !desc ? `-${field}` : field)}
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 4,
          fontWeight: 600,
          font: 'inherit',
        }}
      >
        {children}
        <Icon size={14} opacity={active ? 1 : 0.4} aria-hidden />
      </UnstyledButton>
    </Table.Th>
  );
}

export function GitLink({ url, label }: { url?: string; label?: string }) {
  const { t } = useTranslation();
  if (!url) return null;
  return (
    <Anchor href={url} target="_blank" rel="noopener noreferrer" fz="sm">
      <Group gap={4} wrap="nowrap" component="span">
        <IconGitBranch size={14} />
        {label ?? t('common.openInGit')}
        <IconExternalLink size={12} />
      </Group>
    </Anchor>
  );
}

export function ExternalLinkIcon({ href, label }: { href: string; label: string }) {
  return (
    <Tooltip label={label}>
      <ActionIcon
        component="a"
        href={href}
        target="_blank"
        rel="noopener noreferrer"
        variant="subtle"
        color="gray"
        size="sm"
        aria-label={label}
      >
        <IconExternalLink size={14} />
      </ActionIcon>
    </Tooltip>
  );
}

/** Human readable selector: "site.type = pos AND os.type in [windows]". */
export function SelectorView({ selector }: { selector: Selector | undefined }) {
  const { t } = useTranslation();
  const attrs = Object.entries(selector?.matchAttributes ?? {});
  const exprs = selector?.matchExpressions ?? [];
  if (attrs.length === 0 && exprs.length === 0) {
    return (
      <Text fz="sm" c="dimmed">
        {t('selector.matchAll')}
      </Text>
    );
  }
  return (
    <Group gap={6} wrap="wrap">
      {attrs.map(([k, v]) => (
        <Paper key={k} withBorder px={8} py={2} radius="sm" className="myr-mono">
          {k} = {v}
        </Paper>
      ))}
      {exprs.map((e, i) => (
        <Paper key={`e${i}`} withBorder px={8} py={2} radius="sm" className="myr-mono">
          {e.key} {t(`enum.selectorOp.${e.operator}`)}
          {e.values && e.values.length > 0 ? ` [${e.values.join(', ')}]` : ''}
        </Paper>
      ))}
    </Group>
  );
}
