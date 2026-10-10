// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Alert, AppShell, Box, NavLink, ScrollArea, Skeleton, Stack, Text } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { IconAlertTriangle } from '@tabler/icons-react';
import { Suspense, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, Outlet, useLocation } from 'react-router';
import { useSystemInfo } from '@/api/hooks/overview';
import { useAuth } from '@/auth/useAuth';
import { SERVER_BINARY } from '@/brand';
import { ErrorBoundary } from '@/components/ErrorBoundary';
import { Header } from './Header';
import { isNavActive, NAV } from './nav';

function PageFallback() {
  return (
    <Stack gap="md" aria-busy="true">
      <Skeleton height={32} width={260} />
      <Skeleton height={16} width={420} />
      <Skeleton height={220} />
    </Stack>
  );
}

/** Shown on every page while the session belongs to the break-glass bootstrap admin. */
function BootstrapBanner() {
  const { t } = useTranslation();
  const { me } = useAuth();
  if (me?.authMethod !== 'bootstrap') return null;
  return (
    <Alert
      data-testid="bootstrap-banner"
      role="status"
      color="orange"
      variant="light"
      radius="md"
      mb="md"
      icon={<IconAlertTriangle size={18} />}
      title={t('bootstrapBanner.title')}
    >
      {t('bootstrapBanner.text')}
    </Alert>
  );
}

function VersionFooter() {
  const { data } = useSystemInfo();
  if (!data) return null;
  return (
    <Text fz="xs" c="dimmed" ta="center" className="myr-tabular" py="sm">
      {SERVER_BINARY} {data.version}
    </Text>
  );
}

export function Layout() {
  const { t } = useTranslation();
  const { can } = useAuth();
  const { pathname } = useLocation();
  const [opened, { toggle, close }] = useDisclosure(false);

  // Close the mobile drawer on navigation.
  useEffect(() => {
    close();
  }, [pathname, close]);

  return (
    <AppShell
      header={{ height: 'var(--myr-header-height)' }}
      navbar={{ width: 'var(--myr-nav-width)', breakpoint: 'sm', collapsed: { mobile: !opened } }}
      padding={{ base: 'md', md: 'lg' }}
    >
      <a href="#main" className="myr-skip-link">
        {t('nav.skipToContent')}
      </a>
      <AppShell.Header>
        <Header navOpened={opened} onToggleNav={toggle} />
      </AppShell.Header>

      <AppShell.Navbar aria-label={t('nav.mainNav')}>
        <AppShell.Section grow component={ScrollArea} p="sm">
          <Stack gap="md">
            {NAV.map((group) => {
              const items = group.items.filter((i) => !i.permission || can(i.permission));
              if (items.length === 0) return null;
              return (
                <Box key={group.labelKey}>
                  <Text
                    fz={11}
                    fw={700}
                    c="dimmed"
                    tt="uppercase"
                    px="sm"
                    mb={4}
                    style={{ letterSpacing: '0.06em' }}
                  >
                    {t(group.labelKey)}
                  </Text>
                  <Stack gap={2}>
                    {items.map((item) => (
                      <NavLink
                        key={item.to}
                        component={Link}
                        to={item.to}
                        label={t(item.labelKey)}
                        leftSection={item.icon}
                        active={isNavActive(item, pathname)}
                        variant="light"
                        style={{ borderRadius: 'var(--mantine-radius-md)' }}
                        aria-current={isNavActive(item, pathname) ? 'page' : undefined}
                      />
                    ))}
                  </Stack>
                </Box>
              );
            })}
          </Stack>
        </AppShell.Section>
        <AppShell.Section>
          <VersionFooter />
        </AppShell.Section>
      </AppShell.Navbar>

      <AppShell.Main id="main" style={{ background: 'var(--myr-surface-muted)' }}>
        <BootstrapBanner />
        <div className="myr-page">
          <ErrorBoundary resetKey={pathname}>
            <Suspense fallback={<PageFallback />}>
              <Outlet />
            </Suspense>
          </ErrorBoundary>
        </div>
      </AppShell.Main>
    </AppShell>
  );
}
