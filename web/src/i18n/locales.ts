// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import 'dayjs/locale/de';
import 'dayjs/locale/en';
import de from './de.json';
import en from './en.json';

/**
 * Registry of the locales the UI ships. Adding a language means: add the JSON file, one entry here
 * and the matching `dayjs/locale/<code>` import above. Everything else (language menu, profile
 * settings, detection, the `Lang` type) derives from this object. `en.json` is the source of truth;
 * every other locale must have exactly its keys (see i18n.test.ts).
 */
export const LOCALES = {
  en: { label: 'English', resources: en },
  de: { label: 'Deutsch', resources: de },
} as const;

export type Lang = keyof typeof LOCALES;

export const SUPPORTED_LANGS = Object.keys(LOCALES) as Lang[];

export function isLang(value: unknown): value is Lang {
  return typeof value === 'string' && Object.prototype.hasOwnProperty.call(LOCALES, value);
}
