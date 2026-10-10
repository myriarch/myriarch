// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  ActionIcon,
  Button,
  Checkbox,
  Group,
  Menu,
  Modal,
  Stack,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { IconBookmark, IconBookmarkPlus, IconTrash, IconUsers } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useCreateSavedFilter, useDeleteSavedFilter, useSavedFilters } from '@/api/hooks/agents';
import type { SavedFilter } from '@/api/types';
import { useAuth } from '@/auth/useAuth';
import { ErrorAlert } from '@/components/states';
import { fromSavedQuery, toSavedQuery, type AgentListState } from './filters';

/** Saved filter menu: apply, delete (own filters) and save the current filter. */
export function SavedFilters({
  state,
  hasFilters,
  onApply,
}: {
  state: AgentListState;
  hasFilters: boolean;
  onApply: (s: AgentListState) => void;
}) {
  const { t } = useTranslation();
  const { me, can } = useAuth();
  const canWrite = can('saved_filters:write');
  const { data } = useSavedFilters('agents');
  const create = useCreateSavedFilter();
  const del = useDeleteSavedFilter();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [shared, setShared] = useState(false);
  const items = data?.items ?? [];

  const save = () => {
    create.mutate(
      { name: name.trim(), page: 'agents', query: toSavedQuery(state), shared },
      {
        onSuccess: () => {
          setOpen(false);
          setName('');
          setShared(false);
        },
      },
    );
  };

  const isOwn = (f: SavedFilter) => f.userId === me?.id;

  return (
    <>
      <Menu position="bottom-end" width={300} shadow="md">
        <Menu.Target>
          <Button
            variant="default"
            size="xs"
            leftSection={<IconBookmark size={14} />}
            data-testid="saved-filters-button"
          >
            {t('agents.savedFilters.title')}
            {items.length > 0 ? ` (${items.length})` : ''}
          </Button>
        </Menu.Target>
        <Menu.Dropdown>
          {items.length === 0 && (
            <Text fz="sm" c="dimmed" p="sm">
              {t('agents.savedFilters.empty')}
            </Text>
          )}
          {items.map((f) => (
            <Menu.Item
              key={f.id}
              onClick={() => onApply(fromSavedQuery(f.query))}
              rightSection={
                isOwn(f) && canWrite ? (
                  <Tooltip label={t('common.delete')}>
                    <ActionIcon
                      component="span"
                      size="sm"
                      variant="subtle"
                      color="red"
                      aria-label={t('common.delete')}
                      onClick={(e: React.MouseEvent) => {
                        e.stopPropagation();
                        del.mutate(f.id);
                      }}
                    >
                      <IconTrash size={14} />
                    </ActionIcon>
                  </Tooltip>
                ) : null
              }
            >
              <Group gap={6} wrap="nowrap">
                <Text fz="sm" truncate>
                  {f.name}
                </Text>
                {f.shared && (
                  <Tooltip label={t('agents.savedFilters.shared')}>
                    <IconUsers size={13} aria-label={t('agents.savedFilters.shared')} />
                  </Tooltip>
                )}
              </Group>
            </Menu.Item>
          ))}
          {canWrite && (
            <>
              <Menu.Divider />
              <Menu.Item
                leftSection={<IconBookmarkPlus size={15} />}
                disabled={!hasFilters}
                onClick={() => setOpen(true)}
              >
                {t('agents.savedFilters.saveCurrent')}
              </Menu.Item>
            </>
          )}
        </Menu.Dropdown>
      </Menu>

      <Modal
        opened={open}
        onClose={() => setOpen(false)}
        title={t('agents.savedFilters.saveCurrent')}
      >
        <Stack gap="md">
          <TextInput
            label={t('common.name')}
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
            maxLength={100}
            data-autofocus
            onKeyDown={(e) => {
              if (e.key === 'Enter' && name.trim()) save();
            }}
          />
          <Checkbox
            label={t('agents.savedFilters.shareWithAll')}
            checked={shared}
            onChange={(e) => setShared(e.currentTarget.checked)}
          />
          <ErrorAlert error={create.error} />
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={save} loading={create.isPending} disabled={!name.trim()}>
              {t('common.save')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}
