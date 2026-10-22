// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Alert,
  Anchor,
  Autocomplete,
  Badge,
  Button,
  Checkbox,
  Code,
  Collapse,
  Group,
  List,
  Modal,
  PasswordInput,
  SegmentedControl,
  Skeleton,
  Stack,
  Text,
  TextInput,
} from '@mantine/core';
import { IconAlertTriangle, IconInfoCircle, IconKey } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useCreateEnrollmentToken } from '@/api/hooks/admin';
import { useInstallBundles, useRenderInstallCommands } from '@/api/hooks/install';
import { isApiError } from '@/api/errors';
import type { InstallBundle, InstallCommands } from '@/api/types';
import { useAuth } from '@/auth/useAuth';
import { WEBSITE } from '@/brand';
import { ErrorAlert, ErrorState } from './states';
import { CopyTextButton } from './CopyButton';
import {
  buildRequest,
  insertToken,
  looksLikeToken,
  maskedToken,
  platformLabel,
  platformOf,
  renderErrorKind,
  type AddHostForm,
} from './addHost';

const GUIDE_URL = `${WEBSITE}/docs/tasks/connect-a-host/`;
const SITE_TYPES = ['edge', 'office', 'datacenter', 'k8s-gateway'];

type TokenMode = 'fresh' | 'create' | 'paste';

/**
 * The dialog "Add host": one command for each platform, to copy to the host.
 *
 * The token (ADR-0046, decision 6) lives only in the state of AddHostModal. Nothing writes it to storage, a URL or a log, the
 * render request never carries it (the server renders a placeholder, the browser fills it in), and the component is
 * unmounted when the dialog closes, which drops the state. `freshToken` is a token that the caller just created; the
 * caller owns that value and clears it when its own dialog closes.
 */
export function AddHostDialog({
  opened,
  onClose,
  freshToken,
}: {
  opened: boolean;
  onClose: () => void;
  freshToken?: string;
}) {
  return opened ? <AddHostModal onClose={onClose} freshToken={freshToken} /> : null;
}

function BundleBadge({ bundle }: { bundle: InstallBundle }) {
  const { t } = useTranslation();
  const color = bundle.status === 'ready' ? 'teal' : bundle.status === 'failed' ? 'red' : 'yellow';
  return (
    <Badge size="xs" variant="light" color={color}>
      {t(`addHost.bundleState.${bundle.status}`)}
    </Badge>
  );
}

