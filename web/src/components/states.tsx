// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Alert,
  Box,
  Button,
  Center,
  Code,
  Collapse,
  Group,
  Skeleton,
  Stack,
  Text,
  ThemeIcon,
  Title,
} from '@mantine/core';
import { IconAlertCircle, IconInbox, IconLock, IconRefresh } from '@tabler/icons-react';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { formatApiError } from '@/lib/problem';
import { useAuth } from '@/auth/useAuth';
import type { Permission } from '@/api/types';

export function EmptyState({
  icon,
  title,
  text,
  action,
  compact,
}: {
  icon?: ReactNode;
  title: ReactNode;
  text?: ReactNode;
  action?: ReactNode;
  compact?: boolean;
}) {
  return (
    <Center py={compact ? 'lg' : 48} px="md">
      <Stack align="center" gap="xs" maw={440} ta="center">
        <ThemeIcon size={compact ? 40 : 56} radius="xl" variant="light" color="gray">
          {icon ?? <IconInbox size={compact ? 22 : 28} />}
        </ThemeIcon>
        <Text fw={600} fz={compact ? 'sm' : 'md'}>
          {title}
        </Text>
        {text && (
          <Text c="dimmed" fz="sm">
            {text}
          </Text>
        )}
        {action}
      </Stack>
    </Center>
  );
}

export function ErrorState({
  error,
  onRetry,
  title,
  compact,
}: {
  error: unknown;
  onRetry?: () => void;
  title?: string;
  compact?: boolean;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const f = formatApiError(t, error);
  return (
    <Center py={compact ? 'md' : 48} px="md" role="alert">
      <Stack align="center" gap="xs" maw={520} ta="center">
        <ThemeIcon size={compact ? 40 : 56} radius="xl" variant="light" color="red">
          <IconAlertCircle size={compact ? 22 : 28} />
        </ThemeIcon>
        <Text fw={600}>{title ?? t('errors.loadFailed')}</Text>
        <Text c="dimmed" fz="sm">
          {f.title}
          {f.detail ? ` ${f.detail}` : ''}
        </Text>
        <Group gap="xs">
          {onRetry && (
            <Button
              variant="light"
              leftSection={<IconRefresh size={16} />}
              onClick={onRetry}
              size="xs"
            >
              {t('common.retry')}
            </Button>
          )}
          {(f.requestId || f.issues.length > 0) && (
            <Button variant="subtle" color="gray" size="xs" onClick={() => setOpen((o) => !o)}>
              {t('errors.technicalDetails')}
            </Button>
          )}
        </Group>
        <Collapse expanded={open}>
          <Box ta="left">
            {f.requestId && (
              <Text fz="xs" c="dimmed">
                {t('errors.requestId')}: <Code>{f.requestId}</Code>
              </Text>
            )}
            {f.issues.map((i, idx) => (
              <Text key={idx} fz="xs" c="dimmed">
                {i.code}: {i.message}
              </Text>
            ))}
          </Box>
        </Collapse>
      </Stack>
    </Center>
  );
}

/** Inline error for dialogs/forms (shows the localized headline plus per-field issues). */
export function ErrorAlert({ error }: { error: unknown }) {
  const { t } = useTranslation();
  if (!error) return null;
  const f = formatApiError(t, error);
  return (
    <Alert color="red" variant="light" icon={<IconAlertCircle size={18} />} title={f.title}>
      {f.detail && <Text fz="sm">{f.detail}</Text>}
      {f.issues.map((i, idx) => (
        <Text key={idx} fz="sm">
          {i.path ? `${i.path}: ` : ''}
          {i.message}
        </Text>
      ))}
    </Alert>
  );
}

export function NoPermission({ permission }: { permission: Permission }) {
  const { t } = useTranslation();
  const { me } = useAuth();
  return (
    <EmptyState
      icon={<IconLock size={28} />}
      title={t('noPermission.title')}
      text={t('noPermission.text', {
        permission: t(`enum.permission.${permission}`),
        role: me ? t(`enum.role.${me.role}`) : '–',
      })}
    />
  );
}

export function TableSkeleton({ rows = 8, cols = 6 }: { rows?: number; cols?: number }) {
  return (
    <Stack gap={0} aria-busy="true">
      {Array.from({ length: rows }, (_, r) => (
        <Group
          key={r}
          gap="md"
          px="md"
          py={10}
          wrap="nowrap"
          style={{ borderBottom: '1px solid var(--myr-border)' }}
        >
          {Array.from({ length: cols }, (_, c) => (
            <Skeleton key={c} height={14} style={{ flex: c === 0 ? 2 : 1 }} />
          ))}
        </Group>
      ))}
    </Stack>
  );
}

export function CardSkeleton({ height = 120 }: { height?: number }) {
  return <Skeleton height={height} radius="md" aria-busy="true" />;
}

export function PageTitle({ children }: { children: ReactNode }) {
  return <Title order={1}>{children}</Title>;
}
