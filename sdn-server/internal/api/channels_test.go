package api

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
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
		"pinnedCount",
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
		"/api/v1/channels/spaceaware-OMM/stream?visibility=private&subject=peer-alpha&grantId="+grantID,
		nil,
	)
	streamRec := httptest.NewRecorder()
	mux.ServeHTTP(streamRec, streamReq)

	if streamRec.Code != http.StatusNotFound {
		t.Fatalf("private stream status = %d, want no verified stream after grant body=%s", streamRec.Code, streamRec.Body.String())
	}
	if strings.Contains(streamRec.Body.String(), "verified channel grant required") {
		t.Fatalf("private stream did not pass grant boundary: %s", streamRec.Body.String())
	}
}

func TestChannelHandlerPublicPublishRejectsUnverifiedPNM(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?"+signing.providerKeyQuery(), bytes.NewReader(buildAPIUnsignedPNM(t)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("publish status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "SIGNATURE_TYPE") {
		t.Fatalf("publish body did not report PNM signature rejection: %s", rec.Body.String())
	}
}

func TestChannelHandlerPublicPublishRequiresProviderPublicKey(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish", bytes.NewReader(buildAPISignedPNM(t, signing.privateKey, "bafyverifiedhead", "DPM")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("publish status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "provider public key is required") {
		t.Fatalf("publish body did not require provider key: %s", rec.Body.String())
	}
}

func TestChannelHandlerPublicPublishUpdatesVerifiedMonitor(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?"+signing.providerKeyQuery(), bytes.NewReader(buildAPISignedPNM(t, signing.privateKey, "bafyverifiedhead", "DPM")))
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

func TestChannelHandlerReturnsVerifiedPNMBytes(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	pnmBytes := buildAPISignedPNM(t, signing.privateKey, "bafyverifiedhead", "DPM")

	publishReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?"+signing.providerKeyQuery(), bytes.NewReader(pnmBytes))
	publishRec := httptest.NewRecorder()
	mux.ServeHTTP(publishRec, publishReq)
	if publishRec.Code != http.StatusAccepted {
		t.Fatalf("publish status = %d body=%s", publishRec.Code, publishRec.Body.String())
	}

	pnmReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/pnm", nil)
	pnmRec := httptest.NewRecorder()
	mux.ServeHTTP(pnmRec, pnmReq)
	if pnmRec.Code != http.StatusOK {
		t.Fatalf("PNM status = %d body=%s", pnmRec.Code, pnmRec.Body.String())
	}
	if got := pnmRec.Header().Get("Content-Type"); got != "application/vnd.sdn.pnm" {
		t.Fatalf("PNM Content-Type = %q", got)
	}
	if !bytes.Equal(pnmRec.Body.Bytes(), pnmBytes) {
		t.Fatal("PNM endpoint did not return the verified PNM bytes")
	}
}

func TestChannelHandlerPublicStreamPublishRequiresVerifiedPNM(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	streamBytes := nativeAPIFrame("OMM1", []byte{1, 2, 3})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?stream=1", bytes.NewReader(streamBytes))
	req.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("stream publish status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "verified PNM required") {
		t.Fatalf("stream publish body did not require verified PNM: %s", rec.Body.String())
	}
}

func TestChannelHandlerPublicStreamPublishRequiresVerifiedDPM(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)

	pnmReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?"+signing.providerKeyQuery(), bytes.NewReader(buildAPISignedPNM(t, signing.privateKey, "bafystreamhead", "DPM")))
	pnmRec := httptest.NewRecorder()
	mux.ServeHTTP(pnmRec, pnmReq)
	if pnmRec.Code != http.StatusAccepted {
		t.Fatalf("PNM publish status = %d body=%s", pnmRec.Code, pnmRec.Body.String())
	}

	streamBytes := nativeAPIFrame("OMM1", []byte{1, 2, 3})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?stream=1", bytes.NewReader(streamBytes))
	req.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("stream publish status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "verified DPM required") {
		t.Fatalf("stream publish body did not require verified DPM: %s", rec.Body.String())
	}
}

