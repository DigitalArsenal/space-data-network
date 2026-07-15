package sdnruns

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrRunNotFound is returned when a run id is not present in the store.
var ErrRunNotFound = errors.New("sdnruns: run not found")

// Store persists supplemental-OMM runs and tracks the currently executing run.
// Each run is one JSON file under dir (<repo>/sdn/runs/<id>.json, 0600, atomic
// writes); the store also keeps every run in memory (loaded at open) so the API
// serves list/get/search cheaply. An empty dir selects no-persistence mode: runs
// live only in memory (they do not survive a restart), which keeps a node with no
// repo path working.
type Store struct {
	dir string

	mu     sync.Mutex
	runs   map[string]*Run // id -> run (store-owned copy)
	order  []string        // ids, newest-first
	liveID string          // id of the running run, "" when idle
}

// NewStore opens (creating if needed) the runs directory and loads any persisted
// runs. An empty dir selects no-persistence mode.
func NewStore(dir string) (*Store, error) {
	dir = strings.TrimSpace(dir)
	s := &Store{dir: dir, runs: make(map[string]*Run)}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sdnruns: create runs dir %q: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("sdnruns: read runs dir %q: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r Run
		if err := jsonUnmarshal(data, &r); err != nil || r.ID == "" {
			continue
		}
		// A run left "running" by a crash is reconciled to failed at load.
		if r.Status == StatusRunning {
			r.Status = StatusFailed
			if r.Error == "" {
				r.Error = "run interrupted (node restarted mid-run)"
			}
		}
		s.runs[r.ID] = &r
	}
	s.reorderLocked()
	return s, nil
}

// NewRunID mints a sortable, unique run id from the start time.
func NewRunID(started time.Time) string {
	return fmt.Sprintf("run-%s", started.UTC().Format("20060102T150405.000Z"))
}

// StartRun registers a new run as the live/current run and persists it. The run
// must have a unique ID, Status=StatusRunning and a Started time.
func (s *Store) StartRun(r *Run) error {
	if r == nil || strings.TrimSpace(r.ID) == "" {
		return errors.New("sdnruns: run requires an id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.runs[r.ID]; exists {
		return fmt.Errorf("sdnruns: run %q already exists", r.ID)
	}
	if r.Objects == nil {
		r.Objects = []ObjectResult{}
	}
	r.Status = StatusRunning
	r.recompute()
	stored := r.clone()
	s.runs[r.ID] = stored
	s.liveID = r.ID
	s.reorderLocked()
	return s.persistLocked(stored)
}

// AppendObject appends one fitted object to a run, refreshes its aggregates, and
// persists. Unknown id -> ErrRunNotFound.
func (s *Store) AppendObject(id string, obj ObjectResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return ErrRunNotFound
	}
	r.Objects = append(r.Objects, obj)
	r.recompute()
	return s.persistLocked(r)
}

// FinishRun stamps a run finished with the given status (StatusCompleted or
// StatusFailed) and clears the live pointer when it was the live run.
func (s *Store) FinishRun(id, status, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return ErrRunNotFound
	}
	now := time.Now().UTC()
	r.Finished = &now
	r.Status = status
	if errMsg != "" {
		r.Error = errMsg
	}
	r.recompute()
	if s.liveID == id {
		s.liveID = ""
	}
	return s.persistLocked(r)
}

// List returns every run's summary, newest-first.
func (s *Store) List() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Summary, 0, len(s.order))
	for _, id := range s.order {
		if r := s.runs[id]; r != nil {
			out = append(out, r.summary())
		}
	}
	return out
}

// Get returns a deep copy of one run (summary + all object rows). Unknown id ->
// ErrRunNotFound.
func (s *Store) Get(id string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, ErrRunNotFound
	}
	return *r.clone(), nil
}

