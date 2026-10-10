// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Badge,
  Button,
  Grid,
  Group,
  SegmentedControl,
  Stack,
  Text,
  useMantineColorScheme,
} from '@mantine/core';
import { IconLogout } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { PERMISSIONS } from '@/api/enums';
import { useAuth } from '@/auth/useAuth';
import { KeyValue } from '@/components/misc';
import { SectionCard } from '@/components/SectionCard';
import { isLang, LOCALES, setLanguage } from '@/i18n';
import { useAllowedLangs, useLang } from '@/i18n/useLang';

export default function ProfileTab() {
  const { t } = useTranslation();
  const { me, logout, can } = useAuth();
  const navigate = useNavigate();
  const lang = useLang();
  const allowedLangs = useAllowedLangs();
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  if (!me) return null;
  return (
    <Grid gap="md">
      <Grid.Col span={{ base: 12, lg: 6 }}>
        <SectionCard title={t('settings.profile.account')}>
          <KeyValue
            labelWidth={140}
            items={[
              { label: t('common.name'), value: me.name },
              { label: 'E-Mail', value: me.email ?? '–' },
              {
                label: t('common.role'),
                value: <Badge variant="light">{t(`enum.role.${me.role}`)}</Badge>,
              },
              {
                label: t('settings.profile.authMethod'),
                value: t(`enum.authMethod.${me.authMethod}`),
              },
              { label: 'ID', value: <span className="myr-mono">{me.id}</span> },
            ]}
          />
          <Group mt="md">
            <Button
              color="red"
              variant="light"
              leftSection={<IconLogout size={16} />}
              onClick={() => void logout().then(() => navigate('/login', { replace: true }))}
            >
              {t('header.signOut')}
            </Button>
          </Group>
        </SectionCard>
      </Grid.Col>
      <Grid.Col span={{ base: 12, lg: 6 }}>
        <Stack gap="md">
          <SectionCard title={t('settings.profile.appearance')}>
            <Stack gap="md">
              <Stack gap={4}>
                <Text fz="sm" fw={500}>
                  {t('header.language')}
                </Text>
                <SegmentedControl
                  w="fit-content"
                  value={lang}
                  onChange={(v) => {
                    if (isLang(v)) setLanguage(v);
                  }}
                  data={allowedLangs.map((code) => ({ value: code, label: LOCALES[code].label }))}
                />
              </Stack>
              <Stack gap={4}>
                <Text fz="sm" fw={500}>
                  {t('header.theme')}
                </Text>
                <SegmentedControl
                  w="fit-content"
                  value={colorScheme}
                  onChange={(v) => setColorScheme(v as 'light' | 'dark' | 'auto')}
                  data={[
                    { value: 'light', label: t('header.themeLight') },
                    { value: 'dark', label: t('header.themeDark') },
                    { value: 'auto', label: t('header.themeAuto') },
                  ]}
                />
              </Stack>
            </Stack>
          </SectionCard>
          <SectionCard
            title={t('settings.profile.permissions')}
            description={t('settings.profile.permissionsHint')}
          >
            <Group gap={6} wrap="wrap">
              {PERMISSIONS.map((p) => (
                <Badge
                  key={p}
                  size="sm"
                  color={can(p) ? 'teal' : 'gray'}
                  variant={can(p) ? 'light' : 'outline'}
                  style={{ textTransform: 'none' }}
                >
                  {t(`enum.permission.${p}`)}
                </Badge>
              ))}
            </Group>
          </SectionCard>
        </Stack>
      </Grid.Col>
    </Grid>
  );
}
