package format4proof

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// host02QuotaBytes is host-02's storage.max_size (25 GB), the quota format 2
// opens with in every arm run, as the earlier baselines did.
const host02QuotaBytes = 25 << 30

// format4Store is the record backend's selector (contract §5.4: Format4()
// is added beside Format2()). Asserted at run time, so this harness builds
// before the backend lands and refuses a format-4 arm until it has.
type format4Store interface{ Format4() bool }

// IsFormat4 reports whether s runs on store format 4.
func IsFormat4(s *storage.FlatSQLStore) bool {
	f, ok := any(s).(format4Store)
	return ok && f.Format4()
}

// PrewarmAOT compiles the engines an arm runs into the daemon's AOT cache
// (what `spacedatanetwork prewarm-aot` does), so no measurement includes a
// compile and no store open misses its artifact: the daemon's format-4 open
// never compiles. A build without a format-4 engine skips it (arm s then
// fails at its open, naming ErrNoP4Artifact).
func PrewarmAOT() error {
	cache := storage.EngineAOTCacheDir()
	if _, _, err := flatsqlrt.PrewarmEngineAOT(cache); err != nil {
		return fmt.Errorf("prewarm the format-1 engine: %w", err)
	}
	if _, _, err := flatsqlrt.PrewarmPSThreadsAOT(cache); err != nil {
		return fmt.Errorf("prewarm the format-2 engine: %w", err)
	}
	if _, _, err := flatsqlrt.PrewarmP4ThreadsAOT(cache); err != nil && !errors.Is(err, flatsqlrt.ErrNoP4Artifact) {
		return fmt.Errorf("prewarm the format-4 engine: %w", err)
	}
	return nil
}

// OpenArm opens the store directory dir (always a clone) as arm, through
// storage.NewFlatSQLStore exactly as the daemon does, and checks that the
// store opened in the arm's format. It returns the open time.
func OpenArm(arm, dir string, opts ...storage.StoreOption) (*storage.FlatSQLStore, float64, error) {
	os.Setenv(format2.FormatEnv, ArmFormat(arm))
	v, err := sds.NewValidator(nil)
	if err != nil {
		return nil, 0, err
	}
	all := []storage.StoreOption{storage.WithDeferredBootRebuilds()}
	if arm == ArmF2 {
		all = append(all, storage.WithQuotaBytes(host02QuotaBytes))
	}
	all = append(all, opts...)
	st := time.Now()
	s, err := storage.NewFlatSQLStore(dir, v, all...)
	ms := float64(time.Since(st).Microseconds()) / 1000
	if err != nil {
		return nil, ms, fmt.Errorf("open %s store: %w", arm, err)
	}
	var ok bool
	switch arm {
	case ArmF1:
		ok = !s.Format2() && !IsFormat4(s)
	case ArmF2:
		ok = s.Format2()
	case ArmS:
		ok = IsFormat4(s)
	}
	if !ok {
		_ = s.Close()
		return nil, ms, fmt.Errorf("the store opened in another format than arm %s (format2=%v format4=%v): is the format-4 backend in this build?",
			arm, s.Format2(), IsFormat4(s))
	}
	return s, ms, nil
}

// settleLimit bounds SettleStore's wait for the full-text builds (the
// fixture's four types build in about 5 minutes).
const settleLimit = 45 * time.Minute

// SettleStore brings a format-4 store to the state a daemon leaves it in
// once its background work is done: it opens dir through SDN as the daemon
// does, waits until the full-text index of every fixture type whose spec
// enables full text (format4.TypeSpecFor) is ready, and closes the store
// (WAL at rest). It returns each type's full-text state and the time taken.
func SettleStore(dir string) (map[string]string, time.Duration, error) {
	st := time.Now()
	if err := PrewarmAOT(); err != nil {
		return nil, 0, err
	}
	s, _, err := OpenArm(ArmS, dir)
	if err != nil {
		return nil, time.Since(st), err
	}
	if _, _, err := s.WarmFullTextIndexes(); err != nil {
		_ = s.Close()
		return nil, time.Since(st), fmt.Errorf("settle %s: %w", dir, err)
	}
	states := map[string]string{}
	for {
		pending := 0
		for _, schema := range pointSchemas {
			spec, err := format4.TypeSpecFor(schema)
			if err != nil {
				_ = s.Close()
				return states, time.Since(st), err
			}
			states[schema] = s.FullTextIndexState(schema)
			if spec.FullText && states[schema] != "ready" {
				pending++
			}
		}
		if pending == 0 {
			break
		}
		if time.Since(st) > settleLimit {
			_ = s.Close()
			return states, time.Since(st), fmt.Errorf("settle %s: full text not ready after %s: %v", dir, settleLimit, states)
		}
		time.Sleep(2 * time.Second)
	}
	return states, time.Since(st), s.Close()
}

// StoreRecords is the store's unique live records over the fixture's four
// types (format 1's Count per schema).
func StoreRecords(s *storage.FlatSQLStore) (int64, error) {
	var n int64
	for _, schema := range pointSchemas {
		c, err := s.Count(schema)
		if err != nil {
			return 0, fmt.Errorf("count %s: %w", schema, err)
		}
		n += c
	}
	return n, nil
}

// guard runs fn with a wall-clock limit. A call that overruns is reported as
// a timeout and its goroutine abandoned (the engine's own guard then fences
// it), so one hung call cannot hang a whole run.
func guard(limit time.Duration, fn func()) (time.Duration, bool) {
	done := make(chan struct{})
	st := time.Now()
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
		return time.Since(st), true
	case <-time.After(limit):
		return time.Since(st), false
	}
}
