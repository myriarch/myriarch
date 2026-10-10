// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package release

import "github.com/myriarch/myriarch/internal/store"

func store_filter(dist string) store.ReleaseFilter {
	return store.ReleaseFilter{Distribution: dist, IncludePrerelease: true}
}

func store_filterPre(dist string) store.ReleaseFilter {
	return store.ReleaseFilter{Distribution: dist, IncludePrerelease: true}
}
