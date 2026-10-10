// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Alert,
  Anchor,
  Box,
  Button,
  Card,
  Center,
  Divider,
  Group,
  PasswordInput,
  SegmentedControl,
  Stack,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { useForm } from '@mantine/form';
import {
  IconAlertCircle,
  IconAlertTriangle,
  IconLogin2,
  IconShieldLock,
} from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Navigate, useSearchParams } from 'react-router';
import { safeRedirectPath, useAuth } from '@/auth/useAuth';
import { NAME } from '@/brand';
import { LogoMark } from '@/components/Logo';
import { ErrorAlert, ErrorState } from '@/components/states';
import { LanguageMenu, ThemeMenu } from '@/app/Header';
import { FullPageLoader } from '@/app/RequireAuth';

type BootstrapMode = 'password' | 'token';

/**
 * Break-glass sign-in of the bootstrap admin (auth.bootstrapAdmin). It is the only method on a
 * fresh installation; next to single sign-on or the dev login it stays behind a toggle, and its
 * inputs are not in the DOM while collapsed.
 */
function BootstrapSection({
  passwordEnabled,
  tokenEnabled,
  collapsible,
}: {
  passwordEnabled: boolean;
  tokenEnabled: boolean;
  collapsible: boolean;
}) {
  const { t } = useTranslation();
  const { bootstrapLogin } = useAuth();
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState<BootstrapMode>(passwordEnabled ? 'password' : 'token');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const expanded = !collapsible || open;
  const activeMode: BootstrapMode =
    mode === 'password' && !passwordEnabled
      ? 'token'
      : mode === 'token' && !tokenEnabled
        ? 'password'
        : mode;

  const form = useForm({
    initialValues: { username: '', password: '', token: '' },
    validate: (values) =>
      activeMode === 'password'
        ? {
            username: values.username.trim() ? null : t('login.usernameRequired'),
            password: values.password ? null : t('login.passwordRequired'),
          }
        : { token: values.token.trim() ? null : t('login.bootstrap.tokenRequired') },
  });

  const submit = form.onSubmit(async (values) => {
    setSubmitting(true);
    setError(null);
    try {
      await bootstrapLogin(
        activeMode === 'password'
          ? { username: values.username.trim(), password: values.password }
          : { token: values.token.trim() },
      );
    } catch (e) {
      setError(e);
    } finally {
      setSubmitting(false);
    }
  });

  const changeMode = (m: string) => {
    setMode(m as BootstrapMode);
    setError(null);
    form.clearErrors();
  };

  return (
    <Stack gap="sm" data-testid="bootstrap-login">
      {collapsible && (
        <Anchor
          component="button"
          type="button"
          fz="sm"
          ta="center"
          aria-expanded={expanded}
          aria-controls="bootstrap-login-form"
          onClick={() => setOpen((v) => !v)}
        >
          {t('login.bootstrap.toggle')}
        </Anchor>
      )}
      {expanded && (
        <form id="bootstrap-login-form" onSubmit={submit} noValidate>
          <Stack gap="sm">
            <Alert
              color="orange"
              variant="light"
              p="xs"
              fz="xs"
              icon={<IconAlertTriangle size={16} />}
            >
              {t('login.bootstrap.warning')}
            </Alert>
            {passwordEnabled && tokenEnabled && (
              <SegmentedControl
                size="xs"
                fullWidth
                value={activeMode}
                onChange={changeMode}
                data={[
                  { value: 'password', label: t('login.bootstrap.modePassword') },
                  { value: 'token', label: t('login.bootstrap.modeToken') },
                ]}
              />
            )}
            {activeMode === 'password' ? (
              <>
                <TextInput
                  label={t('login.username')}
                  autoComplete="username"
                  {...form.getInputProps('username')}
                />
                <PasswordInput
                  label={t('login.password')}
                  autoComplete="current-password"
                  {...form.getInputProps('password')}
                />
              </>
            ) : (
              <PasswordInput
                label={t('login.bootstrap.token')}
                description={t('login.bootstrap.tokenHint')}
                autoComplete="one-time-code"
                {...form.getInputProps('token')}
              />
            )}
            <ErrorAlert error={error} />
            <Button type="submit" loading={submitting} variant={collapsible ? 'default' : 'filled'}>
              {t('login.signIn')}
            </Button>
          </Stack>
        </form>
      )}
    </Stack>
  );
}

