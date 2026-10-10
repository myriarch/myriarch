// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package pglock

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type dtoMetric struct{}

func (dtoMetric) read(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetGauge().GetValue()
}
