package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
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
	if row["visibility"] != "public" ||
		row["subscribed"] != false ||
		row["grantState"] != "not-required" ||
		row["encryptionState"] != "none" {
		t.Fatalf("unexpected public channel list state: %#v", row)
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
	if body["pnmVerified"] != false ||
		body["visibility"] != "public" ||
		body["subscribed"] != false ||
		body["grantState"] != "not-required" ||
		body["encryptionState"] != "none" {
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

func TestChannelHandlerPublicSubscribeUpdatesMonitor(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	subscribeReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/subscribe", nil)
	subscribeRec := httptest.NewRecorder()
	mux.ServeHTTP(subscribeRec, subscribeReq)

	if subscribeRec.Code != http.StatusOK {
		t.Fatalf("subscribe status = %d body=%s", subscribeRec.Code, subscribeRec.Body.String())
	}
	subscribeBody := decodeChannelJSON(t, subscribeRec.Body.String())
	if subscribeBody["channelId"] != "spaceaware-OMM" ||
		subscribeBody["subscribed"] != true ||
		subscribeBody["visibility"] != "public" ||
		subscribeBody["grantState"] != "not-required" ||
		subscribeBody["encryptionState"] != "none" {
		t.Fatalf("unexpected subscribe response: %#v", subscribeBody)
	}

	monitorReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/monitor", nil)
	monitorRec := httptest.NewRecorder()
	mux.ServeHTTP(monitorRec, monitorReq)

	if monitorRec.Code != http.StatusOK {
		t.Fatalf("monitor status = %d body=%s", monitorRec.Code, monitorRec.Body.String())
	}
	monitorBody := decodeChannelJSON(t, monitorRec.Body.String())
	if monitorBody["subscribed"] != true ||
		monitorBody["visibility"] != "public" ||
		monitorBody["grantState"] != "not-required" ||
		monitorBody["encryptionState"] != "none" {
		t.Fatalf("unexpected subscribed monitor response: %#v", monitorBody)
	}

	unsubscribeReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/unsubscribe", nil)
	unsubscribeRec := httptest.NewRecorder()
	mux.ServeHTTP(unsubscribeRec, unsubscribeReq)

	if unsubscribeRec.Code != http.StatusOK {
		t.Fatalf("unsubscribe status = %d body=%s", unsubscribeRec.Code, unsubscribeRec.Body.String())
	}
	unsubscribeBody := decodeChannelJSON(t, unsubscribeRec.Body.String())
	if unsubscribeBody["subscribed"] != false ||
		unsubscribeBody["visibility"] != "public" ||
		unsubscribeBody["grantState"] != "not-required" {
		t.Fatalf("unexpected unsubscribe response: %#v", unsubscribeBody)
	}
}

func TestChannelHandlerIssuesPrivateGrantAndAuthorizesBoundaries(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	grantReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/channels/spaceaware-OMM/grants",
		strings.NewReader(`{"to":"peer-alpha","scopes":["subscribe","stream_open"]}`),
	)
	grantRec := httptest.NewRecorder()
	mux.ServeHTTP(grantRec, grantReq)

	if grantRec.Code != http.StatusCreated {
		t.Fatalf("grant status = %d body=%s", grantRec.Code, grantRec.Body.String())
	}
	grantBody := decodeChannelJSON(t, grantRec.Body.String())
	grantID, ok := grantBody["grantId"].(string)
	if !ok || grantID == "" {
		t.Fatalf("grant response missing grantId: %#v", grantBody)
	}
	if grantBody["channelId"] != "spaceaware-OMM" ||
		grantBody["subject"] != "peer-alpha" ||
		grantBody["grantState"] != "verified" {
		t.Fatalf("unexpected grant response: %#v", grantBody)
	}

	subscribeReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/channels/spaceaware-OMM/subscribe?visibility=private&subject=peer-alpha&grantId="+grantID,
		nil,
	)
	subscribeRec := httptest.NewRecorder()
	mux.ServeHTTP(subscribeRec, subscribeReq)

	if subscribeRec.Code != http.StatusOK {
		t.Fatalf("private subscribe status = %d body=%s", subscribeRec.Code, subscribeRec.Body.String())
	}
	subscribeBody := decodeChannelJSON(t, subscribeRec.Body.String())
	if subscribeBody["grantState"] != "verified" {
		t.Fatalf("private subscribe did not verify grant: %#v", subscribeBody)
	}

	streamReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/channels/spaceaware-OMM/stream?subject=peer-alpha&grantId="+grantID,
		nil,
	)
	streamRec := httptest.NewRecorder()
	mux.ServeHTTP(streamRec, streamReq)

	if streamRec.Code != http.StatusOK {
		t.Fatalf("private stream status = %d body=%s", streamRec.Code, streamRec.Body.String())
	}
	streamBody := decodeChannelJSON(t, streamRec.Body.String())
	if streamBody["grantState"] != "verified" {
		t.Fatalf("private stream did not verify grant: %#v", streamBody)
	}
}

