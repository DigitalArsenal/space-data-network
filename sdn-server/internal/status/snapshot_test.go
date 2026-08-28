package status

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/status/nst"
)

// scriptedBuilder returns the next frame/error from a script on each call.
type scriptedBuilder struct {
	mu     sync.Mutex
	steps  [][]byte
	errs   []error
	calls  int
	frames [][]byte
}

func (s *scriptedBuilder) build() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	if i >= len(s.steps) {
		i = len(s.steps) - 1
	}
	s.calls++
	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}
	frame := s.steps[i]
	s.frames = append(s.frames, frame)
	return frame, nil
}

func TestSnapshotCacheLane(t *testing.T) {
	boom := errors.New("store busy")

	cases := []struct {
		name string
		// steps/errs drive successive builds; one Refresh per step.
		steps      [][]byte
		errs       []error
		wantFrame  string
		wantGen    uint64
		wantErr    error
		wantRefErr bool
	}{
		{
			name:      "single build serves that frame",
			steps:     [][]byte{[]byte("a")},
			wantFrame: "a",
			wantGen:   1,
		},
		{
			name:      "unchanged frame holds the generation",
			steps:     [][]byte{[]byte("a"), []byte("a"), []byte("a")},
			wantFrame: "a",
			wantGen:   1,
		},
		{
			name:      "changed frame increments the generation",
			steps:     [][]byte{[]byte("a"), []byte("b")},
			wantFrame: "b",
			wantGen:   2,
		},
		{
			name:       "build error keeps the previous good frame",
			steps:      [][]byte{[]byte("a"), nil},
			errs:       []error{nil, boom},
			wantFrame:  "a",
			wantGen:    1,
			wantErr:    boom,
			wantRefErr: true,
		},
		{
			name:       "error before any good build leaves the lane cold",
			steps:      [][]byte{nil},
			errs:       []error{boom},
			wantFrame:  "",
			wantGen:    0,
			wantErr:    boom,
			wantRefErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb := &scriptedBuilder{steps: tc.steps, errs: tc.errs}
			c := NewSnapshotCache(LaneConfig{Name: "stats", Interval: time.Hour, Build: sb.build})

			var lastErr error
			for range tc.steps {
				lastErr = c.Refresh("stats")
			}

			if tc.wantRefErr && lastErr == nil {
				t.Fatalf("Refresh returned nil, want %v", tc.wantErr)
			}
			if !tc.wantRefErr && lastErr != nil {
				t.Fatalf("Refresh returned %v, want nil", lastErr)
			}

			snap, ok := c.Frame("stats")
			if !ok {
				t.Fatal("Frame reported no lane")
			}
			if got := string(snap.Frame); got != tc.wantFrame {
				t.Errorf("frame = %q, want %q", got, tc.wantFrame)
			}
			if snap.Generation != tc.wantGen {
				t.Errorf("generation = %d, want %d", snap.Generation, tc.wantGen)
			}
			if !errors.Is(snap.Err, tc.wantErr) {
				t.Errorf("lane error = %v, want %v", snap.Err, tc.wantErr)
			}
		})
	}
}

