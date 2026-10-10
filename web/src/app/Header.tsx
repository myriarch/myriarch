// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  ActionIcon,
  Avatar,
  Badge,
  Burger,
  Divider,
  Group,
  Menu,
  Text,
  TextInput,
  Tooltip,
  useMantineColorScheme,
} from '@mantine/core';
import {
  IconCheck,
  IconDeviceDesktop,
  IconLanguage,
  IconLogout,
  IconMoon,
  IconSearch,
  IconSun,
  IconUser,
} from '@tabler/icons-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { useAuth } from '@/auth/useAuth';
import { NAME } from '@/brand';
import { Logo } from '@/components/Logo';
import { LOCALES, setLanguage } from '@/i18n';
import { useAllowedLangs, useLang } from '@/i18n/useLang';
import { LiveIndicator } from './LiveIndicator';

function initials(name: string): string {
  const parts = name.split(/[\s@.]+/).filter(Boolean);
  return ((parts[0]?.[0] ?? '?') + (parts[1]?.[0] ?? '')).toUpperCase();
}

export function ThemeMenu() {
  const { t } = useTranslation();
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  const Icon =
    colorScheme === 'dark' ? IconMoon : colorScheme === 'light' ? IconSun : IconDeviceDesktop;
  const items = [
    { value: 'light', label: t('header.themeLight'), icon: <IconSun size={16} /> },
    { value: 'dark', label: t('header.themeDark'), icon: <IconMoon size={16} /> },
    { value: 'auto', label: t('header.themeAuto'), icon: <IconDeviceDesktop size={16} /> },
  ] as const;
  return (
    <Menu position="bottom-end" width={190}>
      <Menu.Target>
        <Tooltip label={t('header.theme')}>
          <ActionIcon variant="subtle" color="gray" size="lg" aria-label={t('header.theme')}>
            <Icon size={19} />
          </ActionIcon>
        </Tooltip>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Label>{t('header.theme')}</Menu.Label>
        {items.map((i) => (
          <Menu.Item
            key={i.value}
            leftSection={i.icon}
            rightSection={colorScheme === i.value ? <IconCheck size={14} /> : null}
            onClick={() => setColorScheme(i.value)}
          >
            {i.label}
          </Menu.Item>
        ))}
      </Menu.Dropdown>
    </Menu>
  );
}

export function LanguageMenu() {
  const { t } = useTranslation();
  const lang = useLang();
  const allowed = useAllowedLangs();
  if (allowed.length < 2) return null;
  return (
    <Menu position="bottom-end" width={170}>
      <Menu.Target>
        <Tooltip label={t('header.language')}>
          <ActionIcon variant="subtle" color="gray" size="lg" aria-label={t('header.language')}>
            <IconLanguage size={19} />
          </ActionIcon>
        </Tooltip>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Label>{t('header.language')}</Menu.Label>
        {allowed.map((code) => (
          <Menu.Item
            key={code}
            rightSection={lang === code ? <IconCheck size={14} /> : null}
            onClick={() => setLanguage(code)}
          >
            {LOCALES[code].label}
          </Menu.Item>
        ))}
      </Menu.Dropdown>
    </Menu>
  );
}

export function Header({
  navOpened,
  onToggleNav,
}: {
  navOpened: boolean;
  onToggleNav: () => void;
}) {
  const { t } = useTranslation();
  const { me, logout } = useAuth();
  const navigate = useNavigate();
  const [q, setQ] = useState('');

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const term = q.trim();
    void navigate(term ? `/agents?q=${encodeURIComponent(term)}` : '/agents');
    setQ('');
  };

  return (
    <Group h="100%" px="md" justify="space-between" wrap="nowrap">
      <Group gap="sm" wrap="nowrap">
        <Burger
          opened={navOpened}
          onClick={onToggleNav}
          hiddenFrom="sm"
          size="sm"
          aria-label={navOpened ? t('nav.closeMenu') : t('nav.openMenu')}
        />
        <Link to="/" style={{ textDecoration: 'none', color: 'inherit' }} aria-label={NAME}>
          <Logo />
        </Link>
        {import.meta.env.VITE_MOCK === '1' && (
          <Tooltip label={t('header.mockModeHint')}>
            <Badge color="orange" variant="light" size="sm" visibleFrom="xs">
              {t('header.mockMode')}
            </Badge>
          </Tooltip>
        )}
      </Group>

      <form onSubmit={submit} role="search" style={{ flex: '0 1 380px' }}>
        <TextInput
          size="xs"
          radius="xl"
          placeholder={t('header.searchPlaceholder')}
          aria-label={t('header.searchPlaceholder')}
          leftSection={<IconSearch size={14} />}
          value={q}
          onChange={(e) => setQ(e.currentTarget.value)}
          visibleFrom="md"
        />
      </form>

      <Group gap={4} wrap="nowrap">
        <LiveIndicator />
        <Divider orientation="vertical" mx={6} />
        <LanguageMenu />
        <ThemeMenu />
        <Menu position="bottom-end" width={240}>
          <Menu.Target>
            <ActionIcon variant="subtle" size="lg" radius="xl" aria-label={t('header.account')}>
              <Avatar size={30} radius="xl" color="brand" variant="filled">
                {me ? initials(me.name) : <IconUser size={16} />}
              </Avatar>
            </ActionIcon>
          </Menu.Target>
          <Menu.Dropdown>
            {me && (
              <div style={{ padding: '8px 12px' }}>
                <Text fw={600} fz="sm" truncate>
                  {me.name}
                </Text>
                {me.email && (
                  <Text fz="xs" c="dimmed" truncate>
                    {me.email}
                  </Text>
                )}
                <Badge mt={6} size="xs" variant="light">
                  {t(`enum.role.${me.role}`)}
                </Badge>
              </div>
            )}
            <Menu.Divider />
            <Menu.Item leftSection={<IconUser size={16} />} component={Link} to="/settings/profile">
              {t('settings.tabs.profile')}
            </Menu.Item>
            <Menu.Item
              color="red"
              leftSection={<IconLogout size={16} />}
              onClick={() => {
                void logout().then(() => navigate('/login', { replace: true }));
              }}
            >
              {t('header.signOut')}
            </Menu.Item>
          </Menu.Dropdown>
        </Menu>
      </Group>
    </Group>
  );
}