func TestChannelHandlerRejectsDPMMismatchedToPNM(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPM(t, signing.privateKey, "OTHER")

	pnmReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?"+signing.providerKeyQuery(), bytes.NewReader(buildAPISignedPNM(t, signing.privateKey, manifest.CID, "DPM")))
	pnmRec := httptest.NewRecorder()
	mux.ServeHTTP(pnmRec, pnmReq)
	if pnmRec.Code != http.StatusAccepted {
		t.Fatalf("PNM publish status = %d body=%s", pnmRec.Code, pnmRec.Body.String())
	}

	dpmReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?manifest=1&"+signing.providerKeyQuery(), bytes.NewReader(manifest.Bytes))
	dpmReq.Header.Set("Content-Type", "application/vnd.sdn.dpm")
	dpmRec := httptest.NewRecorder()
	mux.ServeHTTP(dpmRec, dpmReq)
	if dpmRec.Code != http.StatusBadRequest {
		t.Fatalf("DPM publish status = %d, want %d body=%s", dpmRec.Code, http.StatusBadRequest, dpmRec.Body.String())
	}
	if !strings.Contains(dpmRec.Body.String(), "does not match PNM") {
		t.Fatalf("DPM publish body did not report FILE_ID mismatch: %s", dpmRec.Body.String())
	}
}

func TestChannelHandlerRejectsDPMFromDifferentProviderKey(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	otherSigning := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPM(t, signing.privateKey, "DPM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")

	dpmReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?manifest=1&"+otherSigning.providerKeyQuery(), bytes.NewReader(manifest.Bytes))
	dpmReq.Header.Set("Content-Type", "application/vnd.sdn.dpm")
	dpmRec := httptest.NewRecorder()
	mux.ServeHTTP(dpmRec, dpmReq)
	if dpmRec.Code != http.StatusForbidden {
		t.Fatalf("DPM publish status = %d, want %d body=%s", dpmRec.Code, http.StatusForbidden, dpmRec.Body.String())
	}
	if !strings.Contains(dpmRec.Body.String(), "does not match verified PNM provider") {
		t.Fatalf("DPM publish body did not report provider mismatch: %s", dpmRec.Body.String())
	}
}

func TestChannelHandlerPublishesAndOpensNativeFlatBufferStream(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPM(t, signing.privateKey, "DPM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	streamBytes := bytes.Join([][]byte{
		nativeAPIFrame("OMM1", []byte{1, 2, 3}),
		nativeAPIFrame("OMM1", []byte{4, 5, 6, 7}),
	}, nil)
	streamReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?stream=1", bytes.NewReader(streamBytes))
	streamReq.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	streamRec := httptest.NewRecorder()
	mux.ServeHTTP(streamRec, streamReq)
	if streamRec.Code != http.StatusAccepted {
		t.Fatalf("stream publish status = %d body=%s", streamRec.Code, streamRec.Body.String())
	}
	publishBody := decodeChannelJSON(t, streamRec.Body.String())
	if publishBody["pnmVerified"] != true ||
		publishBody["streamBytes"] != float64(len(streamBytes)) ||
		publishBody["streamFrames"] != float64(2) {
		t.Fatalf("unexpected stream publish response: %#v", publishBody)
	}

	openReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/stream", nil)
	openReq.Header.Set("Accept", "application/vnd.sdn.flatbuffers.stream")
	openRec := httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("stream open status = %d body=%s", openRec.Code, openRec.Body.String())
	}
	if got := openRec.Header().Get("Content-Type"); got != "application/vnd.sdn.flatbuffers.stream" {
		t.Fatalf("stream Content-Type = %q", got)
	}
	if !bytes.Equal(openRec.Body.Bytes(), streamBytes) {
		t.Fatal("stream open did not return original native FlatBuffer stream bytes")
	}
	if strings.Contains(openRec.Body.String(), "base64") || strings.Contains(openRec.Body.String(), "records") {
		t.Fatalf("stream hot path returned JSON/base64-looking data: %q", openRec.Body.String())
	}

	monitorReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/monitor", nil)
	monitorRec := httptest.NewRecorder()
	mux.ServeHTTP(monitorRec, monitorReq)
	if monitorRec.Code != http.StatusOK {
		t.Fatalf("monitor status = %d body=%s", monitorRec.Code, monitorRec.Body.String())
	}
	monitorBody := decodeChannelJSON(t, monitorRec.Body.String())
	if monitorBody["syncedBytes"] != float64(len(streamBytes)) || monitorBody["syncedRows"] != float64(2) {
		t.Fatalf("monitor did not reflect native stream import: %#v", monitorBody)
	}
	if throughput, ok := monitorBody["throughputBytesPerSecond"].(float64); !ok || throughput <= 0 {
		t.Fatalf("monitor did not report current native stream throughput: %#v", monitorBody)
	}
}

