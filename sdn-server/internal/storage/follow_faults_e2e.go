//go:build sdn_e2e_follow_faults

package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A build with faults in its followed lanes, for the publication real-daemon
// tests; no release build carries this tag.
//   - A batch list of a lane whose source name starts with "slowlist-", and
//     a fetch by CID of a lane whose source name starts with "slowfetch-",
//     is a store read that takes 10 minutes, or until the read's context
//     ends (the follower resets the stream, or the read's bound). Each logs
//     when it begins.
//   - A list page after the first of a lane whose source name starts with
//     "growlist-", read while the gate "<source>-hold" is open, waits until
//     the gate "<source>-go" is open (at most 10 minutes, or until the
//     read's context ends), so the test can store more of the batch while
//     the follower lists it. It logs when it begins to wait. A gate is open
//     when a file of its name exists in the folder SDN_E2E_PUBLICATION_GATES
//     names.
func init() {
	followReadFault = func(ctx context.Context, read, sourceName string) error {
		if read == "listmore" {
			return growListGate(ctx, sourceName)
		}
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

func growListGate(ctx context.Context, sourceName string) error {
	dir := os.Getenv("SDN_E2E_PUBLICATION_GATES")
	if !strings.HasPrefix(sourceName, "growlist-") || dir == "" {
		return nil
	}
	open := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, sourceName+"-"+name))
		return err == nil
	}
	if !open("hold") {
		return nil
	}
	log.Infof("follow fault: a list page of %s waits for its gate", sourceName)
	deadline := time.Now().Add(10 * time.Minute)
	for !open("go") && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}
