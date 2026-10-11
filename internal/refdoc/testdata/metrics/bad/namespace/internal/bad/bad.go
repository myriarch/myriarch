// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package bad

import "github.com/prometheus/client_golang/prometheus"

var _ = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "myriarch", Name: "bad", Help: "Bad."})