func TestChannelHandlerReportsNativeStreamWireSpeedUtilization(t *testing.T) {
	t.Setenv("SDN_TEST_LINK_GBIT", "2")

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPM(t, signing.privateKey, "DPM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	streamBytes := bytes.Join([][]byte{
		nativeAPIFrame("OMM1", []byte{1, 2, 3}),
		nativeAPIFrame("OMM1", []byte{4, 5, 6, 7}),
	}, nil)
	streamReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?stream=1", bytes.NewReader(streamBytes))
	streamReq.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	streamRec := httptest.NewRecorder()
	mux.ServeHTTP(streamRec, streamReq)
	if streamRec.Code != http.StatusAccepted {
		t.Fatalf("stream publish status = %d body=%s", streamRec.Code, streamRec.Body.String())
	}

	monitorReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/monitor", nil)
	monitorRec := httptest.NewRecorder()
	mux.ServeHTTP(monitorRec, monitorReq)
	if monitorRec.Code != http.StatusOK {
		t.Fatalf("monitor status = %d body=%s", monitorRec.Code, monitorRec.Body.String())
	}
	monitorBody := decodeChannelJSON(t, monitorRec.Body.String())
	if utilization, ok := monitorBody["wireSpeedUtilization"].(float64); !ok || utilization <= 0 {
		t.Fatalf("monitor did not report native stream wire-speed utilization: %#v", monitorBody)
	}
}

func TestChannelHandlerEnforcesWirespeedGateWhenEnabled(t *testing.T) {
	t.Setenv("SDN_WIRESPEED_TEST", "1")
	t.Setenv("SDN_TEST_LINK_GBIT", "2")

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPM(t, signing.privateKey, "DPM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	streamBytes := nativeAPIFrame("OMM1", []byte{1, 2, 3})
	streamReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?stream=1", bytes.NewReader(streamBytes))
	streamReq.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	streamRec := httptest.NewRecorder()
	mux.ServeHTTP(streamRec, streamReq)
	if streamRec.Code != http.StatusTooManyRequests {
		t.Fatalf("wirespeed-gated stream publish status = %d body=%s", streamRec.Code, streamRec.Body.String())
	}
	body := decodeChannelJSON(t, streamRec.Body.String())
	if body["wireSpeedTarget"] != 0.9 {
		t.Fatalf("wire speed target = %#v, want 0.9 body=%#v", body["wireSpeedTarget"], body)
	}
	if body["requiredBytesPerSecond"] != 225_000_000.0 {
		t.Fatalf("required bytes/sec = %#v, want 225000000 body=%#v", body["requiredBytesPerSecond"], body)
	}
	if body["targetMet"] != false {
		t.Fatalf("targetMet = %#v, want false body=%#v", body["targetMet"], body)
	}
	timings, ok := body["timingsMs"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing timingsMs: %#v", body)
	}
	for _, key := range []string{"discovery", "grantNegotiation", "pnmDpmVerification", "transfer", "decrypt", "hashVerification", "durableImport"} {
		if _, ok := timings[key]; !ok {
			t.Fatalf("timingsMs missing %q: %#v", key, timings)
		}
	}
}

func TestChannelHandlerReadsPublicNativeFlatBufferByteRange(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPM(t, signing.privateKey, "DPM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	streamBytes := bytes.Join([][]byte{
		nativeAPIFrame("OMM1", []byte{1, 2, 3}),
		nativeAPIFrame("OMM1", []byte{4, 5, 6, 7}),
	}, nil)
	streamReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?stream=1", bytes.NewReader(streamBytes))
	streamReq.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	streamRec := httptest.NewRecorder()
	mux.ServeHTTP(streamRec, streamReq)
	if streamRec.Code != http.StatusAccepted {
		t.Fatalf("stream publish status = %d body=%s", streamRec.Code, streamRec.Body.String())
	}

	rangeReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/bytes?offset=1&length=6", nil)
	rangeRec := httptest.NewRecorder()
	mux.ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent {
		t.Fatalf("byte range status = %d body=%s", rangeRec.Code, rangeRec.Body.String())
	}
	if got := rangeRec.Header().Get("Content-Type"); got != "application/vnd.sdn.flatbuffers.stream" {
		t.Fatalf("byte range Content-Type = %q", got)
	}
	if got := rangeRec.Header().Get("Content-Range"); got != "bytes 1-6/"+strconv.Itoa(len(streamBytes)) {
		t.Fatalf("byte range Content-Range = %q", got)
	}
	if !bytes.Equal(rangeRec.Body.Bytes(), streamBytes[1:7]) {
		t.Fatalf("byte range body = %v, want %v", rangeRec.Body.Bytes(), streamBytes[1:7])
	}
}

