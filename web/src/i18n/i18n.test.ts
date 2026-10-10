// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { firstDayOfWeek } from '@/lib/format';
import i18n, {
  allowedLanguages,
  applyServerLanguageConfig,
  detectLanguage,
  LANG_STORAGE_KEY,
  matchBrowserLanguage,
} from './index';
import { isLang, LOCALES, SUPPORTED_LANGS } from './locales';
import {
  AGENT_KINDS,
  ARTIFACT_STATUSES,
  CONFIG_SYNC_STATES,
  CONNECTION_STATUSES,
  EXECUTOR_TYPES,
  OBJECT_KINDS,
  PACKAGE_INSTALL_STATUSES,
  PARAMETER_TYPES,
  PERMISSIONS,
  NOT_ALLOWED_CODES,
  PROBLEM_CODES,
  PROGRESS_SOURCES,
  REMOTE_CONFIG_STATUSES,
  ROLLOUT_ORIGINS,
  ROLES,
  ROLLOUT_KINDS,
  ROLLOUT_STATUSES,
  SELECTOR_OPERATORS,
  SELECTION_ISSUE_CODES,
  TARGET_STATUSES,
  UPDATE_PHASES,
  UPDATE_STEP_NAMES,
  UPDATE_STEP_STATUSES,
  VERSION_REASON_CODES,
  VERSION_TARGET_SOURCES,
  WAVE_STATUSES,
} from '@/api/enums';

function flatten(obj: unknown, prefix = ''): string[] {
  if (typeof obj !== 'object' || obj === null) return [prefix];
  return Object.entries(obj).flatMap(([k, v]) => flatten(v, prefix ? `${prefix}.${k}` : k));
}

const SOURCES = import.meta.glob<string>('../**/*.{ts,tsx}', {
  query: '?raw',
  import: 'default',
  eager: true,
});
const sourceFiles = Object.entries(SOURCES).filter(
  ([f]) => !/\.test\.|\/mocks\/|\/test\/|schema\.d\.ts/.test(f),
);

const en = LOCALES.en.resources;
const enKeys = flatten(en).sort();
const getValue = (o: unknown, path: string) =>
  path.split('.').reduce<unknown>((a, k) => (a as Record<string, unknown>)[k], o);

