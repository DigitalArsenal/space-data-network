package wasmrt

import (
	"os"
	"testing"
	"time"
)

func TestWasiThreadsShareMemoryAndWake(t *testing.T) {
	bytes, err := os.ReadFile("testdata/wasi-threads.wasm")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewModule(bytes, WithMaxMemoryPages(2), WithExecTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Release()
	for i := 0; i < 10; i++ {
		v, err := m.Execute("run")
		if err != nil {
			t.Fatal(err)
		}
		if len(v) != 1 || ToInt32(v[0]) != 42 {
			t.Fatalf("shared worker result: %v", v)
		}
	}
	if m.threads == nil || m.threads.next != 11 {
		t.Fatal("expected ten real worker spawns")
	}
	if !m.threads.stop() {
		t.Fatal("workers did not stop")
	}
}

func TestWasiThreadCapacityAndTrap(t *testing.T) {
	bytes, err := os.ReadFile("testdata/wasi-threads.wasm")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("bounded workers", func(t *testing.T) {
		m, err := NewModule(bytes, WithMaxMemoryPages(2), WithExecTimeout(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		defer m.Release()
		v, err := m.Execute("capacity")
		if err != nil {
			t.Fatal(err)
		}
		if len(v) != 1 || ToInt32(v[0]) != 1 {
			t.Fatalf("expected 33rd worker refused: %v", v)
		}
		if !m.threads.stop() {
			t.Fatal("bounded workers failed to stop")
		}
	})
	t.Run("worker trap poisons instance", func(t *testing.T) {
		m, err := NewModule(bytes, WithMaxMemoryPages(2), WithExecTimeout(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		defer m.Release()
		if _, err = m.Execute("trap_worker"); err != nil && !m.Poisoned() {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for !m.Poisoned() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !m.Poisoned() {
			t.Fatal("worker trap did not poison instance")
		}
		if _, err = m.Execute("run"); err == nil {
			t.Fatal("poisoned module allowed re-entry")
		}
	})
}

func TestWorkerTrapInterruptsWaitingParent(t *testing.T) {
	bytes, err := os.ReadFile("testdata/wasi-threads.wasm")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewModule(bytes, WithMaxMemoryPages(2), WithExecTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Release()
	start := time.Now()
	_, err = m.Execute("join_trapped_worker")
	if !IsPoisoned(err) {
		t.Fatalf("expected worker poison, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("parent waited for full timeout after child trapped")
	}
}

func TestThreadedAtomicWaitReturnsWithinCancellationBudget(t *testing.T) {
	bytes, err := os.ReadFile("testdata/wasi-threads.wasm")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewModule(bytes, WithMaxMemoryPages(2), WithExecTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Release()
	start := time.Now()
	_, err = m.Execute("unbounded_atomic_wait")
	if err == nil {
		t.Fatal("unbounded wait succeeded")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("native wait blocked caller beyond cancellation budget")
	}
	if !m.Poisoned() {
		t.Fatal("timed out threaded module was reusable")
	}
}

// The upstream 0.16.4 library fails this standard atomic-notify contract.
// Run explicitly when validating the isolated runtime patch; keep upstream
// library users able to run the unrelated host tests during rollout.
func TestWasiAtomicNotifyWithoutStore(t *testing.T) {
	if os.Getenv("SDN_WASM_ATOMIC_REGRESSION") != "1" {
		t.Skip("set SDN_WASM_ATOMIC_REGRESSION=1 to verify the WasmEdge atomic-wait runtime fix")
	}
	bytes, err := os.ReadFile("testdata/wasi-threads.wasm")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewModule(bytes, WithMaxMemoryPages(2), WithExecTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Release()
	values, err := m.Execute("notify_without_store")
	if err != nil {
		t.Fatalf("notify must wake a waiter even without a store: %v", err)
	}
	if len(values) != 1 || ToInt32(values[0]) != 0 {
		t.Fatalf("atomic wait returned %v, expected notified (0)", values)
	}
}
