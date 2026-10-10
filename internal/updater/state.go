// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/encoding/protojson"
)

// packageRecord is the locally remembered state of one OpAMP package.
type packageRecord struct {
	Type    int32  `json:"type"`
	Hash    []byte `json:"hash,omitempty"`
	Version string `json:"version,omitempty"`
}

// failureRecord remembers a failed install so that reconnecting updaters do
// not retry (and restart the supervisor) in a loop.
type failureRecord struct {
	Version string    `json:"version"`
	Error   string    `json:"error"`
	At      time.Time `json:"at"`
}

// stateFile is the persisted content of <stateDir>/packages.json.
type stateFile struct {
	AllPackagesHash []byte                   `json:"allPackagesHash,omitempty"`
	Packages        map[string]packageRecord `json:"packages"`
	// LastStatuses is the protojson of the last reported PackageStatuses.
	LastStatuses json.RawMessage          `json:"lastReportedStatuses,omitempty"`
	Failures     map[string]failureRecord `json:"failures,omitempty"` // key: hex(content hash)
}

// stateStore persists updater state atomically. All methods are safe for
// concurrent use.
type stateStore struct {
	dir string
	mu  sync.Mutex
	st  stateFile
}

func openStateStore(dir string) (*stateStore, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	s := &stateStore{dir: dir, st: stateFile{Packages: map[string]packageRecord{}}}
	data, err := os.ReadFile(s.path())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", s.path(), err)
	default:
		if err := json.Unmarshal(data, &s.st); err != nil {
			// A corrupt state file must not brick the updater: start fresh.
			s.st = stateFile{Packages: map[string]packageRecord{}}
		}
		if s.st.Packages == nil {
			s.st.Packages = map[string]packageRecord{}
		}
	}
	return s, nil
}

func (s *stateStore) path() string { return filepath.Join(s.dir, "packages.json") }

// saveLocked persists the state; caller holds s.mu.
func (s *stateStore) saveLocked() error {
	data, err := json.MarshalIndent(&s.st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path(), data, 0o600)
}

func (s *stateStore) update(fn func(st *stateFile)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.st)
	return s.saveLocked()
}

func (s *stateStore) read(fn func(st *stateFile)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.st)
}

func (s *stateStore) lastStatuses() (*protobufs.PackageStatuses, error) {
	s.mu.Lock()
	raw := append(json.RawMessage(nil), s.st.LastStatuses...)
	s.mu.Unlock()
	if len(raw) == 0 {
		return nil, nil
	}
	var ps protobufs.PackageStatuses
	if err := protojson.Unmarshal(raw, &ps); err != nil {
		return nil, nil // unreadable -> start with empty statuses
	}
	return &ps, nil
}

func (s *stateStore) setLastStatuses(ps *protobufs.PackageStatuses) error {
	var raw []byte
	if ps != nil {
		var err error
		raw, err = protojson.Marshal(ps)
		if err != nil {
			return err
		}
	}
	return s.update(func(st *stateFile) { st.LastStatuses = raw })
}

func (s *stateStore) recordFailure(contentHash []byte, version, msg string, now time.Time) error {
	return s.update(func(st *stateFile) {
		if st.Failures == nil {
			st.Failures = map[string]failureRecord{}
		}
		st.Failures[hex.EncodeToString(contentHash)] = failureRecord{Version: version, Error: msg, At: now.UTC()}
		for k, f := range st.Failures { // prune old entries
			if now.Sub(f.At) > 24*time.Hour {
				delete(st.Failures, k)
			}
		}
	})
}

func (s *stateStore) clearFailure(contentHash []byte) error {
	key := hex.EncodeToString(contentHash)
	var present bool
	s.read(func(st *stateFile) { _, present = st.Failures[key] })
	if !present {
		return nil
	}
	return s.update(func(st *stateFile) { delete(st.Failures, key) })
}

func (s *stateStore) failure(contentHash []byte) (failureRecord, bool) {
	var f failureRecord
	var ok bool
	key := hex.EncodeToString(contentHash)
	s.read(func(st *stateFile) { f, ok = st.Failures[key] })
	return f, ok
}

// loadOrCreateInstanceUID reads <dir>/instance_uid or creates a UUIDv7.
func loadOrCreateInstanceUID(dir string) (uuid.UUID, error) {
	p := filepath.Join(dir, "instance_uid")
	if data, err := os.ReadFile(p); err == nil {
		if id, err := uuid.Parse(string(trimSpaceBytes(data))); err == nil && id != uuid.Nil {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return uuid.Nil, fmt.Errorf("read instance uid: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	if err := saveInstanceUID(dir, id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func saveInstanceUID(dir string, id uuid.UUID) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "instance_uid"), []byte(id.String()+"\n"), 0o600)
}

func trimSpaceBytes(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\n' || b[0] == '\r' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 {
		c := b[len(b)-1]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}

// journal records an in-flight update so that a crash between "stop the
// supervisor" and "health verified" can be repaired at the next start.
type journal struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Phase     string    `json:"phase"` // swapping | verifying | rollingBack
	StartedAt time.Time `json:"startedAt"`
}

func journalPath(stateDir string) string { return filepath.Join(stateDir, "update-journal.json") }

func writeJournal(stateDir string, j journal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return writeFileAtomic(journalPath(stateDir), data, 0o600)
}

func readJournal(stateDir string) (*journal, error) {
	data, err := os.ReadFile(journalPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("corrupt update journal: %w", err)
	}
	return &j, nil
}

func clearJournal(stateDir string) error {
	err := os.Remove(journalPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
