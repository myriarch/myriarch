// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Fixed product identifiers of the web UI.
//
// Constants only, no runtime configuration (white-label deployments are out of scope).
// The values shared with the server must equal the constants in internal/brand (Go); a Go test
// parses the `export const NAME = 'value';` lines of this file and compares them.
// Keep exactly one line per constant in that form.

export const NAME = 'myriarch';
export const DISPLAY_NAME = 'Myriarch';
export const WEBSITE = 'https://myriarch.dev';
export const SERVER_BINARY = 'myriarchd';
export const CLI_BINARY = 'myrctl';
export const CLI_ALIAS = 'myr';
export const UPDATER_BINARY = 'myriarch-updater';
export const CSRF_COOKIE = 'myriarch_csrf';
export const ENROLLMENT_TOKEN_ENV = 'MYRIARCH_ENROLLMENT_TOKEN';
export const API_TOKEN_PREFIX = 'myra_';
export const ENROLLMENT_TOKEN_PREFIX = 'myre_';
export const ATTR_COLLECTOR_INSTANCE_UID = 'dev.myriarch.collector.instance_uid';
export const STORAGE_KEY_PREFIX = 'myriarch.';
export const MOCK_DEBUG_HOOK = '__myriarchMock';
export const LINUX_COLLECTOR_EXECUTABLE = '/opt/myriarch/collector/current/otelcol-contrib';
export const ENROLLMENT_TOKEN_FILE = 'enrollment-token';
