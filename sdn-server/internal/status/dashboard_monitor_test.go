package status

import (
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/status/nst"
)

func TestDashboardMonitorWindowsStallAndRecover(t *testing.T) {
	monitor := NewDashboardMonitor(DefaultDashboardWindow)
	base := time.Unix(1_756_000_000, 0)
	source := DashboardSourceRow{
		Schema:        "OMM",
		ProviderID:    "observatory",
		SourceName:    "catalog",
		BatchID:       "daily",
		RecordCount:   4,
		FirstIngestAt: base.Add(-8 * time.Minute).Unix(),
		LastIngestAt:  base.Add(-1 * time.Minute).Unix(),
		UpdatedAt:     base.Add(-1 * time.Minute).Unix(),
		IngestTimestamps: []int64{
			base.Add(-8 * time.Minute).Unix(),
			base.Add(-4 * time.Minute).Unix(),
			base.Add(-2 * time.Minute).Unix(),
			base.Add(-1 * time.Minute).Unix(),
		},
	}

	firstFrame := monitor.Build(DashboardStatsInput{
		Sources: []DashboardSourceRow{source},
		Topics: []DashboardTopicRow{{
			Topic:             "/spacedatanetwork/OMM.fbs",
			Subscribed:        true,
			MessageTimestamps: []int64{base.Add(-30 * time.Second).Unix(), base.Add(-10 * time.Second).Unix()},
		}},
		Now: base,
	})
	first := nst.GetSizePrefixedRootAsDashboardStatsSet(firstFrame, 0)
	var firstSource nst.DashboardSourceStat
	if first.SourcesLength() != 1 || !first.SOURCES(&firstSource, 0) {
		t.Fatal("initial source row missing")
	}
	if got := firstSource.WINDOW_RECORDS(); got != 3 {
		t.Fatalf("WINDOW_RECORDS = %d, want 3 exact injected timestamps", got)
	}
	if got := firstSource.PRIOR_WINDOW_RECORDS(); got != 1 {
		t.Fatalf("PRIOR_WINDOW_RECORDS = %d, want 1 exact injected timestamp", got)
	}
	if got := firstSource.WINDOW_MS(); got != int64(DefaultDashboardWindow/time.Millisecond) {
		t.Fatalf("WINDOW_MS = %d", got)
	}
	if first.EventsLength() != 0 {
		t.Fatalf("initial events = %d, want 0", first.EventsLength())
	}
	var topic nst.DashboardTopicStat
	if first.TopicsLength() != 1 || !first.TOPICS(&topic, 0) {
		t.Fatal("topic row missing")
	}
	if topic.RATE_PER_MIN() != 2 || topic.LAST_SEEN_AT() != base.Add(-10*time.Second).Unix() || !topic.SUBSCRIBED() {
		t.Fatalf("topic = rate %v last %d subscribed %v", topic.RATE_PER_MIN(), topic.LAST_SEEN_AT(), topic.SUBSCRIBED())
	}

	// Median inter-arrival is two minutes, so 3x median is six minutes and
	// the ten-minute minimum governs. Eleven quiet minutes after the most
	// recent arrival emits one Stall; another idle build emits no duplicate.
	stallAt := base.Add(10 * time.Minute)
	source.IngestTimestamps = nil
	stallFrame := monitor.Build(DashboardStatsInput{Sources: []DashboardSourceRow{source}, Now: stallAt})
	stalled := nst.GetSizePrefixedRootAsDashboardStatsSet(stallFrame, 0)
	if stalled.EventsLength() != 1 {
		t.Fatalf("events after silence = %d, want one Stall", stalled.EventsLength())
	}
	var event nst.DashboardIngestEvent
	stalled.EVENTS(&event, 0)
	if event.KIND() != nst.DashboardIngestEventKindStall {
		t.Fatalf("first event = %s, want Stall", event.KIND())
	}

	idleFrame := monitor.Build(DashboardStatsInput{Sources: []DashboardSourceRow{source}, Now: stallAt.Add(time.Minute)})
	idle := nst.GetSizePrefixedRootAsDashboardStatsSet(idleFrame, 0)
	if idle.EventsLength() != 1 {
		t.Fatalf("repeated idle events = %d, want the original Stall only", idle.EventsLength())
	}

	// The next positive count delta records exactly one arrival and emits one
	// Recover. The event history contains one Stall followed by one Recover.
	recoverAt := stallAt.Add(2 * time.Minute)
	source.RecordCount = 5
	source.LastIngestAt = recoverAt.Unix()
	source.UpdatedAt = recoverAt.Unix()
	source.IngestTimestamps = []int64{recoverAt.Unix()}
	recoverFrame := monitor.Build(DashboardStatsInput{Sources: []DashboardSourceRow{source}, Now: recoverAt})
	recovered := nst.GetSizePrefixedRootAsDashboardStatsSet(recoverFrame, 0)
	if recovered.EventsLength() != 2 {
		t.Fatalf("events after recovery = %d, want Stall then Recover", recovered.EventsLength())
	}
	var firstEvent, secondEvent nst.DashboardIngestEvent
	recovered.EVENTS(&firstEvent, 0)
	recovered.EVENTS(&secondEvent, 1)
	if firstEvent.KIND() != nst.DashboardIngestEventKindStall || secondEvent.KIND() != nst.DashboardIngestEventKindRecover {
		t.Fatalf("event order = %s, %s", firstEvent.KIND(), secondEvent.KIND())
	}
	var recoveredSource nst.DashboardSourceStat
	recovered.SOURCES(&recoveredSource, 0)
	if recoveredSource.WINDOW_RECORDS() != 1 || recoveredSource.PRIOR_WINDOW_RECORDS() != 0 {
		t.Fatalf("recovery windows = current %d prior %d, want 1/0",
			recoveredSource.WINDOW_RECORDS(), recoveredSource.PRIOR_WINDOW_RECORDS())
	}
}