// Objects returns a run's per-object rows, optionally filtered by a NORAD search
// substring (matched against the NORAD id and the object name/international id).
// Unknown id -> ErrRunNotFound.
func (s *Store) Objects(id, search string) ([]ObjectResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return nil, ErrRunNotFound
	}
	search = strings.TrimSpace(strings.ToLower(search))
	out := make([]ObjectResult, 0, len(r.Objects))
	for _, obj := range r.clone().Objects {
		if search != "" && !objectMatches(obj, search) {
			continue
		}
		out = append(out, obj)
	}
	return out, nil
}

// Object returns one object row from a run by NORAD id. Unknown id/norad ->
// ErrRunNotFound.
func (s *Store) Object(id string, norad uint32) (ObjectResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return ObjectResult{}, ErrRunNotFound
	}
	for _, obj := range r.clone().Objects {
		if obj.Norad == norad {
			return obj, nil
		}
	}
	return ObjectResult{}, ErrRunNotFound
}

// Live returns a snapshot of the currently executing run, or false when idle.
func (s *Store) Live() (LiveRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.liveID == "" {
		return LiveRun{}, false
	}
	r, ok := s.runs[s.liveID]
	if !ok {
		return LiveRun{}, false
	}
	remaining := r.ObjectsTotal - r.ObjectsDone
	if remaining < 0 {
		remaining = 0
	}
	elapsed := time.Since(r.Started).Seconds()
	var eta float64
	if r.ObjectsDone > 0 && remaining > 0 {
		eta = (elapsed / float64(r.ObjectsDone)) * float64(remaining)
	}
	return LiveRun{
		ID:               r.ID,
		Started:          r.Started,
		Providers:        append([]string(nil), r.Providers...),
		ObjectsTotal:     r.ObjectsTotal,
		ObjectsDone:      r.ObjectsDone,
		ObjectsRemaining: remaining,
		CurrentAvgRMS:    r.AvgRMS,
		ElapsedSeconds:   elapsed,
		RemainingSeconds: eta,
	}, true
}

// objectMatches reports whether obj matches a lowercased search token.
func objectMatches(obj ObjectResult, search string) bool {
	if strings.Contains(fmt.Sprintf("%d", obj.Norad), search) {
		return true
	}
	if strings.Contains(strings.ToLower(obj.ObjectName), search) {
		return true
	}
	if strings.Contains(strings.ToLower(obj.ObjectID), search) {
		return true
	}
	return false
}

// reorderLocked rebuilds the newest-first id ordering. Caller holds s.mu.
func (s *Store) reorderLocked() {
	s.order = s.order[:0]
	for id := range s.runs {
		s.order = append(s.order, id)
	}
	sort.Slice(s.order, func(i, j int) bool {
		ri, rj := s.runs[s.order[i]], s.runs[s.order[j]]
		if ri == nil || rj == nil {
			return s.order[i] > s.order[j]
		}
		if ri.Started.Equal(rj.Started) {
			return s.order[i] > s.order[j]
		}
		return ri.Started.After(rj.Started)
	})
}

// persistLocked writes a run to disk atomically (no-op in no-persistence mode).
// Caller holds s.mu.
func (s *Store) persistLocked(r *Run) error {
	if s.dir == "" {
		return nil
	}
	name, err := safeRunName(r.ID)
	if err != nil {
		return err
	}
	data, err := jsonMarshalIndent(r)
	if err != nil {
		return fmt.Errorf("sdnruns: encode run %q: %w", r.ID, err)
	}
	tmp, err := os.CreateTemp(s.dir, "."+name+".*.tmp")
	if err != nil {
		return fmt.Errorf("sdnruns: temp run %q: %w", r.ID, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("sdnruns: write run %q: %w", r.ID, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("sdnruns: chmod run %q: %w", r.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sdnruns: close run %q: %w", r.ID, err)
	}
	if err := os.Rename(tmpPath, filepath.Join(s.dir, name+".json")); err != nil {
		return fmt.Errorf("sdnruns: commit run %q: %w", r.ID, err)
	}
	return nil
}

// safeRunName maps a run id to a filesystem-safe base name.
func safeRunName(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("sdnruns: empty run id")
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("sdnruns: unsafe run id %q", id)
	}
	return name, nil
}
