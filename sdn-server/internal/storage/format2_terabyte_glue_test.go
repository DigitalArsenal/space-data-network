package storage

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/metrics"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

// B1 (terabyte audit): a label wait that reaches its deadline does not fail
// the write whose records are durable. It is counted on /metrics by schema
// and the write succeeds; every other error is still the write's.
func TestLabelWaitDeadlineIsADurableWrite(t *testing.T) {
	lw := &format2.LabelWaitError{Schema: "B1GLUE.fbs", PID: 129, Partitions: 3, PseqHi: 9, LabeledThrough: 4, Known: true}
	if err := labelWaitOutcome(lw, func() uint64 { return 3 }); err != nil {
		t.Fatalf("label wait deadline: %v, want the write to succeed", err)
	}
	if err := labelWaitOutcome(nil, func() uint64 { return 3 }); err != nil {
		t.Fatalf("labeled: %v", err)
	}
	for _, want := range []error{format2.ErrStopped, errors.New("engine status -5")} {
		if err := labelWaitOutcome(want, func() uint64 { return 3 }); err != want {
			t.Fatalf("labelWaitOutcome(%v) = %v, want it returned", want, err)
		}
	}
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if line := `sdn_storage_label_wait_timeouts_total{schema="B1GLUE.fbs"} 3`; !strings.Contains(rec.Body.String(), line) {
		t.Fatalf("/metrics lacks %q", line)
	}
}
