package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
)

// `spacedatanetwork status` reads the daemon's own operator health surface,
// served here by the real mountHealthRoutes — not a hand-written JSON route.
// STACK ISSUE 670: status used to decode /api/v1/data/health, which answers a
// $QRP FlatBuffer since 47f043132, so every node printed unhealthy forever. A
// fixture that served fake JSON on that path is how the tests missed it.
func TestWriteDaemonStatusReadsTheRealHealthSurface(t *testing.T) {
	healthy := func() healthDeps {
		return healthDeps{engineReady: func() bool { return true }, peerCount: func() int { return 1 }}
	}
	degraded := healthy()
	degraded.alerts = ops.NewRegistry()
	degraded.alerts.Raise(ops.KindUpdateFailed, "e2e", ops.SeverityError, "an alert")

	for _, tc := range []struct {
		name string
		deps healthDeps
		want []string
	}{
		{name: "ok", deps: healthy(), want: []string{"daemon_status=running", "data_health=healthy"}},
		{name: "degraded", deps: degraded, want: []string{"daemon_status=running", "data_health=degraded", "alerts_error=1", "alerts_warning=0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mountHealthRoutes(mux, tc.deps)
			// The data route answers what it answers in production: not JSON.
			mux.HandleFunc("/api/v1/data/health", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
				_, _ = w.Write([]byte{0x10, 0, 0, 0, '$', 'Q', 'R', 'P'})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			var out bytes.Buffer
			if err := writeDaemonStatus(context.Background(), &out, srv.URL); err != nil {
				t.Fatalf("writeDaemonStatus: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want+"\n") {
					t.Fatalf("status output missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}
