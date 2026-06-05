package api

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type ChannelHandler struct {
	store         *storage.FlatSQLStore
	gate          *channels.AccessGate
	grants        *channels.ChannelGrantRegistry
	metadata      *channels.VerifiedMetadataRegistry
	streams       *channels.NativeStreamRegistry
	subscriptions *channels.SubscriptionRegistry
}

func NewChannelHandler(store *storage.FlatSQLStore) *ChannelHandler {
	grants := channels.NewChannelGrantRegistry()
	return &ChannelHandler{
		store:         store,
		gate:          channels.NewAccessGate(grants),
		grants:        grants,
		metadata:      channels.NewVerifiedMetadataRegistry(),
		streams:       channels.NewNativeStreamRegistry(),
		subscriptions: channels.NewSubscriptionRegistry(),
	}
}

func (h *ChannelHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/channels", h.handleCollection)
	mux.HandleFunc("/api/v1/channels/", h.handleChannel)
}

func (h *ChannelHandler) handleCollection(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v1/channels" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	standardFilter := strings.TrimSpace(r.URL.Query().Get("standardCode"))
	if standardFilter == "" {
		standardFilter = strings.TrimSpace(r.URL.Query().Get("standard"))
	}
	if h.isPrivateVisibilityRequest(r) {
		h.writeAccessDenied(w, channels.AccessDecision{
			Allowed:    false,
			GrantState: "required",
			Reason:     "verified channel grant required",
		})
		return
	}
	results := make([]map[string]interface{}, 0)
	if standardFilter != "" {
		code, err := channels.AssertStandardCode(standardFilter)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		results = append(results, channelListRow(code))
	} else {
		for _, schemaName := range sds.SupportedSchemas {
			code, err := channels.StandardCodeFromSchemaName(schemaName)
			if err != nil {
				continue
			}
			results = append(results, channelListRow(code))
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":   len(results),
		"results": results,
	})
}

func (h *ChannelHandler) handleChannel(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/channels/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(rest, "/")
	channelID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	switch action {
	case "":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, h.channelDetail(parsed))
	case "monitor":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, h.channelMonitor(parsed))
	case "pnm":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.getPNM(w, parsed)
	case "subscribe":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if h.requiresPrivateGrant(r, parsed) {
			decision := h.authorizeGrant(r, parsed, channels.BoundarySubscribe)
			if !decision.Allowed {
				h.writeAccessDenied(w, decision)
				return
			}
			writeJSON(w, http.StatusOK, h.subscriptionResponseWithDecision(parsed, h.subscriptions.Subscribe(parsed), decision))
			return
		}
		writeJSON(w, http.StatusOK, h.subscriptionResponse(parsed, h.subscriptions.Subscribe(parsed)))
	case "unsubscribe":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if h.requiresPrivateGrant(r, parsed) {
			decision := h.authorizeGrant(r, parsed, channels.BoundaryUnsubscribe)
			if !decision.Allowed {
				h.writeAccessDenied(w, decision)
				return
			}
			writeJSON(w, http.StatusOK, h.subscriptionResponseWithDecision(parsed, h.subscriptions.Unsubscribe(parsed), decision))
			return
		}
		writeJSON(w, http.StatusOK, h.subscriptionResponse(parsed, h.subscriptions.Unsubscribe(parsed)))
	case "publish":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if h.requiresPrivateGrant(r, parsed) {
			decision := h.authorizeGrant(r, parsed, channels.BoundaryPublish)
			if !decision.Allowed {
				h.writeAccessDenied(w, decision)
				return
			}
			h.publishPublic(w, r, parsed)
			return
		}
		h.publishPublic(w, r, parsed)
	case "stream":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.openStream(w, r, parsed)
	case "bytes":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.readStreamBytes(w, r, parsed)
	case "key-unwrap":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, r, parsed, channels.BoundaryKeyUnwrap)
	case "shard-import":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, r, parsed, channels.BoundaryShardImport)
	case "module-feed":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, r, parsed, channels.BoundaryModuleFeedDelivery)
	case "cache":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, r, parsed, channels.BoundaryLocalCacheRead)
	case "grants":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if h.isPrivateVisibilityRequest(r) {
			h.requireGrant(w, r, parsed, channels.BoundaryGrantIssue)
			return
		}
		h.issueGrant(w, r, parsed)
	default:
		http.NotFound(w, r)
	}
}

