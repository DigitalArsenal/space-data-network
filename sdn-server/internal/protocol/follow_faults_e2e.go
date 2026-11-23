//go:build sdn_e2e_follow_faults

package protocol

import (
	"context"
	"strings"
	"time"
)

// A build with faults in its followed lanes, for the publication real-daemon
// tests; no release build carries this tag: the statement of a lane whose
// source name starts with "slow-" is a store read that takes 10 minutes, or
// until its context ends (the peer resets the stream, or the read's bound).
func init() {
	followWorkFault = func(ctx context.Context, sourceName string) error {
		if !strings.HasPrefix(sourceName, "slow-") {
			return nil
		}
		t := time.NewTimer(10 * time.Minute)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
