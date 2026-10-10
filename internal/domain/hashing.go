// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"

	"github.com/myriarch/myriarch/internal/brand"
)

// RemoteConfigKey is the single key under which myriarchd sends the rendered
// collector configuration in AgentRemoteConfig.config.config_map. The
// supervisor merges all config_map entries (sorted by key, "" last) into
// $REMOTE_CONFIG; using one fixed key keeps the merge trivial.
const RemoteConfigKey = brand.RemoteConfigKey

// RemoteConfigContentType is the content type of the rendered config.
const RemoteConfigContentType = "text/yaml"

// ConfigHash returns the OpAMP AgentRemoteConfig.config_hash for a rendered
// config body: SHA-256 of the canonical YAML bytes. Because myriarchd always
// sends exactly one config_map entry (RemoteConfigKey, RemoteConfigContentType)
// the body hash identifies the whole AgentConfigMap. The same value is the
// primary key of the rendered config store.
func ConfigHash(body []byte) Hash { return SHA256(body) }

// PackageHash computes PackageAvailable.hash for an offer: SHA-256 over
// type(top-level=0) | version | download_url | content_hash | signature,
// each field length-prefixed (uint32 big endian).
func PackageHash(o PackageOffer) Hash {
	h := sha256.New()
	writeField := func(b []byte) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(b)))
		h.Write(l[:])
		h.Write(b)
	}
	writeField([]byte{0}) // PackageType_TopLevel
	writeField([]byte(o.Version))
	writeField([]byte(o.DownloadURL))
	writeField(o.ContentHash)
	writeField(o.Signature)
	return Hash(h.Sum(nil))
}

// AllPackagesHash computes PackagesAvailable.all_packages_hash: SHA-256 over
// the offers sorted by name, each contributing name and PackageHash
// (length-prefixed). The empty offer set hashes to SHA-256 of nothing, which
// is still a non-empty hash (OpAMP requires the field to be set).
func AllPackagesHash(offers []PackageOffer) Hash {
	sorted := make([]PackageOffer, len(offers))
	copy(sorted, offers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	h := sha256.New()
	for _, o := range sorted {
		ph := PackageHash(o)
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(o.Name)))
		h.Write(l[:])
		h.Write([]byte(o.Name))
		h.Write(ph)
	}
	return Hash(h.Sum(nil))
}