func (h *ChannelHandler) requireGrant(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID, boundary channels.AccessBoundary) {
	decision := h.authorizeGrant(r, parsed, boundary)
	if !decision.Allowed {
		h.writeAccessDenied(w, decision)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"channelId":  parsed.ChannelID,
		"grantState": decision.GrantState,
	})
}

func (h *ChannelHandler) authorizeGrant(r *http.Request, parsed channels.ChannelID, boundary channels.AccessBoundary) channels.AccessDecision {
	gate := h.gate
	if gate == nil {
		gate = channels.NewAccessGate(nil)
	}
	return gate.Authorize(channels.AccessRequest{
		Channel:  parsed,
		Boundary: boundary,
		Subject:  strings.TrimSpace(r.URL.Query().Get("subject")),
		GrantID:  strings.TrimSpace(r.URL.Query().Get("grantId")),
	})
}

type channelGrantIssuePayload struct {
	To        string   `json:"to"`
	Subject   string   `json:"subject"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expiresAt"`
}

func (h *ChannelHandler) issueGrant(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID) {
	var payload channelGrantIssuePayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid grant payload: "+err.Error())
		return
	}
	subject := strings.TrimSpace(payload.Subject)
	if subject == "" {
		subject = strings.TrimSpace(payload.To)
	}
	scopes, err := parseGrantScopes(payload.Scopes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	expiresAt := time.Time{}
	if strings.TrimSpace(payload.ExpiresAt) != "" {
		expiresAt, err = time.Parse(time.RFC3339, strings.TrimSpace(payload.ExpiresAt))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid expiresAt: "+err.Error())
			return
		}
	}
	grant, err := h.grants.Issue(channels.ChannelGrantIssueRequest{
		Channel:   parsed,
		Subject:   subject,
		Scopes:    scopes,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, channelGrantResponse(grant))
}

func (h *ChannelHandler) publishPublic(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID) {
	if h.isNativeStreamPublish(r) {
		h.publishNativeStream(w, r, parsed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read PNM envelope: "+err.Error())
		return
	}
	if h.isDPMManifestPublish(r, body) {
		h.publishDPMManifest(w, r, parsed, body)
		return
	}
	providerPublicKey, err := providerPublicKeyFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	evidence, err := channels.VerifySignedPNMEnvelopeWithProviderKey(body, providerPublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "verified PNM envelope required: "+err.Error())
		return
	}
	metadata := h.metadata.RecordPNM(parsed, evidence)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"channelId":     parsed.ChannelID,
		"standardCode":  parsed.StandardCode,
		"pnmVerified":   true,
		"pnmCid":        evidence.CID,
		"signatureType": evidence.SignatureType,
		"verifiedAt":    metadata.VerifiedAt.Format(time.RFC3339Nano),
	})
}

func (h *ChannelHandler) getPNM(w http.ResponseWriter, parsed channels.ChannelID) {
	metadata, verified := h.metadata.Get(parsed)
	if !verified || len(metadata.PNMBytes) == 0 {
		writeError(w, http.StatusNotFound, "verified PNM unavailable for channel")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sdn.pnm")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(metadata.PNMBytes)
}

func (h *ChannelHandler) publishDPMManifest(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID, body []byte) {
	metadata, verified := h.metadata.Get(parsed)
	if !verified {
		writeError(w, http.StatusForbidden, "verified PNM required before DPM publish")
		return
	}
	providerPublicKey, err := providerPublicKeyFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	providerPublicKeyHex := hex.EncodeToString(providerPublicKey)
	if metadata.ProviderPublicKey != "" && metadata.ProviderPublicKey != providerPublicKeyHex {
		writeError(w, http.StatusForbidden, "DPM provider public key does not match verified PNM provider")
		return
	}
	evidence, err := storage.VerifySignedDatasetPublicationManifest(body, providerPublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "verified DPM manifest required: "+err.Error())
		return
	}
	if metadata.PNMCID != "" && evidence.ManifestCID != metadata.PNMCID {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("DPM CID %q does not match PNM CID %q", evidence.ManifestCID, metadata.PNMCID))
		return
	}
	if metadata.PNMFileID != "" && evidence.FileID != metadata.PNMFileID {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("DPM FILE_ID %q does not match PNM FILE_ID %q", evidence.FileID, metadata.PNMFileID))
		return
	}
	metadata, ok := h.metadata.RecordDPM(parsed, channels.DPMTrustEvidence{
		FileID:        evidence.FileID,
		SignatureType: evidence.SignatureType,
		ProviderPeer:  evidence.ProviderPeer,
		Encrypted:     evidence.Encrypted,
		ContentKeyID:  evidence.ContentKeyID,
		PolicyID:      evidence.PolicyID,
	})
	if !ok {
		writeError(w, http.StatusForbidden, "verified PNM required before DPM publish")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"channelId":     parsed.ChannelID,
		"standardCode":  parsed.StandardCode,
		"pnmVerified":   true,
		"dpmVerified":   true,
		"pnmCid":        metadata.PNMCID,
		"dpmFileId":     evidence.FileID,
		"providerPeer":  metadata.ProviderPeer,
		"signatureType": evidence.SignatureType,
		"verifiedAt":    metadata.DPMVerifiedAt.Format(time.RFC3339Nano),
	})
}

