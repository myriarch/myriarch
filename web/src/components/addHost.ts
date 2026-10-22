// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { ENROLLMENT_TOKEN_PREFIX } from '@/brand';
import { isApiError } from '@/api/errors';
import type { InstallBundle, InstallCommandsRequest } from '@/api/types';

export interface AddHostForm {
  variant: 'token_inline' | 'token_file';
  platforms: string[];
  fleet: string;
  siteType: string;
  siteId: string;
  siteName: string;
  siteCountry: string;
  siteRegion: string;
  siteTimezone: string;
  environment: string;
  linuxTokenFile: string;
  windowsTokenFile: string;
}

export const platformOf = (b: Pick<InstallBundle, 'os' | 'arch'>) => `${b.os}/${b.arch}`;

export function platformLabel(platform: string): string {
  const [os = '', arch = ''] = platform.split('/');
  return `${os === 'windows' ? 'Windows' : 'Linux'} ${arch}`;
}

/** The request for the render route. It never holds the token: the server must not see it. */
export function buildRequest(f: AddHostForm): InstallCommandsRequest {
  const attributes: Record<string, string> = {
    'site.type': f.siteType.trim(),
    'site.id': f.siteId.trim(),
  };
  const optional: Record<string, string> = {
    'site.name': f.siteName,
    'site.country': f.siteCountry,
    'site.region': f.siteRegion,
    'site.timezone': f.siteTimezone,
    'deployment.environment.name': f.environment,
  };
  for (const [k, v] of Object.entries(optional)) if (v.trim()) attributes[k] = v.trim();
  const req: InstallCommandsRequest = { variant: f.variant, platforms: f.platforms, attributes };
  if (f.fleet.trim()) req.fleet = f.fleet.trim();
  if (f.variant === 'token_file') {
    const tokenFiles: { linux?: string; windows?: string } = {};
    if (f.linuxTokenFile.trim()) tokenFiles.linux = f.linuxTokenFile.trim();
    if (f.windowsTokenFile.trim()) tokenFiles.windows = f.windowsTokenFile.trim();
    if (tokenFiles.linux || tokenFiles.windows) req.tokenFiles = tokenFiles;
  }
  return req;
}

/** Puts the token into every place of the placeholder (split/join: no replacement patterns are interpreted). */
export function insertToken(command: string, placeholder: string, token: string): string {
  return command.split(placeholder).join(token);
}

/** What the page shows instead of the token: the prefix and bullets. The copy button copies the real text. */
export function maskedToken(): string {
  return `${ENROLLMENT_TOKEN_PREFIX}••••••••`;
}

/** An enrollment token as the person pastes it: the prefix, then characters that no shell treats specially. */
export function looksLikeToken(v: string): boolean {
  return v.startsWith(ENROLLMENT_TOKEN_PREFIX) && /^[A-Za-z0-9_-]{16,}$/.test(v);
}

export type RenderErrorKind =
  | 'localBuild'
  | 'serverSettings'
  | 'preparing'
  | 'bundleFailed'
  | 'platformNotSupported'
  | 'variantNotAvailable'
  | 'invalid'
  | 'disabled'
  | 'conflict'
  | 'other';

/** Sorts an error of `renderInstallCommands` by status and reason (the code of the first issue). */
export function renderErrorKind(e: unknown): RenderErrorKind {
  if (!isApiError(e)) return 'other';
  const reason = e.issues[0]?.code;
  switch (e.status) {
    case 404:
      return 'disabled';
    case 409:
      return 'conflict';
    case 412:
      return reason === 'local_build'
        ? 'localBuild'
        : reason === 'server_settings'
          ? 'serverSettings'
          : 'other';
    case 422:
      return reason === 'platform_not_supported'
        ? 'platformNotSupported'
        : reason === 'variant_not_available'
          ? 'variantNotAvailable'
          : 'invalid';
    case 503:
      return reason === 'bundle_preparing'
        ? 'preparing'
        : reason === 'bundle_failed'
          ? 'bundleFailed'
          : 'other';
    default:
      return 'other';
  }
}
