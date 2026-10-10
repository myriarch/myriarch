// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
)

// ControlHandler serves the local control API:
//
//	POST /control/upgrade   {"hostnames": [...], "version": "0.163.0"} -> {"matched": n}
//	GET  /control/agents?hostname=...                                  -> agent state(s)
//	GET  /control/summary                                              -> counters
func (s *Simulator) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/control/upgrade", s.handleUpgrade)
	mux.HandleFunc("/control/agents", s.handleAgents)
	mux.HandleFunc("/control/summary", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Stats())
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Simulator) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var req struct {
		Hostnames []string `json:"hostnames"`
		Version   string   `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	ver, err := normalizeVersion(req.Version)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	matched := 0
	for _, h := range req.Hostnames {
		for _, c := range s.FindByHostname(strings.TrimSpace(h)) {
			matched++
			c.upgrade(ver) // restart for restartDuration, then new service.version
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"matched": matched})
}

func (s *Simulator) handleAgents(w http.ResponseWriter, r *http.Request) {
	h := r.URL.Query().Get("hostname")
	if h == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hostname query parameter required"})
		return
	}
	agents := s.FindByHostname(h)
	if len(agents) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no simulated agent with host name " + h})
		return
	}
	out := make([]AgentState, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.State())
	}
	if len(out) == 1 {
		writeJSON(w, http.StatusOK, out[0])
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func heapMiB() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc >> 20
}

func goroutines() int { return runtime.NumGoroutine() }
