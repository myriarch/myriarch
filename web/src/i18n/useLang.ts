// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import { useAuth } from '@/auth/useAuth';
import { allowedLanguages } from './index';
import { isLang, type Lang } from './locales';

/** Active UI language; re-renders on change. */
export function useLang(): Lang {
  const { i18n } = useTranslation();
  return isLang(i18n.language) ? i18n.language : 'en';
}

/** Languages the language pickers offer: the shipped ones, restricted by `ui.languages` of the server. */
export function useAllowedLangs(): Lang[] {
  return allowedLanguages(useAuth().config?.ui);
}