func TestChannelHandlerImportsVerifiedNativeStreamIntoFlatSQL(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	store := newChannelTestStore(t)
	mux := http.NewServeMux()
	NewChannelHandler(store).RegisterRoutes(mux)
	manifest := buildAPISignedDPM(t, signing.privateKey, "DPM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	streamBytes := bytes.Join([][]byte{
		nativeAPIFrame("OMM1", []byte{1, 2, 3}),
		nativeAPIFrame("OMM1", []byte{4, 5, 6, 7}),
	}, nil)
	streamReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?stream=1", bytes.NewReader(streamBytes))
	streamReq.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	streamRec := httptest.NewRecorder()
	mux.ServeHTTP(streamRec, streamReq)
	if streamRec.Code != http.StatusAccepted {
		t.Fatalf("stream publish status = %d body=%s", streamRec.Code, streamRec.Body.String())
	}

	schemaName, err := channels.SchemaNameFromStandardCode("OMM")
	if err != nil {
		t.Fatalf("SchemaNameFromStandardCode failed: %v", err)
	}
	count, err := store.CountRawRecords(storage.RawRecordQuery{SchemaName: schemaName})
	if err != nil {
		t.Fatalf("CountRawRecords failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("durable OMM rows = %d, want 2", count)
	}
	records, err := store.QueryRawRecords(storage.RawRecordQuery{SchemaName: schemaName, Limit: 10})
	if err != nil {
		t.Fatalf("QueryRawRecords failed: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("durable OMM records = %d, want 2", len(records))
	}
	if !bytes.Equal(records[0].Data, nativeAPIFrame("OMM1", []byte{1, 2, 3})) &&
		!bytes.Equal(records[1].Data, nativeAPIFrame("OMM1", []byte{1, 2, 3})) {
		t.Fatal("durable FlatSQL rows did not preserve native stream record bytes")
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
		{http.MethodGet, "/api/v1/channels/spaceaware-OMM/stream?visibility=private"},
		{http.MethodGet, "/api/v1/channels/spaceaware-OMM/bytes?visibility=private"},
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

func TestChannelHandlerPrivateListedCollectionFailsClosedWithoutGrant(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPMWithAccess(t, signing.privateKey, "DPM", "channel-private-key", "policy-spaceaware-OMM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels?standardCode=OMM&visibility=private-listed", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("private-listed collection without grant status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "spaceaware-OMM") || strings.Contains(rec.Body.String(), "private-listed") {
		t.Fatalf("private-listed collection leaked private metadata without grant: %s", rec.Body.String())
	}
}

func TestChannelHandlerPrivateListedCollectionReturnsGrantedVerifiedMetadata(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPMWithAccess(t, signing.privateKey, "DPM", "channel-private-key", "policy-spaceaware-OMM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	grantReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/channels/spaceaware-OMM/grants",
		strings.NewReader(`{"to":"peer-alpha","scopes":["list_private"]}`),
	)
	grantRec := httptest.NewRecorder()
	mux.ServeHTTP(grantRec, grantReq)
	if grantRec.Code != http.StatusCreated {
		t.Fatalf("grant status = %d body=%s", grantRec.Code, grantRec.Body.String())
	}
	grantID := decodeChannelJSON(t, grantRec.Body.String())["grantId"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels?standardCode=OMM&visibility=private-listed&subject=peer-alpha&grantId="+grantID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("private-listed collection with grant status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeChannelJSON(t, rec.Body.String())
	results := body["results"].([]interface{})
	if len(results) != 1 {
		t.Fatalf("private-listed result count = %d, want 1 body=%#v", len(results), body)
	}
	row := results[0].(map[string]interface{})
	for key, want := range map[string]interface{}{
		"channelId":       "spaceaware-OMM",
		"sourceId":        "spaceaware",
		"standardCode":    "OMM",
		"visibility":      "private-listed",
		"grantState":      "verified",
		"encryptionState": "encrypted",
		"pnmVerified":     true,
		"dpmVerified":     true,
	} {
		if row[key] != want {
			t.Fatalf("private-listed row[%s] = %#v, want %#v row=%#v", key, row[key], want, row)
		}
	}
	for _, forbidden := range []string{"contentKeyID", "encryptionPolicy"} {
		if _, ok := row[forbidden]; ok {
			t.Fatalf("private-listed row exposed protected key metadata %q: %#v", forbidden, row)
		}
	}
}

func TestChannelHandlerPrivateMonitorFailsClosedWithoutGrant(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPMWithAccess(t, signing.privateKey, "DPM", "channel-private-key", "policy-spaceaware-OMM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/monitor?visibility=private-listed", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("private monitor without grant status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "spaceaware-OMM") || strings.Contains(rec.Body.String(), "private-listed") {
		t.Fatalf("private monitor leaked private metadata without grant: %s", rec.Body.String())
	}

	grantReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/channels/spaceaware-OMM/grants",
		strings.NewReader(`{"to":"peer-alpha","scopes":["list_private"]}`),
	)
	grantRec := httptest.NewRecorder()
	mux.ServeHTTP(grantRec, grantReq)
	if grantRec.Code != http.StatusCreated {
		t.Fatalf("grant status = %d body=%s", grantRec.Code, grantRec.Body.String())
	}
	grantID := decodeChannelJSON(t, grantRec.Body.String())["grantId"].(string)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/monitor?visibility=private-listed&subject=peer-alpha&grantId="+grantID, nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("private monitor with grant status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeChannelJSON(t, rec.Body.String())
	if body["channelId"] != "spaceaware-OMM" || body["visibility"] != "private-listed" || body["grantState"] != "verified" {
		t.Fatalf("private monitor did not return verified grant metadata: %#v", body)
	}
}

func TestChannelHandlerPrivateHiddenMetadataFailsClosedWithoutGrant(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPMWithAccess(t, signing.privateKey, "DPM", "channel-private-key", "private-hidden:policy-spaceaware-OMM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	for _, target := range []string{
		"/api/v1/channels/spaceaware-OMM",
		"/api/v1/channels/spaceaware-OMM/monitor",
		"/api/v1/channels/spaceaware-OMM/pnm",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s without grant status = %d body=%s", target, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "spaceaware-OMM") || strings.Contains(rec.Body.String(), "private-hidden") {
			t.Fatalf("%s leaked private-hidden metadata without grant: %s", target, rec.Body.String())
		}
	}

	grantReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/channels/spaceaware-OMM/grants",
		strings.NewReader(`{"to":"peer-alpha","scopes":["list_private"]}`),
	)
	grantRec := httptest.NewRecorder()
	mux.ServeHTTP(grantRec, grantReq)
	if grantRec.Code != http.StatusCreated {
		t.Fatalf("grant status = %d body=%s", grantRec.Code, grantRec.Body.String())
	}
	grantID := decodeChannelJSON(t, grantRec.Body.String())["grantId"].(string)

	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM?subject=peer-alpha&grantId="+grantID, nil)
	detailRec := httptest.NewRecorder()
	mux.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("private-hidden detail with grant status = %d body=%s", detailRec.Code, detailRec.Body.String())
	}
	detailBody := decodeChannelJSON(t, detailRec.Body.String())
	if detailBody["visibility"] != "private-hidden" || detailBody["grantState"] != "verified" {
		t.Fatalf("private-hidden detail did not require verified grant: %#v", detailBody)
	}

	pnmReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/pnm?subject=peer-alpha&grantId="+grantID, nil)
	pnmRec := httptest.NewRecorder()
	mux.ServeHTTP(pnmRec, pnmReq)
	if pnmRec.Code != http.StatusOK {
		t.Fatalf("private-hidden PNM with grant status = %d body=%s", pnmRec.Code, pnmRec.Body.String())
	}
	if pnmRec.Header().Get("Content-Type") != "application/vnd.sdn.pnm" || pnmRec.Body.Len() == 0 {
		t.Fatalf("private-hidden PNM response did not return verified PNM bytes: contentType=%q len=%d", pnmRec.Header().Get("Content-Type"), pnmRec.Body.Len())
	}
}

func TestChannelHandlerVerifiedEncryptedDPMMakesChannelPrivateFailClosed(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPMWithAccess(t, signing.privateKey, "DPM", "channel-private-key", "policy-spaceaware-OMM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	monitorReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/monitor", nil)
	monitorRec := httptest.NewRecorder()
	mux.ServeHTTP(monitorRec, monitorReq)
	if monitorRec.Code != http.StatusOK {
		t.Fatalf("monitor status = %d body=%s", monitorRec.Code, monitorRec.Body.String())
	}
	monitorBody := decodeChannelJSON(t, monitorRec.Body.String())
	if monitorBody["visibility"] != "private-listed" ||
		monitorBody["grantState"] != "required" ||
		monitorBody["encryptionState"] != "encrypted" {
		t.Fatalf("monitor did not reflect encrypted private DPM: %#v", monitorBody)
	}

	subscribeReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/subscribe", nil)
	subscribeRec := httptest.NewRecorder()
	mux.ServeHTTP(subscribeRec, subscribeReq)
	if subscribeRec.Code != http.StatusForbidden {
		t.Fatalf("private subscribe without grant status = %d body=%s", subscribeRec.Code, subscribeRec.Body.String())
	}

	streamBytes := nativeAPIFrame("OMM1", []byte{1, 2, 3})
	publishReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?stream=1", bytes.NewReader(streamBytes))
	publishReq.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	publishRec := httptest.NewRecorder()
	mux.ServeHTTP(publishRec, publishReq)
	if publishRec.Code != http.StatusForbidden {
		t.Fatalf("private stream publish without grant status = %d body=%s", publishRec.Code, publishRec.Body.String())
	}

	grantReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/channels/spaceaware-OMM/grants",
		strings.NewReader(`{"to":"peer-alpha","scopes":["publish","stream_open"]}`),
	)
	grantRec := httptest.NewRecorder()
	mux.ServeHTTP(grantRec, grantReq)
	if grantRec.Code != http.StatusCreated {
		t.Fatalf("grant status = %d body=%s", grantRec.Code, grantRec.Body.String())
	}
	grantID := decodeChannelJSON(t, grantRec.Body.String())["grantId"].(string)

	publishReq = httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?stream=1&subject=peer-alpha&grantId="+grantID, bytes.NewReader(streamBytes))
	publishReq.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	publishRec = httptest.NewRecorder()
	mux.ServeHTTP(publishRec, publishReq)
	if publishRec.Code != http.StatusAccepted {
		t.Fatalf("private stream publish with grant status = %d body=%s", publishRec.Code, publishRec.Body.String())
	}

	openReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/stream", nil)
	openRec := httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusForbidden {
		t.Fatalf("private stream open without grant status = %d body=%s", openRec.Code, openRec.Body.String())
	}

	openReq = httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/stream?subject=peer-alpha&grantId="+grantID, nil)
	openRec = httptest.NewRecorder()
	mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("private stream open with grant status = %d body=%s", openRec.Code, openRec.Body.String())
	}
	if !bytes.Equal(openRec.Body.Bytes(), streamBytes) {
		t.Fatal("private stream open did not return verified stream bytes after grant")
	}
}

