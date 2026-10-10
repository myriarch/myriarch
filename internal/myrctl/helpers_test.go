// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package myrctl_test

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

func yamlUnmarshal(b []byte, v any) error { return yaml.Unmarshal(b, v) }

func toString(v any) string { return fmt.Sprint(v) }
