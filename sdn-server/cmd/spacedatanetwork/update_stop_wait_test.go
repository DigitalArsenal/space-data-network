package main

import (
	"math"
	"testing"
	"time"
)

// TestUpdateHelperPublicationStopWait: what the daemon's waitBeforeStopSeconds adds to
// the helper's graceful-stop grace. No real-daemon run reaches a value other
// than the daemon's own 1800, so the arithmetic is checked here.
func TestUpdateHelperPublicationStopWait(t *testing.T) {
	for _, c := range []struct {
		seconds int64
		wait    time.Duration
		clamped bool
	}{
		{0, 0, false}, // missing (omitempty) or zero
		{-1, 0, false},
		{math.MinInt64, 0, false},
		{1, time.Second, false},
		{1800, 30 * time.Minute, false}, // the daemon's publication wait
		{3600, time.Hour, false},
		{3601, time.Hour, true},
		{1_000_000_000, time.Hour, true}, // about 31 years
		{9_300_000_000, time.Hour, true}, // overflows a Duration in seconds
		{math.MaxInt64, time.Hour, true},
	} {
		wait, clamped := publicationStopWait(c.seconds)
		if wait != c.wait || clamped != c.clamped {
			t.Errorf("publicationStopWait(%d) = %s, %t; want %s, %t", c.seconds, wait, clamped, c.wait, c.clamped)
		}
	}
}