func TestChannelHandlerReadsPrivateNativeFlatBufferByteRangeWithGrant(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	mux := http.NewServeMux()
	NewChannelHandler(nil).RegisterRoutes(mux)
	manifest := buildAPISignedDPMWithAccess(t, signing.privateKey, "DPM", "channel-private-key", "policy-spaceaware-OMM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	streamBytes := bytes.Join([][]byte{
		nativeAPIFrame("OMM1", []byte{1, 2, 3}),
		nativeAPIFrame("OMM1", []byte{4, 5, 6, 7}),
	}, nil)

	grantReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/channels/spaceaware-OMM/grants",
		strings.NewReader(`{"to":"peer-alpha","scopes":["publish","byte_range_read"]}`),
	)
	grantRec := httptest.NewRecorder()
	mux.ServeHTTP(grantRec, grantReq)
	if grantRec.Code != http.StatusCreated {
		t.Fatalf("grant status = %d body=%s", grantRec.Code, grantRec.Body.String())
	}
	grantID := decodeChannelJSON(t, grantRec.Body.String())["grantId"].(string)

	publishReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?stream=1&subject=peer-alpha&grantId="+grantID, bytes.NewReader(streamBytes))
	publishReq.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	publishRec := httptest.NewRecorder()
	mux.ServeHTTP(publishRec, publishReq)
	if publishRec.Code != http.StatusAccepted {
		t.Fatalf("private stream publish with grant status = %d body=%s", publishRec.Code, publishRec.Body.String())
	}

	rangeReq := httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/bytes?offset=1&length=3", nil)
	rangeRec := httptest.NewRecorder()
	mux.ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusForbidden {
		t.Fatalf("private byte range without grant status = %d body=%s", rangeRec.Code, rangeRec.Body.String())
	}

	rangeReq = httptest.NewRequest(http.MethodGet, "/api/v1/channels/spaceaware-OMM/bytes?offset=1&length=3&subject=peer-alpha&grantId="+grantID, nil)
	rangeRec = httptest.NewRecorder()
	mux.ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent {
		t.Fatalf("private byte range with grant status = %d body=%s", rangeRec.Code, rangeRec.Body.String())
	}
	if got := rangeRec.Header().Get("Content-Range"); got != "bytes 1-3/"+strconv.Itoa(len(streamBytes)) {
		t.Fatalf("private byte range Content-Range = %q", got)
	}
	if !bytes.Equal(rangeRec.Body.Bytes(), streamBytes[1:4]) {
		t.Fatalf("private byte range body = %v, want %v", rangeRec.Body.Bytes(), streamBytes[1:4])
	}
}

