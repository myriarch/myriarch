// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Card, Group, RingProgress, Stack, Text, ThemeIcon, Tooltip } from '@mantine/core';
import type { ReactNode } from 'react';
import { Link } from 'react-router';

/** KPI tile for the dashboard. `ring` (0..100) draws a progress ring in `color`. */
export function StatCard({
  label,
  value,
  sub,
  icon,
  color = 'brand',
  to,
  ring,
  hint,
}: {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  icon?: ReactNode;
  color?: string;
  to?: string;
  ring?: number;
  hint?: string;
}) {
  const cardProps = {
    h: '100%',
    padding: 'md',
    style: {
      textDecoration: 'none',
      color: 'inherit',
      transition: 'box-shadow 150ms ease, transform 150ms ease',
    },
    className: to ? 'myr-stat-link' : undefined,
  } as const;
  const content = (
    <>
      <Group justify="space-between" wrap="nowrap" align="flex-start">
        <Stack gap={2} style={{ minWidth: 0 }}>
          <Text fz="xs" fw={600} c="dimmed" tt="uppercase" style={{ letterSpacing: '0.05em' }}>
            {label}
          </Text>
          <Text fz={30} fw={700} lh={1.15} className="myr-tabular" data-testid={`stat-${label}`}>
            {value}
          </Text>
          {sub && (
            <Text fz="xs" c="dimmed" component="div">
              {sub}
            </Text>
          )}
        </Stack>
        {ring !== undefined ? (
          <RingProgress
            size={64}
            thickness={7}
            roundCaps
            sections={[{ value: Math.max(0, Math.min(100, ring)), color: `${color}.6` }]}
            label={icon ? <Group justify="center">{icon}</Group> : undefined}
          />
        ) : (
          icon && (
            <ThemeIcon variant="light" color={color} size={40} radius="md">
              {icon}
            </ThemeIcon>
          )
        )}
      </Group>
    </>
  );
  const body = to ? (
    <Card component={Link} to={to} {...cardProps}>
      {content}
    </Card>
  ) : (
    <Card {...cardProps}>{content}</Card>
  );
  return hint ? <Tooltip label={hint}>{body}</Tooltip> : body;
}
