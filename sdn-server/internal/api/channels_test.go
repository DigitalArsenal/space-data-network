package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChannelHandlerListsStandardCodesOnly(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels?standardCode=OMM", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeChannelJSON(t, rec.Body.String())
	results := body["results"].([]interface{})
	if len(results) != 1 {
		t.Fatalf("result count = %d, want 1", len(results))
	}
	row := results[0].(map[string]interface{})
	if row["standardCode"] != "OMM" || row["topic"] != "/spacedatanetwork/channels/OMM" {
		t.Fatalf("unexpected channel row: %#v", row)
	}
	if strings.Contains(rec.Body.String(), string([]byte{'.', 'f', 'b', 's'})) {
		t.Fatalf("channel list exposed internal schema suffix: %s", rec.Body.String())
	}
}

func TestChannelHandlerShowsHyphenatedSourceChannel(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels/celestrak-eth-CDM", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeChannelJSON(t, rec.Body.String())
	if body["channelId"] != "celestrak-eth-CDM" || body["sourceId"] != "celestrak-eth" || body["standardCode"] != "CDM" {
		t.Fatalf("unexpected channel response: %#v", body)
	}
	if body["pnmVerified"] != false || body["visibility"] != "unknown" {
		t.Fatalf("unexpected verification fields: %#v", body)
	}
}

func TestChannelHandlerMonitorReportsRequiredFields(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/monitor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeChannelJSON(t, rec.Body.String())
	for _, key := range []string{
		"channelHead",
		"pnmVerified",
		"providerPeer",
		"localRows",
		"remoteRows",
		"syncedRows",
		"missingRows",
		"pinnedRows",
		"syncedBytes",
		"throughputBytesPerSecond",
		"wireSpeedUtilization",
		"grantState",
		"encryptionState",
		"lastVerifiedUpdate",
	} {
		if _, ok := body[key]; !ok {
			t.Fatalf("monitor response missing %q: %#v", key, body)
		}
	}
}

func TestChannelHandlerPrivateRoutesFailClosed(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/subscribe"},
		{http.MethodGet, "/api/v1/channels/spaceaware-OMM/stream"},
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/grants"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want %d body=%s", tc.method, tc.path, rec.Code, http.StatusForbidden, rec.Body.String())
		}
	}
}

func decodeChannelJSON(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode JSON failed: %v\n%s", err, body)
	}
	return payload
}