func (h *ChannelHandler) publishNativeStream(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID) {
	started := time.Now()
	pnmDPMStarted := time.Now()
	metadata, verified := h.metadata.Get(parsed)
	if !verified {
		writeError(w, http.StatusForbidden, "verified PNM required before stream publish")
		return
	}
	if metadata.DPMVerifiedAt.IsZero() {
		writeError(w, http.StatusForbidden, "verified DPM required before stream publish")
		return
	}
	pnmDPMDuration := time.Since(pnmDPMStarted)
	transferStarted := time.Now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read native FlatBuffer stream: "+err.Error())
		return
	}
	transferDuration := time.Since(transferStarted)
	frames, err := channels.SplitNativeStreamFrames(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid native FlatBuffer stream: "+err.Error())
		return
	}
	schemaName, err := channels.SchemaNameFromStandardCode(parsed.StandardCode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	throughputBPS := measuredBytesPerSecond(len(body), time.Since(started))
	wireUtilization := measuredWireSpeedUtilization(throughputBPS)
	timings := channelThroughputTimings{
		PNMDPMVerification: pnmDPMDuration,
		Transfer:           transferDuration,
	}
	gate := evaluateChannelWireSpeedGate(throughputBPS, wireUtilization, timings)
	if gate.Enabled && !gate.TargetMet {
		writeJSON(w, http.StatusTooManyRequests, gate.Response(parsed, len(body), len(frames)))
		return
	}
	importedRows := 0
	if h.store != nil {
		importStarted := time.Now()
		tags := storage.SourceTags{
			ProviderID:        parsed.SourceID,
			SourceName:        "channel:" + parsed.ChannelID,
			BatchID:           parsed.ChannelID,
			ContentKeyID:      "public",
			ProducerPeerID:    parsed.SourceID,
			ProducerPublicKey: parsed.SourceID,
		}
		importedRows, err = h.store.StoreBatchWithSourceTags(schemaName, frames, "channel:"+parsed.SourceID, nil, tags)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "durable FlatSQL import failed: "+err.Error())
			return
		}
		timings.DurableImport = time.Since(importStarted)
	}
	snapshot, err := h.streams.Store(parsed, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid native FlatBuffer stream: "+err.Error())
		return
	}
	metadata, _ = h.metadata.RecordNativeStream(parsed, snapshot, throughputBPS, wireUtilization)
	response := map[string]interface{}{
		"channelId":                parsed.ChannelID,
		"standardCode":             parsed.StandardCode,
		"pnmVerified":              true,
		"pnmCid":                   metadata.PNMCID,
		"streamBytes":              snapshot.ByteCount,
		"streamFrames":             snapshot.FrameCount,
		"throughputBytesPerSecond": throughputBPS,
		"wireSpeedUtilization":     wireUtilization,
		"importedRows":             importedRows,
		"verifiedAt":               metadata.VerifiedAt.Format(time.RFC3339Nano),
		"streamUpdated":            snapshot.UpdatedAt.Format(time.RFC3339Nano),
		"timingsMs":                timings.AsMilliseconds(),
	}
	if gate.Enabled {
		response["wireSpeedTarget"] = gate.Target
		response["requiredBytesPerSecond"] = gate.RequiredBytesPerSecond
		response["targetMet"] = gate.TargetMet
	}
	writeJSON(w, http.StatusAccepted, response)
}

func measuredBytesPerSecond(byteCount int, elapsed time.Duration) int64 {
	if byteCount <= 0 {
		return 0
	}
	if elapsed <= 0 {
		elapsed = time.Nanosecond
	}
	return int64(float64(byteCount) / elapsed.Seconds())
}

