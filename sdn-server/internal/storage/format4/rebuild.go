package format4

import (
	"context"
	"fmt"
	"time"
)

// WaitFTSReady waits for the asynchronous full-text rebuild to finish.
// An empty type selects every full-text index. Disabled indexes need no work.
// Ordinary ingest lag does not change the ready state reported by SUMMARY.
func WaitFTSReady(ctx context.Context, api interface {
	FTS(context.Context) ([]FTSState, error)
}, typ string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		states, err := api.FTS(ctx)
		if err != nil {
			return err
		}
		ready := true
		for _, state := range states {
			if typ != "" && state.Type != typ {
				continue
			}
			switch state.State {
			case "ready", "off":
			case "building":
				ready = false
			default:
				return fmt.Errorf("full-text index %s has unknown state %q", state.Type, state.State)
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