func TestChannelHandlerPrivateDurableImportPreservesDPMContentKeyScope(t *testing.T) {
	t.Parallel()

	signing := newChannelSigningFixture(t)
	store := newChannelTestStore(t)
	mux := http.NewServeMux()
	NewChannelHandler(store).RegisterRoutes(mux)
	manifest := buildAPISignedDPMWithAccess(t, signing.privateKey, "DPM", "channel-private-key", "policy-spaceaware-OMM")

	publishPNMForChannel(t, mux, "spaceaware-OMM", signing, manifest.CID, "DPM")
	publishDPMForChannel(t, mux, "spaceaware-OMM", signing, manifest)

	grantReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/channels/spaceaware-OMM/grants",
		strings.NewReader(`{"to":"peer-alpha","scopes":["publish"]}`),
	)
	grantRec := httptest.NewRecorder()
	mux.ServeHTTP(grantRec, grantReq)
	if grantRec.Code != http.StatusCreated {
		t.Fatalf("grant status = %d body=%s", grantRec.Code, grantRec.Body.String())
	}
	grantID := decodeChannelJSON(t, grantRec.Body.String())["grantId"].(string)

	streamBytes := nativeAPIFrame("OMM1", []byte{1, 2, 3})
	publishReq := httptest.NewRequest(http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish?stream=1&subject=peer-alpha&grantId="+grantID, bytes.NewReader(streamBytes))
	publishReq.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	publishRec := httptest.NewRecorder()
	mux.ServeHTTP(publishRec, publishReq)
	if publishRec.Code != http.StatusAccepted {
		t.Fatalf("private stream publish with grant status = %d body=%s", publishRec.Code, publishRec.Body.String())
	}

	schemaName, err := channels.SchemaNameFromStandardCode("OMM")
	if err != nil {
		t.Fatalf("SchemaNameFromStandardCode failed: %v", err)
	}
	records, err := store.QueryRawRecords(storage.RawRecordQuery{SchemaName: schemaName, Limit: 10})
	if err != nil {
		t.Fatalf("QueryRawRecords failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("durable private OMM records = %d, want 1", len(records))
	}
	tags, err := store.GetSourceTags(schemaName, records[0].CID)
	if err != nil {
		t.Fatalf("GetSourceTags failed: %v", err)
	}
	if tags.ContentKeyID != "channel-private-key" {
		t.Fatalf("private durable ContentKeyID = %q, want channel-private-key", tags.ContentKeyID)
	}
}

func nativeAPIFrame(fileIdentifier string, payload []byte) []byte {
	if len(fileIdentifier) != 4 {
		panic("fileIdentifier must be four bytes")
	}
	frame := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(frame[:4], uint32(4+len(payload)))
	copy(frame[4:8], []byte(fileIdentifier))
	copy(frame[8:], payload)
	return frame
}

type channelSigningFixture struct {
	publicKey  ed25519.PublicKey
	privateKey ed25519.PrivateKey
}

func newChannelSigningFixture(t *testing.T) channelSigningFixture {
	t.Helper()

	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	return channelSigningFixture{
		publicKey:  append(ed25519.PublicKey(nil), publicKey...),
		privateKey: append(ed25519.PrivateKey(nil), privateKey...),
	}
}

func (f channelSigningFixture) providerKeyQuery() string {
	return "providerPublicKey=" + hex.EncodeToString(f.publicKey)
}

func publishPNMForChannel(t *testing.T, mux *http.ServeMux, channelID string, signing channelSigningFixture, cid string, fileID string) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/"+channelID+"/publish?"+signing.providerKeyQuery(), bytes.NewReader(buildAPISignedPNM(t, signing.privateKey, cid, fileID)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("PNM publish status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeChannelJSON(t, rec.Body.String())
	if body["pnmVerified"] != true || body["pnmCid"] != cid {
		t.Fatalf("unexpected PNM publish response: %#v", body)
	}
}

