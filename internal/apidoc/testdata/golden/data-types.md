---
title: "Data types"
weight: 30
description: "Every schema of the REST API with its fields, types, constraints and values."
---

The named types of the [REST API](_index.md), in alphabetical order. Field names are in the JSON spelling (camelCase). A field marked required is always present; required maps and arrays are never `null`.

## CreateItem

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` | 1 to 63 characters. |
| `kind` | [Kind](#kind) |  |
| `weight` | `number` | Default `1.5`. |

## Item

An item.

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` | Format `uuid`. |
| `name` (required) | `string` | At most 63 characters. Pattern `^[a-z]+(a\|b)$`. |
| `kind` (required) | [Kind](#kind) |  |
| `createdAt` | `string` | Format `date-time`. |
| `labels` | map of `string` to `string` | Free-form labels, a \| b. |
| `parts` | array of [Part](#part) |  |

## ItemList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [Item](#item) |  |

## Kind

Kind of an item.

Type: `string`. Values:

- `alpha`
- `beta`

## Part

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` | Example `bolt`. |
| `subParts` | array of [Part](#part) |  |
| `parent` | [Part](#part) |  |

## Problem

| Field | Type | Description |
|---|---|---|
| `title` (required) | `string` |  |
| `status` (required) | `integer` |  |
| `code` (required) | `string` | One of `unauthorized`, `validation_failed`. |
