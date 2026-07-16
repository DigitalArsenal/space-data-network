package sdncron

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/kubo/sdn/plugins"
)

// capturingModule records the input payload each scheduled fire receives — the
// seam the config-driven data-source pull config (timer_input, e.g. objectCap)
// rides on.
type capturingModule struct {
	mu       sync.Mutex
	lastseen []byte
	got      bool
}

func (c *capturingModule) ID() string { return "capture-mod" }

func (c *capturingModule) CronMethods() []plugins.CronMethodSpec {
	return []plugins.CronMethodSpec{{
		Method:          "pull",
		Description:     "test pull",
		DefaultInterval: "20ms",
		Input:           "json",
		Output:          "json",
	}}
}

func (c *capturingModule) InvokeCron(_ context.Context, _ string, input []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastseen = append([]byte(nil), input...)
	c.got = true
	return []byte("{}"), nil
}

func (c *capturingModule) seen() ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastseen, c.got
}

// TestTimerInputPassthrough proves the scheduler passes a module's configured
// timer_input object as the invoke payload on each fire (and nil when unset).
func TestTimerInputPassthrough(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	s := NewScheduler(store, nil)

	mod := &capturingModule{}
	if err := s.Register(Registration{Module: mod, Name: "Capture", Version: "0.0.1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Seed a config-driven pull config: a high objectCap via timer_input.
	if _, err := s.ApplyConfig(mod.ID(), ModuleConfig{
		"timer_input": map[string]interface{}{"objectCap": 100000},
	}); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := mod.seen(); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, ok := mod.seen()
	if !ok {
		t.Fatalf("timer never fired")
	}
	want := `{"objectCap":100000}`
	if string(got) != want {
		t.Fatalf("fire input = %q, want %q", string(got), want)
	}
}
