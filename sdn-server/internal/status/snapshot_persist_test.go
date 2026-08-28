package status

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitForGeneration polls Frame until the lane reports at least gen, or fails.
func waitForGeneration(t *testing.T, c *SnapshotCache, name string, gen uint64) Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		snap, ok := c.Frame(name)
		if ok && snap.Generation >= gen {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("lane %s never reached generation %d (got %d)", name, gen, snap.Generation)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSnapshotCacheServesPersistedFrameAtBoot is the restart requirement: the
// previous boot's frame is served from the FIRST request, before this boot's
// first build has produced anything.
func TestSnapshotCacheServesPersistedFrameAtBoot(t *testing.T) {
	dir := t.TempDir()
	want := []byte("frame-from-the-previous-boot")

	first := NewSnapshotCache(LaneConfig{
		Name:     "stats",
		Interval: time.Hour,
		Build:    func() ([]byte, error) { return want, nil },
	})
	first.SetPersistDir(dir)
	first.Start()
	built := waitForGeneration(t, first, "stats", 1)
	first.Stop()

	if _, err := os.Stat(filepath.Join(dir, "lane-stats.bin")); err != nil {
		t.Fatalf("lane file not written: %v", err)
	}

	// Second boot: the build blocks, exactly like a store mid-hydration.
	release := make(chan struct{})
	second := NewSnapshotCache(LaneConfig{
		Name:     "stats",
		Interval: time.Hour,
		Build: func() ([]byte, error) {
			<-release
			return []byte("this-boot"), nil
		},
	})
	second.SetPersistDir(dir)
	second.Start()
	defer second.Stop()
	// LIFO: this closes BEFORE Stop runs, releasing the blocked build so
	// Stop's wg.Wait can return — deferring it before Stop deadlocked.
	defer close(release)

	snap, ok := second.Frame("stats")
	if !ok {
		t.Fatal("lane missing")
	}
	if snap.Generation == 0 {
		t.Fatal("Generation=0 at boot — the surface answers SNAPSHOT_COLD (this is the bug)")
	}
	if !bytes.Equal(snap.Frame, want) {
		t.Fatalf("Frame = %q, want the persisted %q", snap.Frame, want)
	}
	if !snap.Restored {
		t.Fatal("Restored=false for a frame loaded from the previous boot")
	}
	if snap.Generation != built.Generation {
		t.Fatalf("Generation = %d, want the persisted %d (an ETag must not regress across a restart)", snap.Generation, built.Generation)
	}
	if snap.BuiltAt.IsZero() || snap.BuiltAt.Unix() != built.BuiltAt.Unix() {
		t.Fatalf("BuiltAt = %v, want the persisted %v (AS_OF is how the client sees the age)", snap.BuiltAt, built.BuiltAt)
	}
}

// TestSnapshotCacheReplacesRestoredFrame: once this boot builds, the restored
// frame is superseded and Restored clears.
func TestSnapshotCacheReplacesRestoredFrame(t *testing.T) {
	dir := t.TempDir()

	first := NewSnapshotCache(LaneConfig{
		Name:     "stats",
		Interval: time.Hour,
		Build:    func() ([]byte, error) { return []byte("old"), nil },
	})
	first.SetPersistDir(dir)
	first.Start()
	waitForGeneration(t, first, "stats", 1)
	first.Stop()

	second := NewSnapshotCache(LaneConfig{
		Name:     "stats",
		Interval: time.Hour,
		Build:    func() ([]byte, error) { return []byte("new"), nil },
	})
	second.SetPersistDir(dir)
	second.Start()
	defer second.Stop()

	snap := waitForGeneration(t, second, "stats", 2)
	if !bytes.Equal(snap.Frame, []byte("new")) {
		t.Fatalf("Frame = %q, want the freshly built %q", snap.Frame, "new")
	}
	if snap.Restored {
		t.Fatal("Restored stayed true after this boot built the lane")
	}

	// And the file now holds the new frame. The write is WRITE-BEHIND on the
	// lane goroutine (a slow disk must never delay a Frame() read), so the
	// in-memory generation leads the file by design — poll, don't read once.
	deadline := time.Now().Add(5 * time.Second)
	for {
		frame, gen, _, err := readLaneFile(filepath.Join(dir, "lane-stats.bin"))
		if err == nil && bytes.Equal(frame, []byte("new")) && gen == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("persisted frame = %q gen %d (err %v), want %q gen 2", frame, gen, err, "new")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSnapshotCachePersistCorruptFile: an unreadable lane file starts cold, no
// error. Persistence is an optimization, never a dependency.
func TestSnapshotCachePersistCorruptFile(t *testing.T) {
	good := func() []byte {
		dir := t.TempDir()
		path := filepath.Join(dir, "lane-stats.bin")
		if err := writeLaneFile(path, Snapshot{Frame: []byte("ok"), Generation: 3, BuiltAt: time.Now()}); err != nil {
			t.Fatalf("writeLaneFile: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return raw
	}()

	cases := []struct {
		name    string
		content []byte
	}{
		{"empty", nil},
		{"short", []byte("SDNL")},
		{"bad magic", append([]byte("XXXXXXXX"), good[8:]...)},
		{"truncated frame", good[:len(good)-1]},
		{"trailing garbage", append(append([]byte{}, good...), 'x')},
		{"zero generation", func() []byte {
			b := append([]byte{}, good...)
			for i := 8; i < 16; i++ {
				b[i] = 0
			}
			return b
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "lane-stats.bin"), tc.content, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			release := make(chan struct{})
			c := NewSnapshotCache(LaneConfig{
				Name:     "stats",
				Interval: time.Hour,
				Build: func() ([]byte, error) {
					<-release
					return []byte("this-boot"), nil
				},
			})
			c.SetPersistDir(dir)
			c.Start()
			defer c.Stop()
			// LIFO: release the blocked build BEFORE Stop's wg.Wait runs.
			defer close(release)

			snap, ok := c.Frame("stats")
			if !ok {
				t.Fatal("lane missing")
			}
			if snap.Generation != 0 || snap.Frame != nil || snap.Restored {
				t.Fatalf("corrupt file produced snapshot %+v, want a cold lane", snap)
			}
		})
	}
}

// TestSnapshotCacheNoPersistDirStaysRAMOnly: the default construction must be
// byte-for-byte today's behavior — no file, no disk touch.
func TestSnapshotCacheNoPersistDirStaysRAMOnly(t *testing.T) {
	dir := t.TempDir()
	c := NewSnapshotCache(LaneConfig{
		Name:     "stats",
		Interval: time.Hour,
		Build:    func() ([]byte, error) { return []byte("x"), nil },
	})
	c.Start()
	waitForGeneration(t, c, "stats", 1)
	c.Stop()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("RAM-only cache wrote %d files", len(entries))
	}
}

// TestSnapshotCacheLaneFilePathRejectsUnsafeNames keeps a lane name from
// escaping the cache directory, even though today's names are constants.
func TestSnapshotCacheLaneFilePathRejectsUnsafeNames(t *testing.T) {
	c := NewSnapshotCache()
	c.SetPersistDir("/tmp/ui-cache")

	cases := []struct {
		name string
		want bool
	}{
		{"stats", true},
		{"peer_health", true},
		{"peer-health", true},
		{"../escape", false},
		{"a/b", false},
		{"a.b", false},
		{"", false},
	}
	for _, tc := range cases {
		got := c.laneFilePath(tc.name) != ""
		if got != tc.want {
			t.Fatalf("laneFilePath(%q) accepted=%v, want %v", tc.name, got, tc.want)
		}
	}
}
