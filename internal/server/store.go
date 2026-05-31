package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ServiceRecord is one tunnel's persisted state: it survives restarts so the
// hub/status pages can show services that are currently offline too.
type ServiceRecord struct {
	Namespace string    `json:"namespace,omitempty"`
	Slug      string    `json:"slug"`
	Host      string    `json:"host"`
	Label     string    `json:"label,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Online    bool      `json:"online"`
}

// serviceStore is a file-backed registry of services keyed by host. Writes are
// atomic (tmp + rename). With an empty path it is purely in-memory.
type serviceStore struct {
	mu   sync.RWMutex
	path string
	recs map[string]*ServiceRecord
}

// newServiceStore loads existing state from path (if any). All loaded records
// start Offline — liveness is re-asserted as clients reconnect.
func newServiceStore(path string) *serviceStore {
	s := &serviceStore{path: path, recs: map[string]*ServiceRecord{}}
	if path == "" {
		return s
	}
	if b, err := os.ReadFile(path); err == nil {
		var recs []*ServiceRecord
		if json.Unmarshal(b, &recs) == nil {
			for _, r := range recs {
				r.Online = false
				s.recs[r.Host] = r
			}
		}
	}
	return s
}

// markOnline records (or refreshes) a live service and persists.
func (s *serviceStore) markOnline(namespace, slug, host, label string) {
	now := time.Now()
	s.mu.Lock()
	r, ok := s.recs[host]
	if !ok {
		r = &ServiceRecord{Host: host, FirstSeen: now}
		s.recs[host] = r
	}
	r.Namespace, r.Slug, r.Label, r.LastSeen, r.Online = namespace, slug, label, now, true
	s.mu.Unlock()
	s.persist()
}

// markOffline flags a service as disconnected (kept for history) and persists.
func (s *serviceStore) markOffline(host string) {
	s.mu.Lock()
	if r, ok := s.recs[host]; ok {
		r.Online, r.LastSeen = false, time.Now()
	}
	s.mu.Unlock()
	s.persist()
}

// list returns all records sorted by host. If namespace is non-empty, only that
// namespace's records are returned.
func (s *serviceStore) list(namespace string) []ServiceRecord {
	s.mu.RLock()
	out := make([]ServiceRecord, 0, len(s.recs))
	for _, r := range s.recs {
		if namespace == "" || r.Namespace == namespace {
			out = append(out, *r)
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// persist atomically writes the current state to disk (no-op without a path).
func (s *serviceStore) persist() {
	if s.path == "" {
		return
	}
	s.mu.RLock()
	recs := make([]*ServiceRecord, 0, len(s.recs))
	for _, r := range s.recs {
		recs = append(recs, r)
	}
	s.mu.RUnlock()
	sort.Slice(recs, func(i, j int) bool { return recs[i].Host < recs[j].Host })

	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path) // atomic replace
	}
}
