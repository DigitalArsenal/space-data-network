package flatsqlrt

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// p4TestWasm is the engine the p4 tests run: SDN_P4_WASM (a build of the
// engine's task branch, for development) or the embedded release. Without
// either the tests skip.
func p4TestWasm(t testing.TB) []byte {
	t.Helper()
	if p := os.Getenv("SDN_P4_WASM"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if len(P4ThreadsWasm()) == 0 {
		t.Skip("no format-4 engine: none is embedded yet and SDN_P4_WASM is unset")
	}
	return P4ThreadsWasm()
}

// p4Config is a minimal flatsql_p4_init config: root, create mode 1, 4 cores.
func p4Config(root string) []byte {
	tlv := func(out []byte, tag uint16, v []byte) []byte {
		out = binary.LittleEndian.AppendUint16(out, tag)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(v)))
		return append(out, v...)
	}
	c := tlv(nil, 1, []byte(filepath.Join(root, "fsql4")))
	c = tlv(c, 2, []byte{1})
	return tlv(c, 13, binary.LittleEndian.AppendUint32(nil, 4))
}

func openP4(t testing.TB, root string, onFail func(*P4Instance, error)) *P4Instance {
	t.Helper()
	requirePatched(t)
	p, err := OpenP4Instance(P4Config{Wasm: p4TestWasm(t), AOTCacheDir: sharedPSAOTDir(t), CompileOnMiss: true,
		StoreRoot: root, InitConfig: p4Config(root), OnFailure: onFail})
	if err != nil {
		t.Fatalf("OpenP4Instance: %v", err)
	}
	return p
}

// The instance boots only AOT-compiled: the artifact under the fsqlp4
// prefix, its layout at version 1, its service threads started; without the
// artifact in the cache it refuses to start (no interpreter fallback).
func TestP4InstanceBootsAOTOnly(t *testing.T) {
	requirePatched(t)
	wasm := p4TestWasm(t)
	if _, err := OpenP4Instance(P4Config{Wasm: wasm, AOTCacheDir: t.TempDir(), StoreRoot: t.TempDir(),
		InitConfig: p4Config(t.TempDir())}); !errors.Is(err, ErrPSSubstrate) {
		t.Fatalf("open without the AOT artifact: %v, want ErrPSSubstrate", err)
	}
	root := t.TempDir()
	p := openP4(t, root, nil)
	defer p.StopWithin(10 * time.Second)
	if !strings.HasPrefix(filepath.Base(p.AOTPath()), P4ThreadsAOTPrefix+"-") {
		t.Fatalf("AOT artifact %s", p.AOTPath())
	}
	lay := p.EngineLayout()
	if len(lay) != P4LayoutBytes || binary.LittleEndian.Uint32(lay) != 1 {
		t.Fatalf("layout %d bytes, version %d", len(lay), binary.LittleEndian.Uint32(lay))
	}
	if n := int(binary.LittleEndian.Uint32(lay[120:])); p.Started() < 1 || n < 1 {
		t.Fatalf("started %d, layout threads %d", p.Started(), n)
	}
	b, rc, err := p.ControlOut("flatsql_p4_stats", 8*256)
	if err != nil || rc < 8*40 || len(b) != int(rc) {
		t.Fatalf("stats: %d bytes, rc %d, %v", len(b), rc, err)
	}
}

// Stop drains within its deadline and leaves no service thread.
func TestP4InstanceStopDrainsWithinTheDeadline(t *testing.T) {
	p := openP4(t, t.TempDir(), nil)
	start := time.Now()
	rc, err := p.StopWithin(5 * time.Second)
	took := time.Since(start)
	if err != nil || rc != 0 {
		t.Fatalf("stop: rc %d, %v", rc, err)
	}
	if took > 6*time.Second {
		t.Fatalf("stop took %s, deadline 5s", took)
	}
	if live := p.Stats().Threads.Live; live != 0 {
		t.Fatalf("%d service threads live after stop", live)
	}
	if p.Enter() {
		t.Fatal("a stopped instance admits accessors")
	}
	t.Logf("stop drained in %s", took.Round(time.Millisecond))
}

// A trap fences only its own instance; reopening the store replays it.
func TestP4TrapFencesOnlyItsInstance(t *testing.T) {
	failed := make(chan error, 1)
	victimRoot := t.TempDir()
	victim := openP4(t, victimRoot, func(_ *P4Instance, err error) { failed <- err })
	other := openP4(t, t.TempDir(), nil)
	defer other.StopWithin(10 * time.Second)
	victim.Fence(errors.New("injected trap"))
	select {
	case err := <-failed:
		t.Logf("victim fenced: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("OnFailure did not run")
	}
	if victim.Failure() == nil || victim.Enter() {
		t.Fatal("the fenced instance is still live")
	}
	if other.Failure() != nil || other.Stats().Poisoned {
		t.Fatal("another instance was affected")
	}
	if _, rc, err := other.ControlOut("flatsql_p4_stats", 8*256); err != nil || rc <= 0 {
		t.Fatalf("the other instance after the trap: rc %d, %v", rc, err)
	}
	_, _ = victim.StopWithin(5 * time.Second)
	again := openP4(t, victimRoot, nil)
	if _, err := again.StopWithin(10 * time.Second); err != nil {
		t.Fatalf("reopened store: %v", err)
	}
}
