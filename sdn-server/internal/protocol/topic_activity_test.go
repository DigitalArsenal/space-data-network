package protocol

import (
	"testing"
	"time"
)

func TestTopicActivityKeepsOnlyTheLastMinutePerTopic(t *testing.T) {
	a := NewTopicActivity()
	now := time.Unix(1_780_000_000, 0)
	a.Observe("/spacedatanetwork/sds/OMM.fbs", now.Add(-2*time.Minute)) // outside the window
	a.Observe("/spacedatanetwork/sds/OMM.fbs", now.Add(-30*time.Second))
	a.Observe("/spacedatanetwork/sds/OMM.fbs", now.Add(-5*time.Second))
	a.Observe("/spacedatanetwork/sds/SPW.fbs", now.Add(-90*time.Second)) // outside
	a.Observe("", now)                                                   // ignored
	snap := a.Snapshot(now)
	if len(snap) != 1 || snap[0].Topic != "/spacedatanetwork/sds/OMM.fbs" || len(snap[0].MessageTimestamps) != 2 {
		t.Fatalf("snapshot = %+v, want one OMM topic with 2 observations inside the minute", snap)
	}
	if snap[0].MessageTimestamps[0] != now.Add(-30*time.Second).UnixMilli() {
		t.Fatalf("timestamps not ascending / not trimmed: %v", snap[0].MessageTimestamps)
	}
	// The stale SPW topic is forgotten, not reported as zero.
	if len(a.Snapshot(now)) != 1 {
		t.Fatal("stale topics must be dropped")
	}
}