func TestSnapshotCacheServesUnchangedBetweenRefreshes(t *testing.T) {
	sb := &scriptedBuilder{steps: [][]byte{[]byte("frame-1")}}
	// An hour-long interval means nothing rebuilds during the test: every read
	// must return the frame the single warm-up build produced, byte for byte.
	c := NewSnapshotCache(LaneConfig{Name: "stats", Interval: time.Hour, Build: sb.build})
	c.Start()
	defer c.Stop()

	deadline := time.Now().Add(2 * time.Second)
	var snap Snapshot
	for time.Now().Before(deadline) {
		s, _ := c.Frame("stats")
		if s.Generation > 0 {
			snap = s
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snap.Generation == 0 {
		t.Fatal("lane never warmed")
	}

	for i := 0; i < 5; i++ {
		s, ok := c.Frame("stats")
		if !ok {
			t.Fatal("Frame reported no lane")
		}
		if string(s.Frame) != "frame-1" || s.Generation != snap.Generation {
			t.Fatalf("read %d = %q/gen %d, want frame-1/gen %d", i, s.Frame, s.Generation, snap.Generation)
		}
	}

	sb.mu.Lock()
	calls := sb.calls
	sb.mu.Unlock()
	if calls != 1 {
		t.Errorf("build ran %d times, want 1 — reads must not trigger builds", calls)
	}
}

func TestSnapshotCacheUnknownLane(t *testing.T) {
	c := NewSnapshotCache()
	if _, ok := c.Frame("stats"); ok {
		t.Error("Frame reported an unknown lane as present")
	}
	if err := c.Refresh("stats"); !errors.Is(err, ErrNoLane) {
		t.Errorf("Refresh error = %v, want %v", err, ErrNoLane)
	}
	// Stop before Start must not panic or hang.
	c.Stop()
}

func TestDashboardStatsFrameRoundTrip(t *testing.T) {
	frame := BuildDashboardStatsSet(DashboardStatsInput{
		Schemas: []DashboardSchemaRow{{Schema: "OMM", RecordCount: 10847, TotalBytes: 4200000}},
		Sources: []DashboardSourceRow{{
			Schema:       "OMM",
			ProviderID:   "celestrak",
			SourceName:   "gp",
			BatchID:      "b-1",
			RecordCount:  10847,
			TotalBytes:   4200000,
			LastIngestAt: 1756000000,
		}},
		TotalRecords: 10847,
		TotalBytes:   4200000,
		Stale:        true,
		AsOf:         time.Unix(1756000000, 0),
		Now:          time.Unix(1756000123, 0),
	})

	if !nst.SizePrefixedDashboardStatsSetBufferHasIdentifier(frame) {
		t.Fatalf("frame does not carry the $NDS identifier: % x", frame[:12])
	}
	// Both frames ride one socket; they must stay distinguishable.
	if nst.SizePrefixedNodeStatusSetBufferHasIdentifier(frame) {
		t.Fatal("dashboard frame is indistinguishable from a node-status frame")
	}

	set := nst.GetSizePrefixedRootAsDashboardStatsSet(frame, 0)
	if got := set.GENERATED_AT(); got != 1756000123 {
		t.Errorf("GENERATED_AT = %d, want 1756000123", got)
	}
	if got := set.AS_OF(); got != 1756000000 {
		t.Errorf("AS_OF = %d, want 1756000000", got)
	}
	if !set.STALE() {
		t.Error("STALE = false, want true — a budgeted read is reported as stale")
	}
	if got := set.TOTAL_RECORDS(); got != 10847 {
		t.Errorf("TOTAL_RECORDS = %d, want 10847", got)
	}
	if got := set.SchemasLength(); got != 1 {
		t.Fatalf("SCHEMAS length = %d, want 1", got)
	}
	var schema nst.DashboardSchemaStat
	set.SCHEMAS(&schema, 0)
	if got := string(schema.SCHEMA()); got != "OMM" {
		t.Errorf("SCHEMA = %q, want OMM", got)
	}
	if got := schema.RECORD_COUNT(); got != 10847 {
		t.Errorf("RECORD_COUNT = %d, want 10847", got)
	}
	var source nst.DashboardSourceStat
	if got := set.SourcesLength(); got != 1 {
		t.Fatalf("SOURCES length = %d, want 1", got)
	}
	set.SOURCES(&source, 0)
	if got := string(source.PROVIDER_ID()); got != "celestrak" {
		t.Errorf("PROVIDER_ID = %q, want celestrak", got)
	}
	if got := source.LAST_INGEST_AT(); got != 1756000000 {
		t.Errorf("LAST_INGEST_AT = %d, want 1756000000", got)
	}
}