func publishDPMForChannel(t *testing.T, mux *http.ServeMux, channelID string, signing channelSigningFixture, manifest *storage.DatasetPublicationManifest) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/"+channelID+"/publish?manifest=1&"+signing.providerKeyQuery(), bytes.NewReader(manifest.Bytes))
	req.Header.Set("Content-Type", "application/vnd.sdn.dpm")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("DPM publish status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeChannelJSON(t, rec.Body.String())
	if body["dpmVerified"] != true || body["dpmFileId"] != manifest.FileID {
		t.Fatalf("unexpected DPM publish response: %#v", body)
	}
}

func newChannelTestStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close FlatSQL store failed: %v", err)
		}
	})
	return store
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

func buildAPISignedPNM(t *testing.T, signingKey ed25519.PrivateKey, cid string, fileID string) []byte {
	t.Helper()

	signature := ed25519.Sign(signingKey, channelTestPNMSignaturePayload(cid, fileID))
	builder := flatbuffers.NewBuilder(256)
	cidOffset := builder.CreateString(cid)
	fileIDOffset := builder.CreateString(fileID)
	timestampOffset := builder.CreateString(time.Now().UTC().Format(time.RFC3339))
	signatureOffset := builder.CreateString(hex.EncodeToString(signature))
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

func channelTestPNMSignaturePayload(manifestCID, fileID string) []byte {
	payload := make([]byte, 0, len(manifestCID)+len(fileID)+18)
	payload = append(payload, []byte("SDN-DPM-PNM\x00")...)
	payload = append(payload, fileID...)
	payload = append(payload, 0)
	payload = append(payload, manifestCID...)
	return payload
}

func buildAPISignedDPM(t *testing.T, signingKey ed25519.PrivateKey, fileID string) *storage.DatasetPublicationManifest {
	return buildAPISignedDPMWithAccess(t, signingKey, fileID, "public", "")
}

func buildAPISignedDPMWithAccess(t *testing.T, signingKey ed25519.PrivateKey, fileID string, contentKeyID string, encryptionPolicy string) *storage.DatasetPublicationManifest {
	t.Helper()

	schemaName, err := channels.SchemaNameFromStandardCode("OMM")
	if err != nil {
		t.Fatalf("SchemaNameFromStandardCode failed: %v", err)
	}
	export, err := storage.ExportDatasetRecords(t.TempDir(), storage.IndexedRecordQuery{
		SchemaName:          schemaName,
		ProviderID:          "spaceaware",
		SourceName:          "channel:spaceaware-OMM",
		BatchID:             "batch-1",
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	}, []storage.DatasetExportRecord{{
		Data: sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build(),
		SourceTags: storage.SourceTags{
			ProviderID:        "spaceaware",
			SourceName:        "channel:spaceaware-OMM",
			BatchID:           "batch-1",
			ContentKeyID:      "public",
			ProducerPeerID:    "spaceaware",
			ProducerPublicKey: "spaceaware",
		},
	}})
	if err != nil {
		t.Fatalf("ExportDatasetRecords failed: %v", err)
	}
	export.ContentKeyID = contentKeyID
	export.EncryptionPolicy = encryptionPolicy
	manifest, err := storage.BuildSignedDatasetPublicationManifest(t.TempDir(), storage.DatasetPublicationManifestOptions{
		Export:          export,
		DatasetID:       "spaceaware",
		UpdateID:        "batch-1",
		FileID:          fileID,
		ProviderPeerID:  "spaceaware",
		ProviderEPMCID:  "bafy-provider-epm",
		PublishedAt:     time.Now().UTC(),
		SigningKey:      signingKey,
		SchemaHash:      "channel-test-schema",
		QueryEngine:     "FlatSQL",
		QueryEngineVers: "sdn-channel-test",
	})
	if err != nil {
		t.Fatalf("BuildSignedDatasetPublicationManifest failed: %v", err)
	}
	return manifest
}

func decodeChannelJSON(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode JSON failed: %v\n%s", err, body)
	}
	return payload
}