func TestChannelHandlerPublicPublishRejectsUnverifiedPNM(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish", bytes.NewReader(buildAPIUnsignedPNM(t)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("publish status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "SIGNATURE_TYPE") {
		t.Fatalf("publish body did not report PNM signature rejection: %s", rec.Body.String())
	}
}

func TestChannelHandlerPublicPublishUpdatesVerifiedMonitor(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish", bytes.NewReader(buildAPISignedPNM(t, "bafyverifiedhead", "DPM")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("publish status = %d body=%s", rec.Code, rec.Body.String())
	}
	publishBody := decodeChannelJSON(t, rec.Body.String())
	if publishBody["pnmVerified"] != true || publishBody["pnmCid"] != "bafyverifiedhead" {
		t.Fatalf("unexpected publish response: %#v", publishBody)
	}

	monitorReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/monitor", nil)
	monitorRec := httptest.NewRecorder()
	mux.ServeHTTP(monitorRec, monitorReq)

	if monitorRec.Code != http.StatusOK {
		t.Fatalf("monitor status = %d body=%s", monitorRec.Code, monitorRec.Body.String())
	}
	monitorBody := decodeChannelJSON(t, monitorRec.Body.String())
	if monitorBody["pnmVerified"] != true ||
		monitorBody["channelHead"] != "bafyverifiedhead" ||
		monitorBody["lastVerifiedUpdate"] == "" {
		t.Fatalf("monitor did not reflect verified PNM: %#v", monitorBody)
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
		{http.MethodGet, "/api/v1/channels?visibility=private"},
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/subscribe?visibility=private"},
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/unsubscribe?visibility=private"},
		{http.MethodGet, "/api/v1/channels/spaceaware-OMM/stream"},
		{http.MethodGet, "/api/v1/channels/spaceaware-OMM/bytes"},
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/key-unwrap"},
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/shard-import"},
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/module-feed"},
		{http.MethodGet, "/api/v1/channels/spaceaware-OMM/cache"},
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?visibility=private"},
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/grants?visibility=private"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want %d body=%s", tc.method, tc.path, rec.Code, http.StatusForbidden, rec.Body.String())
		}
	}
}

func buildAPIUnsignedPNM(t *testing.T) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(256)
	cidOffset := builder.CreateString("bafymanifest")
	fileIDOffset := builder.CreateString("DPM")
	timestampOffset := builder.CreateString(time.Now().UTC().Format(time.RFC3339))

	PNM.PNMStart(builder)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)
	return append([]byte(nil), builder.FinishedBytes()...)
}

func buildAPISignedPNM(t *testing.T, cid string, fileID string) []byte {
	t.Helper()

	signature := strings.Repeat("a", 128)
	builder := flatbuffers.NewBuilder(256)
	cidOffset := builder.CreateString(cid)
	fileIDOffset := builder.CreateString(fileID)
	timestampOffset := builder.CreateString(time.Now().UTC().Format(time.RFC3339))
	signatureOffset := builder.CreateString(signature)
	signatureTypeOffset := builder.CreateString("Ed25519")

	PNM.PNMStart(builder)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	PNM.PNMAddSIGNATURE(builder, signatureOffset)
	PNM.PNMAddSIGNATURE_TYPE(builder, signatureTypeOffset)
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)
	return append([]byte(nil), builder.FinishedBytes()...)
}

func decodeChannelJSON(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode JSON failed: %v\n%s", err, body)
	}
	return payload
}
