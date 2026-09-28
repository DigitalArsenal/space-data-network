package flowrt

import (
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/plugins"
)

var _ plugins.CronFirstRunDelayer = (*ServiceFlow)(nil)

// The scheduler asks a service flow for its first-run phase with the interval
// it resolved; the flow answers through the node's phase function and keeps
// that interval as the trigger's real cadence.
func TestServiceFlowSchedulePhaseDelegatesAndRecordsTheResolvedInterval(t *testing.T) {
	sf := &ServiceFlow{triggers: []serviceTrigger{{TriggerID: "timer-sigmf-captures", IntervalMs: 604800000}}}

	// Before anything is scheduled: the trigger's own interval.
	if got := sf.ScheduledInterval("timer-sigmf-captures"); got != 7*24*time.Hour {
		t.Fatalf("ScheduledInterval before scheduling = %s, want 168h", got)
	}
	// No phase installed: the plain ticker.
	if got := sf.CronFirstRunDelay("timer-sigmf-captures", 7*24*time.Hour); got != 0 {
		t.Fatalf("CronFirstRunDelay without a phase = %s, want 0", got)
	}

	var gotTrigger string
	var gotInterval time.Duration
	sf.SetSchedulePhase(func(triggerID string, interval time.Duration) time.Duration {
		gotTrigger, gotInterval = triggerID, interval
		return 42 * time.Hour
	})
	// A dashboard schedule of two weeks outranks the bundle default.
	if got := sf.CronFirstRunDelay("timer-sigmf-captures", 14*24*time.Hour); got != 42*time.Hour {
		t.Fatalf("CronFirstRunDelay = %s, want the phase function's 42h", got)
	}
	if gotTrigger != "timer-sigmf-captures" || gotInterval != 14*24*time.Hour {
		t.Fatalf("phase asked for %q at %s, want timer-sigmf-captures at 336h", gotTrigger, gotInterval)
	}
	if got := sf.ScheduledInterval("timer-sigmf-captures"); got != 14*24*time.Hour {
		t.Fatalf("ScheduledInterval after scheduling = %s, want the resolved 336h", got)
	}
	if got := sf.ScheduledInterval("no-such-trigger"); got != 0 {
		t.Fatalf("ScheduledInterval of an unknown trigger = %s, want 0", got)
	}
}
