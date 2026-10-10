// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package domain contains the shared domain model of myriarch.
//
// Every component shares this model. A change here changes the REST contract
// (api/openapi.yaml) and the GitOps format (docs/reference/gitops-schema.md), which
// are updated in the same commit.
//
// Conventions:
//   - JSON field names are camelCase and match api/openapi.yaml exactly. Types
//     in this package are used directly as API representations where the
//     OpenAPI schema has the same name.
//   - YAML field names (GitOps objects) are camelCase as well and match
//     docs/reference/gitops-schema.md.
//   - Timestamps are UTC time.Time; JSON encodes them as RFC 3339.
//   - Versions of collector distributions are stored WITHOUT a leading "v"
//     ("0.162.0"), see NormalizeVersion.
//   - Hashes (config hashes, component hashes, package hashes) are raw bytes
//     (type Hash) and are encoded as lower-case hex strings in JSON.
package domain
