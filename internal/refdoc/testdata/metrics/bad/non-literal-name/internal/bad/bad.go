// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package bad

import "github.com/prometheus/client_golang/prometheus"

const name = "myriarch_bad_total"

var _ = prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: "Bad."})