func measuredWireSpeedUtilization(throughputBPS int64) *float64 {
	if throughputBPS <= 0 {
		return nil
	}
	linkGBit := strings.TrimSpace(os.Getenv("SDN_TEST_LINK_GBIT"))
	if linkGBit == "" {
		return nil
	}
	gbits, err := strconv.ParseFloat(linkGBit, 64)
	if err != nil || gbits <= 0 {
		return nil
	}
	linkBytesPerSecond := gbits * 1_000_000_000 / 8
	utilization := float64(throughputBPS) / linkBytesPerSecond
	return &utilization
}

type channelThroughputTimings struct {
	Discovery          time.Duration
	GrantNegotiation   time.Duration
	PNMDPMVerification time.Duration
	Transfer           time.Duration
	Decrypt            time.Duration
	HashVerification   time.Duration
	DurableImport      time.Duration
}

func (t channelThroughputTimings) AsMilliseconds() map[string]int64 {
	return map[string]int64{
		"discovery":          durationMilliseconds(t.Discovery),
		"grantNegotiation":   durationMilliseconds(t.GrantNegotiation),
		"pnmDpmVerification": durationMilliseconds(t.PNMDPMVerification),
		"transfer":           durationMilliseconds(t.Transfer),
		"decrypt":            durationMilliseconds(t.Decrypt),
		"hashVerification":   durationMilliseconds(t.HashVerification),
		"durableImport":      durationMilliseconds(t.DurableImport),
	}
}

func durationMilliseconds(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	return int64(duration / time.Millisecond)
}

type channelWireSpeedGate struct {
	Enabled                bool
	Target                 float64
	RequiredBytesPerSecond int64
	TargetMet              bool
	Timings                channelThroughputTimings
	WireUtilization        *float64
	ThroughputBytesPerSec  int64
}

func evaluateChannelWireSpeedGate(throughputBPS int64, wireUtilization *float64, timings channelThroughputTimings) channelWireSpeedGate {
	gate := channelWireSpeedGate{
		Target:                0.90,
		Timings:               timings,
		WireUtilization:       wireUtilization,
		ThroughputBytesPerSec: throughputBPS,
	}
	if strings.TrimSpace(os.Getenv("SDN_WIRESPEED_TEST")) != "1" {
		return gate
	}
	linkBytesPerSecond, ok := channelLinkBytesPerSecond()
	if !ok {
		return gate
	}
	gate.Enabled = true
	gate.RequiredBytesPerSecond = int64(linkBytesPerSecond * gate.Target)
	gate.TargetMet = throughputBPS >= gate.RequiredBytesPerSecond
	return gate
}

func channelLinkBytesPerSecond() (float64, bool) {
	linkGBit := strings.TrimSpace(os.Getenv("SDN_TEST_LINK_GBIT"))
	if linkGBit == "" {
		return 0, false
	}
	gbits, err := strconv.ParseFloat(linkGBit, 64)
	if err != nil || gbits <= 0 {
		return 0, false
	}
	return gbits * 1_000_000_000 / 8, true
}

func (g channelWireSpeedGate) Response(parsed channels.ChannelID, streamBytes int, streamFrames int) map[string]interface{} {
	return map[string]interface{}{
		"error":                    "channel stream throughput below configured wire-speed gate",
		"channelId":                parsed.ChannelID,
		"standardCode":             parsed.StandardCode,
		"streamBytes":              streamBytes,
		"streamFrames":             streamFrames,
		"throughputBytesPerSecond": g.ThroughputBytesPerSec,
		"wireSpeedUtilization":     g.WireUtilization,
		"wireSpeedTarget":          g.Target,
		"requiredBytesPerSecond":   g.RequiredBytesPerSecond,
		"targetMet":                g.TargetMet,
		"timingsMs":                g.Timings.AsMilliseconds(),
	}
}

