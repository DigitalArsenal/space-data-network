package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// GET /api/v1/pubsub/messages answers the newest `limit` records; when more
// are stored its QRP page says so (TRUNCATED, status partial) instead of
// presenting a short history as complete (stack #848; #651 review).
func TestCoreAPI_PubSubMessages_ReportsTruncation(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := newTestCoreAPIMux(h)
	for i, day := range []string{"2026-09-01", "2026-09-02", "2026-09-03"} {
		storeDataAPITestOMM(t, h.store, uint32(42000+i), "R651", day)
	}
	for _, tc := range []struct {
		limit     string
		records   int
		truncated bool
	}{
		{"2", 2, true},
		{"3", 3, false},
		{"50", 3, false},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/pubsub/messages?schema=OMM.fbs&limit="+tc.limit, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("limit %s: status %d: %s", tc.limit, rec.Code, rec.Body.String())
		}
		frames, err := syncwire.SplitFrames(rec.Body.Bytes())
		if err != nil || len(frames) == 0 {
			t.Fatalf("limit %s: frames: %v", tc.limit, err)
		}
		q, err := syncwire.ParseQRP(frames[0])
		if err != nil {
			t.Fatal(err)
		}
		wantStatus := QRPStatusOk
		if tc.truncated {
			wantStatus = QRPStatusPartial
		}
		if int(q.RECORD_COUNT()) != tc.records || len(frames)-1 != tc.records || q.TRUNCATED() != tc.truncated ||
			q.PARTIAL() != tc.truncated || int8(q.STATUS()) != wantStatus {
			t.Fatalf("limit %s: records=%d frames=%d truncated=%v partial=%v status=%d; want %d records, truncated=%v",
				tc.limit, q.RECORD_COUNT(), len(frames)-1, q.TRUNCATED(), q.PARTIAL(), q.STATUS(), tc.records, tc.truncated)
		}
	}
}
