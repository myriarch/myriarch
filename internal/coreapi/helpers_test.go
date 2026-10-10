// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package coreapi_test

import "github.com/google/uuid"

func mustUUID() uuid.UUID { return uuid.Must(uuid.NewV7()) }