export default function LoginPage() {
  const { t } = useTranslation();
  const { status, config, devLogin, reload, error: authError } = useAuth();
  const [params] = useSearchParams();
  const redirect = safeRedirectPath(params.get('redirect'));
  const errorParam = params.get('error');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const form = useForm({
    initialValues: { username: '', password: '' },
    validate: {
      username: (v) => (v.trim() ? null : t('login.usernameRequired')),
      password: (v) => (v ? null : t('login.passwordRequired')),
    },
  });

  if (status === 'authenticated') return <Navigate to={redirect} replace />;
  if (status === 'loading') return <FullPageLoader />;
  if (status === 'error' || !config) {
    return (
      <Center h="100vh">
        <ErrorState error={authError} onRetry={reload} />
      </Center>
    );
  }

  const bootstrapOffered = config.bootstrapPasswordEnabled || config.bootstrapTokenEnabled;
  const otherMethod = config.oidcEnabled || config.devLoginEnabled;
  const noMethod = !otherMethod && !bootstrapOffered;

  const submit = form.onSubmit(async (values) => {
    setSubmitting(true);
    setError(null);
    try {
      await devLogin(values.username.trim(), values.password);
    } catch (e) {
      setError(e);
    } finally {
      setSubmitting(false);
    }
  });

  return (
    <Box
      mih="100vh"
      style={{
        background:
          'radial-gradient(1200px 500px at 15% -10%, color-mix(in srgb, var(--mantine-color-brand-5) 22%, transparent), transparent), radial-gradient(900px 500px at 100% 110%, color-mix(in srgb, var(--mantine-color-violet-5) 18%, transparent), transparent), var(--myr-surface-muted)',
      }}
    >
      <Group justify="flex-end" p="md" gap={4}>
        <LanguageMenu />
        <ThemeMenu />
      </Group>
      <Center mih="calc(100vh - 80px)" px="md" pb="xl">
        <Card w="100%" maw={420} p="xl" shadow="md" radius="lg">
          <Stack gap="lg">
            <Stack align="center" gap={6}>
              <LogoMark size={52} />
              <Title order={2}>{t('login.title', { name: NAME })}</Title>
              <Text c="dimmed" fz="sm" ta="center">
                {t('login.subtitle')}
              </Text>
            </Stack>

            {errorParam && (
              <Alert color="red" variant="light" icon={<IconAlertCircle size={18} />} role="alert">
                {errorParam === 'access_denied' ? t('login.accessDenied') : t('login.oidcError')}
              </Alert>
            )}
            {params.get('redirect') && !errorParam && (
              <Alert color="blue" variant="light" icon={<IconShieldLock size={18} />}>
                {t('login.sessionRequired')}
              </Alert>
            )}

            {config.oidcEnabled && (
              <Button
                size="md"
                leftSection={<IconLogin2 size={18} />}
                component="a"
                href={`/api/v1/auth/login?redirect=${encodeURIComponent(redirect)}`}
              >
                {t('login.sso', { provider: config.oidcDisplayName || 'SSO' })}
              </Button>
            )}

            {config.oidcEnabled && config.devLoginEnabled && (
              <Divider label={t('login.or')} labelPosition="center" />
            )}

            {config.devLoginEnabled && (
              <form onSubmit={submit} noValidate>
                <Stack gap="sm">
                  <Alert color="yellow" variant="light" p="xs" fz="xs">
                    {t('login.devWarning')}
                  </Alert>
                  <TextInput
                    label={t('login.username')}
                    autoComplete="username"
                    {...form.getInputProps('username')}
                    data-autofocus
                  />
                  <PasswordInput
                    label={t('login.password')}
                    autoComplete="current-password"
                    {...form.getInputProps('password')}
                  />
                  <ErrorAlert error={error} />
                  <Button
                    type="submit"
                    loading={submitting}
                    variant={config.oidcEnabled ? 'default' : 'filled'}
                  >
                    {t('login.signIn')}
                  </Button>
                  {import.meta.env.VITE_MOCK === '1' && (
                    <Text fz="xs" c="dimmed" ta="center">
                      {t('login.mockHint')}{' '}
                      <Anchor
                        component="button"
                        type="button"
                        fz="xs"
                        onClick={() => form.setValues({ username: 'admin', password: 'admin' })}
                      >
                        {t('login.mockFill')}
                      </Anchor>
                    </Text>
                  )}
                </Stack>
              </form>
            )}

            {bootstrapOffered && (
              <>
                {otherMethod && <Divider />}
                <BootstrapSection
                  passwordEnabled={config.bootstrapPasswordEnabled}
                  tokenEnabled={config.bootstrapTokenEnabled}
                  collapsible={otherMethod}
                />
              </>
            )}

            {noMethod && (
              <Alert color="orange" variant="light" icon={<IconAlertCircle size={18} />}>
                {t('login.noMethod')}
              </Alert>
            )}
          </Stack>
        </Card>
      </Center>
    </Box>
  );
}