function AddHostModal({ onClose, freshToken }: { onClose: () => void; freshToken?: string }) {
  const { t } = useTranslation();
  const { can } = useAuth();
  const canCreate = can('enrollment:manage');
  const bundles = useInstallBundles();
  const render = useRenderInstallCommands();
  const create = useCreateEnrollmentToken({ discard: true });

  const [form, setForm] = useState<AddHostForm>({
    variant: freshToken || canCreate ? 'token_inline' : 'token_file',
    platforms: [],
    fleet: '',
    siteType: 'edge',
    siteId: '',
    siteName: '',
    siteCountry: '',
    siteRegion: '',
    siteTimezone: '',
    environment: '',
    linuxTokenFile: '',
    windowsTokenFile: '',
  });
  const [platformsTouched, setPlatformsTouched] = useState(false);
  const [more, setMore] = useState(false);
  const [mode, setMode] = useState<TokenMode>(
    freshToken ? 'fresh' : canCreate ? 'create' : 'paste',
  );
  const [tokenName, setTokenName] = useState('');
  const [created, setCreated] = useState('');
  const [pasted, setPasted] = useState('');
  const [rendered, setRendered] = useState<{ key: string; data: InstallCommands } | null>(null);
  const [lastKey, setLastKey] = useState<string | null>(null);

  const set = <K extends keyof AddHostForm>(k: K, v: AddHostForm[K]) =>
    setForm((f) => ({ ...f, [k]: v }));

  const items = bundles.data?.items ?? [];
  const available = items.map(platformOf);
  // Without a choice of the person every platform of the server is selected.
  const selected = platformsTouched ? form.platforms : available;
  const effective: AddHostForm = { ...form, platforms: selected };
  const request = buildRequest(effective);
  const key = JSON.stringify(request);

  const inline = form.variant === 'token_inline';
  const token = mode === 'fresh' ? (freshToken ?? '') : mode === 'create' ? created : pasted.trim();
  const tokenOk = mode === 'paste' ? looksLikeToken(token) : token !== '';
  const needsLinuxFile = selected.some((p) => !p.startsWith('windows/'));
  const needsWindowsFile = selected.some((p) => p.startsWith('windows/'));
  // The server wants a token file path for every operating system of the rendered platforms.
  const filesOk =
    inline ||
    ((!needsLinuxFile || form.linuxTokenFile.trim() !== '') &&
      (!needsWindowsFile || form.windowsTokenFile.trim() !== ''));
  const ready =
    form.siteType.trim() !== '' && form.siteId.trim() !== '' && selected.length > 0 && filesOk;

  const submit = () => {
    setLastKey(key);
    render.mutate(request, { onSuccess: (data) => setRendered({ key, data }) });
  };
  const createToken = () =>
    create.mutate(
      { name: tokenName.trim() },
      {
        onSuccess: (res) => {
          setCreated(res.secret);
          create.reset();
        },
      },
    );

  const noBundles = bundles.data && bundles.data.source === 'none';
  const disabled = isApiError(bundles.error) && bundles.error.status === 404;
  const shown = rendered && rendered.key === key ? rendered.data : null;
  const showError = render.error && lastKey === key ? render.error : null;

  let body;
  if (bundles.isPending) {
    body = <Skeleton height={180} />;
  } else if (disabled) {
    body = (
      <Alert color="gray" variant="light" icon={<IconInfoCircle size={18} />}>
        {t('addHost.state.disabled')}
      </Alert>
    );
  } else if (bundles.error && !bundles.data) {
    body = <ErrorState compact error={bundles.error} onRetry={() => void bundles.refetch()} />;
  } else if (noBundles) {
    body = (
      <Alert
        color="yellow"
        variant="light"
        icon={<IconInfoCircle size={18} />}
        title={t('addHost.state.noBundleTitle')}
        data-testid="add-host-no-bundle"
      >
        <Text fz="sm">{t('addHost.state.noBundleText')}</Text>
        <Anchor fz="sm" href={GUIDE_URL} target="_blank" rel="noreferrer">
          {t('addHost.manualGuide')}
        </Anchor>
      </Alert>
    );
  } else {
    body = (
      <Stack gap="md">
        <Checkbox.Group
          label={t('addHost.platforms')}
          description={t('addHost.platformsHint')}
          value={selected}
          onChange={(v) => {
            setPlatformsTouched(true);
            set('platforms', v);
          }}
        >
          <Group mt="xs" gap="lg">
            {items.map((b) => (
              <Checkbox
                key={platformOf(b)}
                value={platformOf(b)}
                label={
                  <Group gap={6}>
                    {platformLabel(platformOf(b))}
                    <BundleBadge bundle={b} />
                  </Group>
                }
              />
            ))}
          </Group>
        </Checkbox.Group>

        <Group grow align="flex-start">
          <Autocomplete
            label={t('addHost.siteType')}
            data={SITE_TYPES}
            value={form.siteType}
            onChange={(v) => set('siteType', v)}
            required
          />
          <TextInput
            label={t('addHost.siteId')}
            description={t('addHost.siteIdHint')}
            value={form.siteId}
            onChange={(e) => set('siteId', e.currentTarget.value)}
            maxLength={128}
            required
            data-autofocus
          />
        </Group>
        <TextInput
          label={t('addHost.fleet')}
          description={t('addHost.fleetHint')}
          placeholder="linux-hosts"
          value={form.fleet}
          onChange={(e) => set('fleet', e.currentTarget.value)}
          maxLength={128}
        />
        <Anchor
          component="button"
          type="button"
          fz="sm"
          onClick={() => setMore((m) => !m)}
          ta="left"
        >
          {more ? t('addHost.lessSite') : t('addHost.moreSite')}
        </Anchor>
        <Collapse expanded={more}>
          <Stack gap="xs">
            <Group grow align="flex-start">
              <TextInput
                label={t('addHost.siteName')}
                value={form.siteName}
                onChange={(e) => set('siteName', e.currentTarget.value)}
              />
              <TextInput
                label={t('addHost.environment')}
                placeholder="prod"
                value={form.environment}
                onChange={(e) => set('environment', e.currentTarget.value)}
              />
            </Group>
            <Group grow align="flex-start">
              <TextInput
                label={t('addHost.siteCountry')}
                placeholder="NL"
                value={form.siteCountry}
                onChange={(e) => set('siteCountry', e.currentTarget.value)}
              />
              <TextInput
                label={t('addHost.siteRegion')}
                value={form.siteRegion}
                onChange={(e) => set('siteRegion', e.currentTarget.value)}
              />
              <TextInput
                label={t('addHost.siteTimezone')}
                placeholder="Europe/Amsterdam"
                value={form.siteTimezone}
                onChange={(e) => set('siteTimezone', e.currentTarget.value)}
              />
            </Group>
          </Stack>
        </Collapse>

        <Stack gap="xs">
          <Text fz="sm" fw={500}>
            {t('addHost.variant')}
          </Text>
          <SegmentedControl
            size="xs"
            w="fit-content"
            value={form.variant}
            onChange={(v) => set('variant', v as AddHostForm['variant'])}
            data={[
              { value: 'token_inline', label: t('addHost.variantInline') },
              { value: 'token_file', label: t('addHost.variantFile') },
            ]}
          />
          <Text fz="xs" c="dimmed">
            {inline ? t('addHost.variantInlineHint') : t('addHost.variantFileHint')}
          </Text>
        </Stack>

        {inline ? (
          <Stack gap="xs" data-testid="add-host-token">
            <Text fz="sm" fw={500}>
              {t('addHost.token.title')}
            </Text>
            {!canCreate && (
              <Alert color="blue" variant="light" icon={<IconInfoCircle size={18} />}>
                {t('addHost.token.noPermission')}
              </Alert>
            )}
            {(freshToken || canCreate) && (
              <SegmentedControl
                size="xs"
                w="fit-content"
                value={mode}
                onChange={(v) => setMode(v as TokenMode)}
                data={[
                  freshToken
                    ? { value: 'fresh', label: t('addHost.token.modeFresh') }
                    : { value: 'create', label: t('addHost.token.modeCreate') },
                  { value: 'paste', label: t('addHost.token.modePaste') },
                ]}
              />
            )}
            {mode === 'fresh' && (
              <Text fz="xs" c="dimmed">
                {t('addHost.token.freshHint')}
              </Text>
            )}
            {mode === 'create' &&
              (created ? (
                <Alert color="teal" variant="light" icon={<IconKey size={18} />}>
                  {t('addHost.token.created')}
                </Alert>
              ) : (
                <Stack gap="xs">
                  <Group align="flex-end" wrap="nowrap">
                    <TextInput
                      style={{ flex: 1 }}
                      label={t('addHost.token.name')}
                      description={t('addHost.token.nameHint')}
                      value={tokenName}
                      onChange={(e) => setTokenName(e.currentTarget.value)}
                      maxLength={100}
                    />
                    <Button
                      onClick={createToken}
                      loading={create.isPending}
                      disabled={!tokenName.trim()}
                    >
                      {t('addHost.token.create')}
                    </Button>
                  </Group>
                  <ErrorAlert error={create.error} />
                </Stack>
              ))}
            {mode === 'paste' && (
              <PasswordInput
                label={t('addHost.token.paste')}
                description={t('addHost.token.pasteHint')}
                placeholder="myre_…"
                value={pasted}
                onChange={(e) => setPasted(e.currentTarget.value)}
                autoComplete="off"
                error={pasted.trim() && !tokenOk ? t('addHost.token.invalid') : undefined}
              />
            )}
          </Stack>
        ) : (
          <Group grow align="flex-start">
            {needsLinuxFile && (
              <TextInput
                label={t('addHost.tokenFileLinux')}
                placeholder="/etc/myriarch/enrollment-token"
                value={form.linuxTokenFile}
                onChange={(e) => set('linuxTokenFile', e.currentTarget.value)}
                required
              />
            )}
            {needsWindowsFile && (
              <TextInput
                label={t('addHost.tokenFileWindows')}
                placeholder="C:\ProgramData\Secrets\myriarch-token.txt"
                value={form.windowsTokenFile}
                onChange={(e) => set('windowsTokenFile', e.currentTarget.value)}
                required
              />
            )}
          </Group>
        )}

        {inline && (
          <Alert
            color="yellow"
            variant="light"
            icon={<IconAlertTriangle size={18} />}
            title={t('addHost.warning.title')}
            data-testid="add-host-warning"
          >
            <Text fz="sm">{t('addHost.warning.text')}</Text>
            <List fz="sm" size="sm" mt={4}>
              {(
                [
                  'clipboard',
                  'history',
                  'psreadline',
                  'transcription',
                  'scriptblock',
                  'recording',
                ] as const
              ).map((k) => (
                <List.Item key={k}>{t(`addHost.warning.${k}`)}</List.Item>
              ))}
            </List>
            <Text fz="sm" mt={4}>
              {t('addHost.warning.leaked')}
            </Text>
          </Alert>
        )}
        <Text fz="xs" c="dimmed">
          {t('addHost.mtlsNote')}
        </Text>

        <Group>
          <Button
            onClick={submit}
            loading={render.isPending}
            disabled={!ready || (inline && !tokenOk)}
          >
            {t('addHost.show')}
          </Button>
        </Group>

        {showError && <RenderError error={showError} onRetry={submit} />}
        {shown && <Commands data={shown} token={inline && tokenOk ? token : ''} />}
      </Stack>
    );
  }

  return (
    <Modal
      opened
      onClose={onClose}
      title={t('addHost.title')}
      size="xl"
      closeOnClickOutside={false}
    >
      <Stack gap="md">
        <Text fz="sm" c="dimmed">
          {t('addHost.intro')}
        </Text>
        {body}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.close')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function RenderError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const { t } = useTranslation();
  const kind = renderErrorKind(error);
  if (kind === 'other' || kind === 'invalid') {
    return <ErrorAlert error={error} />;
  }
  const issues = isApiError(error) ? error.issues : [];
  const retry = kind === 'preparing' || kind === 'bundleFailed';
  return (
    <Alert
      color={kind === 'preparing' ? 'yellow' : 'red'}
      variant="light"
      icon={<IconAlertTriangle size={18} />}
      title={t(`addHost.error.${kind}.title`)}
      data-testid="add-host-error"
    >
      <Text fz="sm">{t(`addHost.error.${kind}.text`)}</Text>
      {(kind === 'bundleFailed' || kind === 'serverSettings') &&
        issues.map((i, idx) => (
          <Text key={idx} fz="sm" c="dimmed">
            {i.message}
          </Text>
        ))}
      <Text fz="sm" fw={500} mt={4}>
        {t(`addHost.error.${kind}.next`)}
      </Text>
      {retry && (
        <Button mt="xs" size="xs" variant="light" onClick={onRetry}>
          {t('addHost.retry')}
        </Button>
      )}
    </Alert>
  );
}

function Commands({ data, token }: { data: InstallCommands; token: string }) {
  const { t } = useTranslation();
  const placeholder = data.tokenPlaceholder;
  const needsToken = !!placeholder;
  return (
    <Stack gap="md" data-testid="add-host-commands">
      {data.insecure && (
        <Alert color="orange" variant="light" icon={<IconAlertTriangle size={18} />}>
          {t('addHost.insecure')}
        </Alert>
      )}
      {data.commands.map((c) => {
        const copyText =
          placeholder && token ? insertToken(c.command, placeholder, token) : c.command;
        const shownText = placeholder
          ? insertToken(c.command, placeholder, maskedToken())
          : c.command;
        return (
          <Stack key={c.platform} gap={4} data-testid={`command-${c.platform}`}>
            <Group justify="space-between">
              <Text fw={600} fz="sm">
                {t('addHost.commandFor', { platform: platformLabel(c.platform) })}
              </Text>
              <Group gap="xs">
                <BundleBadge bundle={c.bundle} />
                {needsToken && !token ? (
                  <Button size="xs" variant="light" disabled>
                    {t('addHost.copy')}
                  </Button>
                ) : (
                  <CopyTextButton text={copyText} label={t('addHost.copy')} />
                )}
              </Group>
            </Group>
            <Text fz="xs" c="dimmed">
              {c.shell === 'powershell' ? t('addHost.shellPowershell') : t('addHost.shellBash')}
            </Text>
            <Code block style={{ maxHeight: 260, overflow: 'auto' }}>
              {shownText}
            </Code>
            {c.bundle.sha256 && (
              <Text fz="xs" c="dimmed">
                {t('addHost.pinned', { sha: c.bundle.sha256, version: c.bundle.version })}
              </Text>
            )}
          </Stack>
        );
      })}
      {needsToken && (
        <Text fz="xs" c="dimmed">
          {t('addHost.maskedHint')}
        </Text>
      )}
    </Stack>
  );
}
