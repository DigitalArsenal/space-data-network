//go:build sdn_e2e_follow_faults

package node

import (
	"strings"
	"time"
)

// A build with faults in its followed lanes, for the publication real-daemon
// tests; no release build carries this tag:
//   - the store of a lane whose source name starts with "panic-" panics after
//     the records are verified and before they are stored (the running
//     guard's reservation is released all the same);
//   - a lane whose source name starts with "late-" opens its first followed
//     stream 40 s after it starts (a producer that answers late after a
//     restart), past the first stored-$PNM catch-up 15 s after the start;
//   - a lane whose source name starts with "late270-" opens its first
//     followed stream 270 s after it starts;
//   - a lane whose source name starts with "never-" opens no followed
//     stream for 24 hours (a producer whose stream never connects).
func init() {
	followStoreFault = func(sourceName string) {
		if strings.HasPrefix(sourceName, "panic-") {
			panic("test build: the store of lane " + sourceName + " panics")
		}
	}
	followStartDelay = func(sourceName string) time.Duration {
		switch {
		case strings.HasPrefix(sourceName, "late-"):
			return 40 * time.Second
		case strings.HasPrefix(sourceName, "late270-"):
			return 270 * time.Second
		case strings.HasPrefix(sourceName, "never-"):
			return 24 * time.Hour
		}
		return 0
	}
}
