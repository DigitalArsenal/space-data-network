//go:build sdn_e2e_follow_faults

package storage

import (
	"context"
	"strings"
	"time"
)

// A build with faults in its followed lanes, for the publication real-daemon
// tests; no release build carries this tag. A batch list of a lane whose
// source name starts with "slowlist-", and a fetch by CID of a lane whose
// source name starts with "slowfetch-", is a store read that takes 10
// minutes, or until the read's context ends (the follower resets the stream,
// or the read's bound). Each logs when it begins.
func init() {
	followReadFault = func(ctx context.Context, read, sourceName string) error {
		if !strings.HasPrefix(sourceName, "slow"+read+"-") {
			return nil
		}
		log.Infof("follow fault: the %s read of %s began; it ends in 10 minutes or with its context", read, sourceName)
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
