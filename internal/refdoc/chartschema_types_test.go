// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import "time"

// ChartMini is the configuration of the golden test of the chart schema: the structures of MiniConfig and the shapes that it lacks.
type ChartMini struct {
	Server MiniServer  `yaml:"server"`
	Notify MiniNotify  `yaml:"notify"`
	Dev    MiniDev     `yaml:"dev"`
	Shapes ChartShapes `yaml:"shapes"`
}

// ChartShapes holds the field types that MiniConfig does not have.
type ChartShapes struct {
	// Port is an unsigned port number.
	Port uint16 `yaml:"port"`
	// Retries is a signed number that may be negative.
	Retries int32 `yaml:"retries"`
	// Names is a list of strings.
	Names []string `yaml:"names"`
	// Budget is a duration; the loader accepts only Go syntax.
	Budget time.Duration `yaml:"budget"`
}

// The structures below are what the generator refuses. Each is the root of one case of TestChartSchemaRefusesWhatItCannotRepresent.

// ChartBadSlice has a list of numbers.
type ChartBadSlice struct {
	// Numbers is a list of integers, which the configuration reference does not support either.
	Numbers []int `yaml:"numbers"`
}

// ChartBadMap has a map of numbers.
type ChartBadMap struct {
	// Counts maps names to numbers.
	Counts map[string]int `yaml:"counts"`
}

// ChartBadTime has a time stamp.
type ChartBadTime struct {
	// When is a point in time.
	When time.Time `yaml:"when"`
}

// ChartBadNoComment has a field without a comment.
type ChartBadNoComment struct {
	Name string `yaml:"name"`
}

// ChartBadBacktick has a comment with an unpaired backtick.
type ChartBadBacktick struct {
	// Name is the `name of the thing.
	Name string `yaml:"name"`
}

// ChartBadNested has a nested structure with a field that the generator refuses.
type ChartBadNested struct {
	// Inner is a structure.
	Inner ChartBadSlice `yaml:"inner"`
}
