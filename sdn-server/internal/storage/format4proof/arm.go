package format4proof

import (
	"fmt"
	"os"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
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
// compile. Format 4's artifact is prewarmed by the backend's open path.
func PrewarmAOT() error {
	cache := storage.EngineAOTCacheDir()
	if _, _, err := flatsqlrt.PrewarmEngineAOT(cache); err != nil {
		return fmt.Errorf("prewarm the format-1 engine: %w", err)
	}
	if _, _, err := flatsqlrt.PrewarmPSThreadsAOT(cache); err != nil {
		return fmt.Errorf("prewarm the format-2 engine: %w", err)
	}
	return nil
}

// OpenArm opens the store directory dir (always a clone) as arm, through
// storage.NewFlatSQLStore exactly as the daemon does, and checks that the
// store opened in the arm's format. It returns the open time.
func OpenArm(arm, dir string, opts ...storage.StoreOption) (*storage.FlatSQLStore, float64, error) {
	switch f := ArmFormat(arm); f {
	case "":
		os.Unsetenv(format2.FormatEnv)
	default:
		os.Setenv(format2.FormatEnv, f)
	}
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
