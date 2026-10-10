// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import { STORAGE_KEY_PREFIX } from '@/brand';
import { readStorage, writeStorage } from '@/lib/storage';
import { isLang, LOCALES, SUPPORTED_LANGS, type Lang } from './locales';

export { LOCALES, SUPPORTED_LANGS, isLang, type Lang } from './locales';

export const LANG_STORAGE_KEY = STORAGE_KEY_PREFIX + 'lang';

/** Language options from `GET /auth/config` (`ui.*` in the server configuration). */
export interface ServerLanguageConfig {
  defaultLanguage: string;
  /** Restricts the offered languages; empty or missing = every shipped language. */
  languages?: readonly string[];
}

export interface DetectInput {
  /** The user's saved choice (local storage). */
  stored?: string | null;
  /** `navigator.languages`, most preferred first. */
  browser?: readonly string[];
  /** `ui.defaultLanguage` of the server, once known. */
  serverDefault?: string;
  /** Allowed languages; empty or missing = every shipped language. */
  allowed?: readonly string[];
}

/** Maps a BCP 47 tag ("de-AT") to a shipped language by its full tag or primary subtag. */
function toSupported(tag: string): Lang | undefined {
  const lower = tag.trim().toLowerCase();
  if (isLang(lower)) return lower;
  const primary = lower.split('-')[0];
  return isLang(primary) ? primary : undefined;
}

/**
 * The server validates `ui.languages` as BCP 47 tags, so "de-DE" is a valid entry. Each entry is
 * mapped to a shipped language the same way as the default language (full tag, then primary
 * subtag). Entries without a shipped language are dropped.
 */
function isAllowed(lang: Lang, allowed: readonly string[] | undefined): boolean {
  if (!allowed || allowed.length === 0) return true;
  return allowed.some((tag) => toSupported(tag) === lang);
}

/** The first browser language that is shipped and allowed. */
export function matchBrowserLanguage(
  browser: readonly string[] | undefined,
  allowed?: readonly string[],
): Lang | undefined {
  for (const tag of browser ?? []) {
    const lang = toSupported(tag);
    if (lang && isAllowed(lang, allowed)) return lang;
  }
  return undefined;
}

/**
 * Order: a stored choice (if shipped and allowed) -> the first supported browser language ->
 * the server default (if shipped and allowed) -> English.
 */
export function detectLanguage({
  stored,
  browser,
  serverDefault,
  allowed,
}: DetectInput = {}): Lang {
  if (isLang(stored) && isAllowed(stored, allowed)) return stored;
  const fromBrowser = matchBrowserLanguage(browser, allowed);
  if (fromBrowser) return fromBrowser;
  const fromServer = serverDefault ? toSupported(serverDefault) : undefined;
  if (fromServer && isAllowed(fromServer, allowed)) return fromServer;
  if (isAllowed('en', allowed)) return 'en';
  return SUPPORTED_LANGS.find((l) => isAllowed(l, allowed)) ?? 'en';
}

function browserLanguages(): readonly string[] {
  if (typeof navigator === 'undefined') return [];
  if (navigator.languages && navigator.languages.length > 0) return navigator.languages;
  return navigator.language ? [navigator.language] : [];
}

/** The languages the UI offers, given the server configuration (`undefined` = not loaded yet). */
export function allowedLanguages(ui?: Pick<ServerLanguageConfig, 'languages'>): Lang[] {
  return SUPPORTED_LANGS.filter((l) => isAllowed(l, ui?.languages));
}

void i18n.use(initReactI18next).init({
  resources: Object.fromEntries(
    Object.entries(LOCALES).map(([code, l]) => [code, { translation: l.resources }]),
  ),
  lng: detectLanguage({ stored: readStorage(LANG_STORAGE_KEY), browser: browserLanguages() }),
  fallbackLng: 'en',
  supportedLngs: SUPPORTED_LANGS,
  interpolation: { escapeValue: false },
  returnNull: false,
});

function syncDocumentLang(lng: string): void {
  if (typeof document !== 'undefined') document.documentElement.lang = lng;
}
syncDocumentLang(i18n.language);
i18n.on('languageChanged', syncDocumentLang);

export function setLanguage(lang: Lang): void {
  writeStorage(LANG_STORAGE_KEY, lang);
  void i18n.changeLanguage(lang);
}

export function currentLang(): Lang {
  return isLang(i18n.language) ? i18n.language : 'en';
}

/**
 * Applies the server's language options once `GET /auth/config` resolved. The user's own choice
 * and a matching browser language win; the server default only applies when neither exists, and
 * a language the server does not allow is always replaced. Nothing is persisted, so a later
 * change of the server configuration takes effect.
 */
export function applyServerLanguageConfig(ui: ServerLanguageConfig): void {
  const allowed = ui.languages;
  const stored = readStorage(LANG_STORAGE_KEY);
  const browser = browserLanguages();
  const current = currentLang();
  const hasChoice =
    (isLang(stored) && isAllowed(stored, allowed)) || !!matchBrowserLanguage(browser, allowed);
  if (isAllowed(current, allowed) && hasChoice) return;
  const target = detectLanguage({ stored, browser, serverDefault: ui.defaultLanguage, allowed });
  if (target !== current) void i18n.changeLanguage(target);
}

export default i18n;
