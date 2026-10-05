package storage

// Store format 4 is the default (storeFormatFor), through NewFlatSQLStore as
// the daemon opens its store, on the real engines (each skips without the
// patched runtime):
//   - an empty data directory is created as format 4 and reopens as format 4;
//   - a format-1 store keeps format 1, with a notice naming
//     store-migrate --to 4, and SDN_STORE_FORMAT=1 opens it without one;
//   - an activated format-2 store is refused by name;
//   - any other SDN_STORE_FORMAT value is an error.

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	logging "github.com/ipfs/go-log/v2"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// storageLogs collects the store's log lines, at the daemon's level (info),
// while a test opens it.
type storageLogs struct {
	pr    *logging.PipeReader
	done  chan struct{}
	mu    sync.Mutex
	buf   bytes.Buffer
	level string
}

func captureStorageLogs() *storageLogs {
	level, _ := logging.SubsystemLevelName("storage")
	_ = logging.SetLogLevel("storage", "info")
	c := &storageLogs{pr: logging.NewPipeReader(logging.PipeFormat(logging.PlaintextOutput), logging.PipeLevel(logging.LevelInfo)),
		done: make(chan struct{}), level: level}
	go func() {
		defer close(c.done)
		b := make([]byte, 64<<10)
		for {
			n, err := c.pr.Read(b)
			c.mu.Lock()
			c.buf.Write(b[:n])
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *storageLogs) stop() string {
	_ = c.pr.Close()
	<-c.done
	if c.level != "" {
		_ = logging.SetLogLevel("storage", c.level)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func TestFormat4IsTheDefault(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		t.Setenv(checkpointIntervalEnv, "0")
		v := bootTestValidator(t)
		const notice = "holds a format-1 store: it opens as format 1. To migrate it, stop the daemon and run `spacedatanetwork store-migrate --to 4"

		t.Setenv(format4.FormatEnv, "")
		fresh := t.TempDir()
		for open := 1; open <= 2; open++ {
			s, err := NewFlatSQLStore(fresh, v)
			if err != nil {
				t.Fatalf("open %d of an empty data directory: %v", open, err)
			}
			f4 := s.Format4()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if !f4 {
				t.Fatalf("open %d of an empty data directory is not format 4", open)
			}
		}
		if m, err := marker.Read(fresh); err != nil || !m.Activated() || m.MigratedFrom != 0 || !m.LegacyControlDir {
			t.Fatalf("an empty data directory, unset: %+v %v", m, err)
		}

		// A format-1 store keeps format 1 and says how to migrate it.
		t.Setenv(format4.FormatEnv, "1")
		legacy := t.TempDir()
		l := reopenDeferred(t, legacy)
		rec := f2TestOMM(41000, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), "DEFAULT-FORMAT")
		cid, err := l.Store("OMM.fbs", rec, "peer-a", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		for _, sel := range []string{"", "1"} {
			t.Setenv(format4.FormatEnv, sel)
			logs := captureStorageLogs()
			s, err := NewFlatSQLStore(legacy, v)
			said := logs.stop()
			if err != nil {
				t.Fatalf("SDN_STORE_FORMAT=%q on a format-1 store: %v", sel, err)
			}
			got, gerr := s.GetRecord("OMM.fbs", cid)
			format1 := !s.Format4() && !s.Format2()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if !format1 || gerr != nil || !bytes.Equal(got.Data, rec) {
				t.Fatalf("SDN_STORE_FORMAT=%q on a format-1 store: format 1 %v, GetRecord %v", sel, format1, gerr)
			}
			if strings.Contains(said, notice) != (sel == "") {
				t.Fatalf("SDN_STORE_FORMAT=%q on a format-1 store: the notice (%q) logged %v:\n%s", sel, notice, sel == "", said)
			}
		}
		if m, err := marker.Read(legacy); err != nil || m.Format4() || !m.LegacyControlFile {
			t.Fatalf("the format-1 store after its opens: %+v %v", m, err)
		}

		// An activated format-2 store is refused by name.
		f2 := t.TempDir()
		s2 := openFormat2ForTest(t, f2)
		if err := s2.Close(); err != nil {
			t.Fatal(err)
		}
		t.Setenv(format4.FormatEnv, "")
		if _, err := NewFlatSQLStore(f2, v); !errors.Is(err, ErrFormat2Store) || !strings.Contains(err.Error(), "SDN_STORE_FORMAT=2") {
			t.Fatalf("unset on a format-2 store: %v, want ErrFormat2Store naming SDN_STORE_FORMAT=2", err)
		}

		t.Setenv(format4.FormatEnv, "3")
		if _, err := NewFlatSQLStore(t.TempDir(), v); err == nil || !strings.Contains(err.Error(), format4.FormatEnv) {
			t.Fatalf("SDN_STORE_FORMAT=3: %v, want an error naming %s", err, format4.FormatEnv)
		}
	})
}
