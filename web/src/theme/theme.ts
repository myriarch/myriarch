// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { createTheme, type CSSVariablesResolver, type MantineColorsTuple } from '@mantine/core';

/** Brand color: a calm indigo that works on light and dark surfaces. */
const brand: MantineColorsTuple = [
  '#eef2ff',
  '#dfe6ff',
  '#bcc8ff',
  '#96a8ff',
  '#7189ff',
  '#5a74fb',
  '#4a63ef',
  '#3d52d5',
  '#3446b0',
  '#2b3a8f',
];

const FONT_SANS =
  'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, "Noto Sans", sans-serif, "Apple Color Emoji", "Segoe UI Emoji"';
const FONT_MONO = 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace';

export const theme = createTheme({
  primaryColor: 'brand',
  autoContrast: true,
  primaryShade: { light: 6, dark: 6 },
  colors: { brand },
  fontFamily: FONT_SANS,
  fontFamilyMonospace: FONT_MONO,
  defaultRadius: 'md',
  cursorType: 'pointer',
  black: '#1a1d26',
  headings: {
    fontFamily: FONT_SANS,
    fontWeight: '650',
    sizes: {
      h1: { fontSize: '1.75rem', lineHeight: '1.25' },
      h2: { fontSize: '1.4rem', lineHeight: '1.3' },
      h3: { fontSize: '1.15rem', lineHeight: '1.35' },
      h4: { fontSize: '1rem', lineHeight: '1.4' },
    },
  },
  shadows: {
    xs: '0 1px 2px rgba(16, 24, 40, 0.06)',
    sm: '0 1px 3px rgba(16, 24, 40, 0.08), 0 1px 2px rgba(16, 24, 40, 0.04)',
    md: '0 4px 12px rgba(16, 24, 40, 0.08)',
  },
  components: {
    Card: { defaultProps: { withBorder: true, radius: 'md', padding: 'md' } },
    Paper: { defaultProps: { radius: 'md' } },
    Button: { defaultProps: { radius: 'md' } },
    Badge: {
      defaultProps: { radius: 'sm', variant: 'light' },
      styles: { label: { textTransform: 'none', fontWeight: 600, letterSpacing: 0 } },
    },
    Tooltip: { defaultProps: { withArrow: true, openDelay: 250 } },
    Modal: {
      defaultProps: {
        radius: 'md',
        centered: true,
        overlayProps: { blur: 2, backgroundOpacity: 0.45 },
      },
    },
    Drawer: { defaultProps: { overlayProps: { blur: 2, backgroundOpacity: 0.45 } } },
    TextInput: { defaultProps: { radius: 'md' } },
    Select: {
      defaultProps: { radius: 'md', allowDeselect: false, comboboxProps: { shadow: 'md' } },
    },
    MultiSelect: { defaultProps: { radius: 'md', comboboxProps: { shadow: 'md' } } },
    Table: { defaultProps: { highlightOnHover: true, verticalSpacing: 'xs' } },
    Skeleton: { defaultProps: { radius: 'sm' } },
  },
});

/**
 * Accessibility: Mantine's default "light" badge/alert/text colors reach only ~2.7-3.6:1 on light
 * surfaces. These variables keep the hue but lift every status text color to >= 4.5:1 (WCAG AA).
 * --app-logo-mark is the logo mark color from assets/brand: Tyrian on light, Orchid on dark.
 */
export const cssVariablesResolver: CSSVariablesResolver = () => ({
  variables: {},
  dark: {
    '--app-logo-mark': '#f28ccb',
    '--mantine-color-dimmed': '#a6a7ab',
    '--mantine-color-anchor': '#91a7ff',
  },
  light: {
    '--app-logo-mark': '#b3338a',
    '--mantine-color-dimmed': '#5c636a',
    '--mantine-color-anchor': '#3446b0',
    '--mantine-color-teal-light': '#e6fcf5',
    '--mantine-color-teal-light-color': '#0a6b4e',
    '--mantine-color-teal-text': '#0a6b4e',
    '--mantine-color-red-light': '#fff0f0',
    '--mantine-color-red-light-color': '#b02525',
    '--mantine-color-red-text': '#b02525',
    '--mantine-color-yellow-light': '#fff9db',
    '--mantine-color-yellow-light-color': '#7a4b00',
    '--mantine-color-yellow-text': '#7a4b00',
    '--mantine-color-orange-light': '#fff4e6',
    '--mantine-color-orange-light-color': '#a63a0a',
    '--mantine-color-orange-text': '#a63a0a',
    '--mantine-color-blue-light': '#e7f5ff',
    '--mantine-color-blue-light-color': '#145a9c',
    '--mantine-color-blue-text': '#145a9c',
    '--mantine-color-violet-light': '#f3f0ff',
    '--mantine-color-violet-light-color': '#5235b8',
    '--mantine-color-violet-text': '#5235b8',
    '--mantine-color-gray-outline': '#5c636a',
    '--mantine-color-gray-light': '#f1f3f5',
    '--mantine-color-gray-light-color': '#495057',
    '--mantine-color-brand-light': '#eef2ff',
    '--mantine-color-brand-light-color': '#3446b0',
    '--mantine-color-brand-text': '#3446b0',
  },
});