func (h *ChannelHandler) openStream(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID) {
	if h.requiresPrivateGrant(r, parsed) {
		decision := h.authorizeGrant(r, parsed, channels.BoundaryStreamOpen)
		if !decision.Allowed {
			h.writeAccessDenied(w, decision)
			return
		}
	}
	snapshot, ok := h.streams.Get(parsed)
	if !ok {
		writeError(w, http.StatusNotFound, "verified native FlatBuffer stream unavailable for channel")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(snapshot.Bytes)
}

func (h *ChannelHandler) readStreamBytes(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID) {
	if h.requiresPrivateGrant(r, parsed) {
		decision := h.authorizeGrant(r, parsed, channels.BoundaryByteRangeRead)
		if !decision.Allowed {
			h.writeAccessDenied(w, decision)
			return
		}
	}
	snapshot, ok := h.streams.Get(parsed)
	if !ok {
		writeError(w, http.StatusNotFound, "verified native FlatBuffer stream unavailable for channel")
		return
	}
	start, end, err := parseByteRangeQuery(r, len(snapshot.Bytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end-1, len(snapshot.Bytes)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(snapshot.Bytes[start:end])
}

func parseByteRangeQuery(r *http.Request, total int) (int, int, error) {
	if total <= 0 {
		return 0, 0, fmt.Errorf("verified native FlatBuffer stream is empty")
	}
	offsetText := strings.TrimSpace(r.URL.Query().Get("offset"))
	lengthText := strings.TrimSpace(r.URL.Query().Get("length"))
	if offsetText == "" || lengthText == "" {
		return 0, 0, fmt.Errorf("offset and length query parameters are required")
	}
	offset, err := strconv.ParseInt(offsetText, 10, 64)
	if err != nil || offset < 0 {
		return 0, 0, fmt.Errorf("offset must be a non-negative integer")
	}
	length, err := strconv.ParseInt(lengthText, 10, 64)
	if err != nil || length <= 0 {
		return 0, 0, fmt.Errorf("length must be a positive integer")
	}
	if offset >= int64(total) {
		return 0, 0, fmt.Errorf("offset is outside verified stream byte range")
	}
	end := offset + length
	if end > int64(total) {
		end = int64(total)
	}
	return int(offset), int(end), nil
}

func parseGrantScopes(values []string) ([]channels.AccessBoundary, error) {
	if len(values) == 0 {
		return nil, nil
	}
	allowed := map[string]channels.AccessBoundary{
		string(channels.BoundarySubscribe):          channels.BoundarySubscribe,
		string(channels.BoundaryUnsubscribe):        channels.BoundaryUnsubscribe,
		string(channels.BoundaryPublish):            channels.BoundaryPublish,
		string(channels.BoundaryStreamOpen):         channels.BoundaryStreamOpen,
		string(channels.BoundaryByteRangeRead):      channels.BoundaryByteRangeRead,
		string(channels.BoundaryKeyUnwrap):          channels.BoundaryKeyUnwrap,
		string(channels.BoundaryShardImport):        channels.BoundaryShardImport,
		string(channels.BoundaryModuleFeedDelivery): channels.BoundaryModuleFeedDelivery,
		string(channels.BoundaryLocalCacheRead):     channels.BoundaryLocalCacheRead,
	}
	scopes := make([]channels.AccessBoundary, 0, len(values))
	for _, value := range values {
		scope := strings.TrimSpace(value)
		boundary, ok := allowed[scope]
		if !ok {
			return nil, fmt.Errorf("invalid channel grant scope %q", value)
		}
		scopes = append(scopes, boundary)
	}
	return scopes, nil
}

func channelGrantResponse(grant channels.ChannelGrant) map[string]interface{} {
	scopes := make([]string, 0, len(grant.Scopes))
	for _, scope := range grant.Scopes {
		scopes = append(scopes, string(scope))
	}
	return map[string]interface{}{
		"grantId":    grant.GrantID,
		"channelId":  grant.ChannelID,
		"subject":    grant.Subject,
		"scopes":     scopes,
		"grantState": "verified",
		"issuedAt":   grant.IssuedAt.Format(time.RFC3339Nano),
		"expiresAt":  grant.ExpiresAt.Format(time.RFC3339Nano),
	}
}

func (h *ChannelHandler) writeAccessDenied(w http.ResponseWriter, decision channels.AccessDecision) {
	message := decision.Reason
	if strings.TrimSpace(message) == "" {
		message = "verified channel grant required"
	}
	writeError(w, http.StatusForbidden, message)
}

func (h *ChannelHandler) isPrivateVisibilityRequest(r *http.Request) bool {
	visibility := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("visibility")))
	return visibility == "private" || strings.HasPrefix(visibility, "private-")
}

func (h *ChannelHandler) requiresPrivateGrant(r *http.Request, parsed channels.ChannelID) bool {
	if h.isPrivateVisibilityRequest(r) {
		return true
	}
	metadata, ok := h.metadata.Get(parsed)
	if !ok {
		return false
	}
	return isPrivateChannelMetadata(metadata)
}

func isPrivateChannelMetadata(metadata channels.VerifiedMetadata) bool {
	return strings.HasPrefix(metadata.Visibility, "private") || metadata.EncryptionState == "encrypted"
}

func (h *ChannelHandler) isNativeStreamPublish(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("stream")), "1") ||
		strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("stream")), "true") {
		return true
	}
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	return strings.HasPrefix(contentType, "application/vnd.sdn.flatbuffers.stream")
}

