// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import "github.com/prometheus/client_golang/prometheus"

var _ = prometheus.NewGauge(prometheus.GaugeOpts{Name: "myriarch_sim_in_a_test", Help: "Test files are not scanned."})
