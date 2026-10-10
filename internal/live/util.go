// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package live

import (
	"errors"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
)

func parseUUID(s string) (uuid.UUID, error) { return uuid.Parse(s) }

func isNotFound(err error) bool { return errors.Is(err, domain.ErrNotFound) }
