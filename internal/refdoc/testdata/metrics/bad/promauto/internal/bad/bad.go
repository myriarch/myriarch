// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package bad

import "github.com/prometheus/client_golang/prometheus/promauto"

var _ = promauto.NewCounter(prometheus.CounterOpts{Name: "myriarch_bad_total", Help: "Bad."})
