package flowrt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/plugins"
)

func TestInvokeCronForceBypassesManualAndRetrievalGates(t *testing.T) {
	for _, manual := range []bool{false, true} {
		for _, input := range []string{`{"force":true}`, `{"force":false}`, `{}`, `{"force":"true"}`, `broken`, ``} {
			name := "scheduled/" + input
			ctx := plugins.WithScheduledRun(context.Background())
			if manual {
				name = "manual/" + input
				ctx = plugins.WithManualRun(context.Background())
			}
			t.Run(name, func(t *testing.T) {
				// A closed flow reports "closed" only if invocation reaches the trigger.
				sf := &ServiceFlow{programID: "force-test", triggers: []serviceTrigger{{TriggerID: "tick"}}}
				manualCalls, retrievalCalls := 0, 0
				sf.SetManualRunGate(func() (func(error), time.Time) {
					manualCalls++
					return nil, time.Now().Add(time.Hour)
				})
				sf.SetRetrievalGate(func() (bool, string) {
					retrievalCalls++
					return false, "failure backoff"
				})
				sf.SetRetrievalOutcome(func(error) { t.Fatal("refused or forced run booked an outcome") })
				out, err := sf.InvokeCron(ctx, "tick", []byte(input))
				if input == `{"force":true}` {
					if err == nil || !strings.Contains(err.Error(), "closed") || manualCalls != 0 || retrievalCalls != 0 {
						t.Fatalf("forced run: err=%v manual=%d retrieval=%d", err, manualCalls, retrievalCalls)
					}
				} else if manual {
					var deferred *plugins.DeferredError
					if !errors.As(err, &deferred) || manualCalls != 1 || retrievalCalls != 0 {
						t.Fatalf("manual run: err=%v manual=%d retrieval=%d", err, manualCalls, retrievalCalls)
					}
				} else if err != nil || retrievalCalls != 1 || manualCalls != 0 || !strings.Contains(string(out), `"skipped":true`) {
					t.Fatalf("scheduled run: out=%s err=%v manual=%d retrieval=%d", out, err, manualCalls, retrievalCalls)
				}
			})
		}
	}
}