func (h *ChannelHandler) isDPMManifestPublish(r *http.Request, body []byte) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("manifest")), "1") ||
		strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("manifest")), "true") {
		return true
	}
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	return strings.HasPrefix(contentType, "application/vnd.sdn.dpm") || channels.IsDPMManifest(body)
}

func providerPublicKeyFromRequest(r *http.Request) (ed25519.PublicKey, error) {
	value := strings.TrimSpace(r.URL.Query().Get("providerPublicKey"))
	if value == "" {
		value = strings.TrimSpace(r.URL.Query().Get("provider_public_key"))
	}
	if value == "" {
		value = strings.TrimSpace(r.Header.Get("X-SDN-Provider-Public-Key"))
	}
	if value == "" {
		return nil, fmt.Errorf("provider public key is required")
	}
	key, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode provider public key: %w", err)
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("provider public key length = %d, want %d", len(key), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(key), nil
}

func channelListRow(standardCode string) map[string]interface{} {
	return map[string]interface{}{
		"standardCode":    standardCode,
		"topic":           channels.DiscoveryTopic(standardCode),
		"visibility":      "public",
		"subscribed":      false,
		"grantState":      "not-required",
		"encryptionState": "none",
	}
}

func (h *ChannelHandler) channelDetail(parsed channels.ChannelID) map[string]interface{} {
	state := h.subscriptions.Get(parsed)
	metadata, verified := h.metadata.Get(parsed)
	visibility := state.Visibility
	grantState := state.GrantState
	encryptionState := state.EncryptionState
	if metadata.Visibility != "" {
		visibility = metadata.Visibility
	}
	if metadata.EncryptionState != "" {
		encryptionState = metadata.EncryptionState
	}
	if isPrivateChannelMetadata(metadata) {
		grantState = "required"
	}
	return map[string]interface{}{
		"channelId":       parsed.ChannelID,
		"sourceId":        parsed.SourceID,
		"standardCode":    parsed.StandardCode,
		"feedUuid":        emptyStringAsNil(parsed.FeedUUID),
		"visibility":      visibility,
		"subscribed":      state.Subscribed,
		"pnmVerified":     verified,
		"dpmVerified":     !metadata.DPMVerifiedAt.IsZero(),
		"pnmCid":          emptyStringAsNil(metadata.PNMCID),
		"grantState":      grantState,
		"encryptionState": encryptionState,
	}
}

func (h *ChannelHandler) channelMonitor(parsed channels.ChannelID) map[string]interface{} {
	payload := h.channelDetail(parsed)
	metadata, verified := h.metadata.Get(parsed)
	payload["channelHead"] = metadata.PNMCID
	payload["providerPeer"] = metadata.ProviderPeer
	payload["localRows"] = metadata.LocalRows
	payload["remoteRows"] = metadata.RemoteRows
	payload["syncedRows"] = metadata.SyncedRows
	payload["missingRows"] = metadata.MissingRows
	payload["pinnedRows"] = metadata.PinnedRows
	payload["syncedBytes"] = metadata.SyncedBytes
	payload["throughputBytesPerSecond"] = metadata.ThroughputBPS
	payload["wireSpeedUtilization"] = metadata.WireUtilization
	payload["lastVerifiedUpdate"] = ""
	if verified {
		payload["lastVerifiedUpdate"] = metadata.VerifiedAt.Format(time.RFC3339Nano)
	}
	return payload
}

func (h *ChannelHandler) subscriptionResponse(parsed channels.ChannelID, state channels.SubscriptionState) map[string]interface{} {
	payload := h.channelDetail(parsed)
	payload["subscribed"] = state.Subscribed
	if !state.UpdatedAt.IsZero() {
		payload["lastUpdated"] = state.UpdatedAt.Format(time.RFC3339Nano)
	}
	return payload
}

func (h *ChannelHandler) subscriptionResponseWithDecision(parsed channels.ChannelID, state channels.SubscriptionState, decision channels.AccessDecision) map[string]interface{} {
	payload := h.subscriptionResponse(parsed, state)
	payload["grantState"] = decision.GrantState
	return payload
}

func emptyStringAsNil(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}