describe('i18n resources', () => {
  it('registers English as the source of truth', () => {
    expect(SUPPORTED_LANGS).toContain('en');
    expect(SUPPORTED_LANGS.length).toBeGreaterThanOrEqual(2);
  });

  describe.each(SUPPORTED_LANGS.map((code) => [code] as const))('locale %s', (code) => {
    const keys = flatten(LOCALES[code].resources).sort();

    it('has exactly the keys of en.json', () => {
      expect(keys.filter((k) => !enKeys.includes(k))).toEqual([]);
      expect(enKeys.filter((k) => !keys.includes(k))).toEqual([]);
    });

    it('has no empty translations', () => {
      for (const k of keys) {
        expect(String(getValue(LOCALES[code].resources, k)).trim(), `${code} ${k}`).not.toBe('');
      }
    });
  });

  it('defines every statically referenced t("...") key', () => {
    const all = new Set(enKeys);
    const has = (k: string) => all.has(k) || all.has(`${k}_one`) || all.has(`${k}_other`);
    const missing: string[] = [];
    for (const [file, src] of sourceFiles) {
      for (const m of src.matchAll(/\bt\(\s*['"]([a-zA-Z0-9_.-]+)['"]/g)) {
        if (!has(m[1]!)) missing.push(`${file}: ${m[1]}`);
      }
      // keys passed indirectly (toast keys in mutation meta / lookup tables)
      for (const m of src.matchAll(
        /['"](actions\.[a-zA-Z]+\.[a-zA-Z]+|agents\.savedFilters\.[a-zA-Z]+)['"]/g,
      )) {
        if (!has(m[1]!)) missing.push(`${file}: ${m[1]}`);
      }
    }
    expect(missing).toEqual([]);
  });

  it('covers every enum value used with dynamic keys', () => {
    const all = new Set(enKeys);
    const expectAll = (prefix: string, values: readonly string[]) => {
      for (const v of values) expect(all.has(`${prefix}.${v}`), `${prefix}.${v}`).toBe(true);
    };
    expectAll('enum.kind', AGENT_KINDS);
    expectAll('enum.connection', CONNECTION_STATUSES);
    expectAll('enum.configSync', CONFIG_SYNC_STATES);
    expectAll('enum.configSyncHint', CONFIG_SYNC_STATES);
    expectAll('enum.remoteConfig', REMOTE_CONFIG_STATUSES);
    expectAll('enum.packageStatus', PACKAGE_INSTALL_STATUSES);
    expectAll('enum.role', ROLES);
    expectAll('enum.roleHint', ROLES);
    expectAll('enum.executor', EXECUTOR_TYPES);
    expectAll('enum.paramType', PARAMETER_TYPES);
    expectAll('enum.selectorOp', SELECTOR_OPERATORS);
    expectAll('enum.rolloutKind', ROLLOUT_KINDS);
    expectAll('enum.rolloutStatus', ROLLOUT_STATUSES);
    expectAll('enum.waveStatus', WAVE_STATUSES);
    expectAll('enum.targetStatus', TARGET_STATUSES);
    expectAll('enum.artifactStatus', ARTIFACT_STATUSES);
    expectAll('enum.objectKind', OBJECT_KINDS);
    expectAll('enum.permission', PERMISSIONS);
    expectAll('errors', PROBLEM_CODES);
    expectAll('enum.versionSource', VERSION_TARGET_SOURCES);
    expectAll('enum.versionSourceShort', VERSION_TARGET_SOURCES);
    expectAll('enum.rolloutOrigin', ROLLOUT_ORIGINS);
    expectAll('enum.rolloutOriginShort', ROLLOUT_ORIGINS);
    expectAll('enum.updatePhase', UPDATE_PHASES);
    expectAll('enum.updateStep', UPDATE_STEP_NAMES);
    expectAll('enum.updateStepStatus', UPDATE_STEP_STATUSES);
    expectAll('enum.progressSource', PROGRESS_SOURCES);
    expectAll('enum.versionReason', VERSION_REASON_CODES);
    expectAll('enum.notAllowed', NOT_ALLOWED_CODES);
    expectAll('versionUpdate.error', SELECTION_ISSUE_CODES);
    expectAll('versionUpdate.history.actions', [
      'select',
      'clear',
      'superseded',
      'invalidated',
      'removed',
    ]);
    expectAll('rollouts.annotation', ['executorRef', 'distribution', 'updateParams']);
    expectAll('rollouts.actions', ['pause', 'resume', 'abort', 'rollback', 'retry-failed']);
    for (const a of ['pause', 'resume', 'abort', 'rollback', 'retry-failed', 'approve']) {
      expect(all.has(`rollouts.dialog.${a}.title`)).toBe(true);
      expect(all.has(`rollouts.dialog.${a}.text`)).toBe(true);
      expect(all.has(`rollouts.dialog.${a}.confirm`)).toBe(true);
    }
    for (const t of [
      'connected',
      'disconnected',
      'registered',
      'description_changed',
      'version_changed',
      'health_changed',
      'config_offered',
      'config_applied',
      'config_failed',
      'assigned',
      'package_offered',
      'package_status',
      'command',
      'rollout_target',
      'identity_conflict',
    ]) {
      expect(all.has(`enum.eventType.${t}`), t).toBe(true);
    }
  });
});

describe('language detection', () => {
  it('falls back to English when nothing matches', () => {
    expect(detectLanguage({ browser: ['fr-FR'] })).toBe('en');
    expect(detectLanguage({ browser: [] })).toBe('en');
    expect(detectLanguage()).toBe('en');
  });
  it('matches the primary subtag of the first supported browser language', () => {
    expect(detectLanguage({ browser: ['de-AT'] })).toBe('de');
    expect(detectLanguage({ browser: ['fr-FR', 'de-DE', 'en-US'] })).toBe('de');
    expect(detectLanguage({ browser: ['EN-gb'] })).toBe('en');
  });
  it('uses the server default only when the browser offers nothing supported', () => {
    expect(detectLanguage({ browser: ['fr-FR'], serverDefault: 'de' })).toBe('de');
    expect(detectLanguage({ browser: ['en-US'], serverDefault: 'de' })).toBe('en');
    expect(detectLanguage({ browser: ['fr-FR'], serverDefault: 'xx' })).toBe('en');
  });
  it('prefers a stored choice', () => {
    expect(detectLanguage({ stored: 'en', browser: ['de-DE'] })).toBe('en');
    expect(detectLanguage({ stored: 'de', browser: ['en-US'], serverDefault: 'en' })).toBe('de');
  });
  it('ignores an unsupported stored choice', () => {
    expect(detectLanguage({ stored: 'xx', browser: ['de-DE'] })).toBe('de');
    expect(detectLanguage({ stored: 'xx', browser: ['fr-FR'] })).toBe('en');
  });
  it('ignores a stored choice that is not allowed', () => {
    expect(detectLanguage({ stored: 'de', browser: ['en-US'], allowed: ['en'] })).toBe('en');
    expect(
      detectLanguage({ stored: 'de', browser: ['fr-FR'], serverDefault: 'en', allowed: ['en'] }),
    ).toBe('en');
  });
  it('restricts browser matches and the server default to the allowed languages', () => {
    expect(matchBrowserLanguage(['de-DE', 'en-US'], ['en'])).toBe('en');
    expect(matchBrowserLanguage(['de-DE'], ['en'])).toBeUndefined();
    expect(detectLanguage({ browser: ['fr-FR'], serverDefault: 'de', allowed: ['en'] })).toBe('en');
    expect(detectLanguage({ browser: ['fr-FR'], allowed: ['de'] })).toBe('de');
  });
  it('treats an empty allowed list as every shipped language', () => {
    expect(detectLanguage({ browser: ['de-DE'], allowed: [] })).toBe('de');
    expect(allowedLanguages({ languages: [] })).toEqual(SUPPORTED_LANGS);
    expect(allowedLanguages(undefined)).toEqual(SUPPORTED_LANGS);
    expect(allowedLanguages({ languages: ['de', 'fr'] })).toEqual(['de']);
  });
  it('maps BCP 47 tags with region subtags in the allowed list to shipped languages', () => {
    const allowed = ['de-DE', 'en-US'];
    expect(allowedLanguages({ languages: allowed })).toEqual(SUPPORTED_LANGS);
    expect(allowedLanguages({ languages: ['de-DE'] })).toEqual(['de']);
    expect(allowedLanguages({ languages: ['DE-at', 'fr-FR'] })).toEqual(['de']);
    expect(detectLanguage({ browser: ['fr-FR'], serverDefault: 'de-DE', allowed })).toBe('de');
    expect(detectLanguage({ stored: 'de', browser: ['en-US'], allowed })).toBe('de');
    expect(detectLanguage({ browser: ['de-AT'], allowed: ['en-US'] })).toBe('en');
    expect(matchBrowserLanguage(['de-DE'], ['en-US'])).toBeUndefined();
  });
  it('offers no language when the allowed list contains no shipped locale', () => {
    expect(allowedLanguages({ languages: ['fr-FR', 'xx'] })).toEqual([]);
    expect(detectLanguage({ browser: ['de-DE'], serverDefault: 'de', allowed: ['fr-FR'] })).toBe('en');
  });
  it('knows the registered languages', () => {
    expect(isLang('de')).toBe(true);
    expect(isLang('xx')).toBe(false);
    expect(isLang(null)).toBe(false);
    expect(isLang('toString')).toBe(false);
  });
});

describe('week start', () => {
  it('returns a Mantine day of week (0 = Sunday ... 6) for every shipped language', () => {
    for (const code of SUPPORTED_LANGS) {
      const d = firstDayOfWeek(code);
      expect(d).toBeGreaterThanOrEqual(0);
      expect(d).toBeLessThanOrEqual(6);
    }
  });
  it('starts on Monday for German and falls back to Monday without week info', () => {
    expect(firstDayOfWeek('de')).toBe(1);
    const original = Intl.Locale;
    // a runtime whose Intl.Locale knows neither getWeekInfo() nor weekInfo
    (Intl as unknown as { Locale: unknown }).Locale = class {
      constructor(public tag: string) {}
    };
    try {
      expect(firstDayOfWeek('en')).toBe(1);
    } finally {
      (Intl as unknown as { Locale: unknown }).Locale = original;
    }
  });
});

describe('applyServerLanguageConfig', () => {
  let store: Map<string, string>;
  beforeEach(() => {
    // an in-memory storage: independent of the Node/jsdom localStorage of the test runner
    store = new Map();
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    });
  });
  afterEach(async () => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    await i18n.changeLanguage('en');
  });
  const browser = (...langs: string[]) =>
    vi.spyOn(window.navigator, 'languages', 'get').mockReturnValue(langs);

  it('switches to the server default without a stored choice or a matching browser language', async () => {
    browser('fr-FR');
    applyServerLanguageConfig({ defaultLanguage: 'de' });
    await vi.waitFor(() => expect(i18n.language).toBe('de'));
  });
  it('keeps the browser language over the server default', async () => {
    browser('en-US');
    await i18n.changeLanguage('en');
    applyServerLanguageConfig({ defaultLanguage: 'de' });
    expect(i18n.language).toBe('en');
  });
  it('keeps a stored choice over the server default', async () => {
    browser('fr-FR');
    store.set(LANG_STORAGE_KEY, 'en');
    applyServerLanguageConfig({ defaultLanguage: 'de' });
    expect(i18n.language).toBe('en');
  });
  it('replaces a language the server does not allow', async () => {
    browser('de-DE');
    await i18n.changeLanguage('de');
    applyServerLanguageConfig({ defaultLanguage: 'en', languages: ['en'] });
    await vi.waitFor(() => expect(i18n.language).toBe('en'));
  });
});
