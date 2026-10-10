---
title: "Authentication"
weight: 10
description: "Login, logout, current principal."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Login options (public)](#login-options-public) | `GET /auth/config` | `public` |
| [Start the OIDC authorization code flow (PKCE) - browser redirect](#start-the-oidc-authorization-code-flow-pkce---browser-redirect) | `GET /auth/login` | `public` |
| [OIDC redirect URI - creates the session and redirects to the UI](#oidc-redirect-uri---creates-the-session-and-redirects-to-the-ui) | `GET /auth/callback` | `public` |
| [Local login (only when auth.devLogin.enabled, i.e. dev mode)](#local-login-only-when-authdevloginenabled-ie-dev-mode) | `POST /auth/dev-login` | `public` |
| [Break-glass sign-in of the bootstrap admin (only while auth.bootstrapAdmin is active)](#break-glass-sign-in-of-the-bootstrap-admin-only-while-authbootstrapadmin-is-active) | `POST /auth/bootstrap-login` | `public` |
| [End the session](#end-the-session) | `POST /auth/logout` | `read` (viewer) |
| [Current principal with effective permissions](#current-principal-with-effective-permissions) | `GET /auth/me` | `read` (viewer) |

## Login options (public)

`GET /api/v1/auth/config`

Operation `getAuthConfig`. Permission `public`. No login is needed.

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Login options | `application/json` | [AuthConfigInfo](data-types.md#authconfiginfo) |

## Start the OIDC authorization code flow (PKCE) - browser redirect

`GET /api/v1/auth/login`

Operation `startOidcLogin`. Permission `public`. No login is needed.

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `redirect` | query | `string` | no | UI path to return to after login (must start with "/"). Default `/`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 302 | Redirect to the identity provider | - | - |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## OIDC redirect URI - creates the session and redirects to the UI

`GET /api/v1/auth/callback`

Operation `oidcCallback`. Permission `public`. No login is needed.

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `code` | query | `string` | yes |  |
| `state` | query | `string` | yes |  |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 302 | Redirect to the UI (session and CSRF cookies set). On failure the redirect goes to `/login?error=access_denied` (no role mapped for the user) or `/login?error=oidc_error`. | - | - |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Local login (only when auth.devLogin.enabled, i.e. dev mode)

`POST /api/v1/auth/dev-login`

Operation `devLogin`. Permission `public`. No login is needed.

### Request body

Content type `application/json`, required. Type: [DevLoginRequest](data-types.md#devloginrequest).

```json
{
  "username": "string",
  "password": "string"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Logged in; session and CSRF cookies set | `application/json` | [Me](data-types.md#me) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 429 | Too many login attempts for this client address or user name (code too_many_requests, header Retry-After) | `application/problem+json` | [Problem](data-types.md#problem) |

## Break-glass sign-in of the bootstrap admin (only while auth.bootstrapAdmin is active)

`POST /api/v1/auth/bootstrap-login`

Operation `bootstrapLogin`. Permission `public`. No login is needed.

### Request body

Content type `application/json`, required. Type: [BootstrapLoginRequest](data-types.md#bootstraploginrequest).

```json
{
  "username": "string",
  "password": "string",
  "token": "string"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Signed in; session and CSRF cookies set | `application/json` | [Me](data-types.md#me) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 429 | Too many sign-in attempts for this client address or for the bootstrap admin (code too_many_requests, header Retry-After) | `application/problem+json` | [Problem](data-types.md#problem) |

## End the session

`POST /api/v1/auth/logout`

Operation `logout`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 204 | Logged out | - | - |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Current principal with effective permissions

`GET /api/v1/auth/me`

Operation `getMe`. Permission `read` (lowest role: viewer).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Current principal | `application/json` | [Me](data-types.md#me) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
