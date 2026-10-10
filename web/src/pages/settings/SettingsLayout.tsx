// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Skeleton, Tabs } from '@mantine/core';
import { IconBell, IconKey, IconServerCog, IconUser, IconUsers } from '@tabler/icons-react';
import { Suspense } from 'react';
import { useTranslation } from 'react-i18next';
import { Outlet, useLocation, useNavigate } from 'react-router';
import { useAuth } from '@/auth/useAuth';
import { PageHeader } from '@/components/PageHeader';

export default function SettingsLayout() {
  const { t } = useTranslation();
  const { can } = useAuth();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const current = pathname.split('/')[2] ?? 'system';
  const showTokens = can('tokens:manage') || can('enrollment:manage');
  const showUsers = can('users:manage');

  return (
    <>
      <PageHeader title={t('settings.title')} subtitle={t('settings.subtitle')} />
      <Tabs
        value={current}
        onChange={(v) => v && void navigate(`/settings/${v}`)}
        keepMounted={false}
      >
        <Tabs.List mb="md" style={{ flexWrap: 'nowrap', overflowX: 'auto' }}>
          <Tabs.Tab value="system" leftSection={<IconServerCog size={16} />}>
            {t('settings.tabs.system')}
          </Tabs.Tab>
          <Tabs.Tab value="notifications" leftSection={<IconBell size={16} />}>
            {t('settings.tabs.notifications')}
          </Tabs.Tab>
          {showTokens && (
            <Tabs.Tab value="tokens" leftSection={<IconKey size={16} />}>
              {t('settings.tabs.tokens')}
            </Tabs.Tab>
          )}
          {showUsers && (
            <Tabs.Tab value="users" leftSection={<IconUsers size={16} />}>
              {t('settings.tabs.users')}
            </Tabs.Tab>
          )}
          <Tabs.Tab value="profile" leftSection={<IconUser size={16} />}>
            {t('settings.tabs.profile')}
          </Tabs.Tab>
        </Tabs.List>
      </Tabs>
      <Suspense fallback={<Skeleton height={240} />}>
        <Outlet />
      </Suspense>
    </>
  );
}
