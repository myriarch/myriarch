// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import "github.com/prometheus/client_golang/prometheus"

func main() {
	prometheus.NewCounter(prometheus.CounterOpts{Name: "myriarch_tool_runs_total", Help: "Runs of the tool."})
}
