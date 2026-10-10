// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * Tiny module-level session state shared between the API client and the AuthProvider:
 * the CSRF token (cookie `myriarch_csrf`, or `csrfToken` from /auth/me) and the "session
 * expired" signal that makes the AuthProvider show the login page.
 */
import { CSRF_COOKIE } from '@/brand';

let csrfFromMe: string | undefined;
const listeners = new Set<() => void>();

export function setCsrfToken(token: string | undefined): void {
  csrfFromMe = token;
}

export function readCookie(name: string, source: string = document.cookie): string | undefined {
  for (const part of source.split(';')) {
    const idx = part.indexOf('=');
    if (idx < 0) continue;
    if (part.slice(0, idx).trim() === name) {
      try {
        return decodeURIComponent(part.slice(idx + 1).trim());
      } catch {
        return part.slice(idx + 1).trim();
      }
    }
  }
  return undefined;
}

export { CSRF_COOKIE };

export function getCsrfToken(): string | undefined {
  return readCookie(CSRF_COOKIE) ?? csrfFromMe;
}

/** Called by the API client when any (non-auth-probe) request returns 401. */
export function notifyUnauthorized(): void {
  for (const l of [...listeners]) l();
}

export function onUnauthorized(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
