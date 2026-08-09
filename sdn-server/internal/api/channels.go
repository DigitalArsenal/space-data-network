package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type ChannelHandler struct {
	store            *storage.FlatSQLStore
	gate             *channels.AccessGate
	grants           *channels.ChannelGrantRegistry
	metadata         *channels.VerifiedMetadataRegistry
	streams          *channels.NativeStreamRegistry
	subscriptions    *channels.SubscriptionRegistry
	encryptedStreams EncryptedNativeStreamDecryptor
	keyEnvelopes     PrivateChannelKeyEnvelopeProvider
	accessAudit      ChannelAccessAuditSink
	replayMu         sync.Mutex
	encryptedIndexes map[string]struct{}
	// activityRing is the node's shared node_activity_read event ring (M2
	// activity capability, caps/nodeactivity.go). Optional — nil unless
	// ChannelHandlerOptions.ActivityRing is set — and every Append call is
	// itself nil-safe, so a handler constructed without one behaves
	// exactly as before this field was added.
	activityRing *caps.ActivityRing
}

type EncryptedNativeStreamHeader struct {
	Algorithm          string
	Context            string
	EphemeralPublicKey string
	SenderPublicKey    string
	RecipientKeyID     string
	NonceStart         string
}

type EncryptedNativeStreamDecryptRequest struct {
	Channel     channels.ChannelID
	Header      EncryptedNativeStreamHeader
	RecordIndex uint64
	Ciphertext  []byte
	Metadata    channels.VerifiedMetadata
}

type EncryptedNativeStreamDecryptor interface {
	DecryptNativeStream(EncryptedNativeStreamDecryptRequest) ([]byte, error)
}

type ChannelHandlerOptions struct {
	EncryptedStreams EncryptedNativeStreamDecryptor
	KeyEnvelopes     PrivateChannelKeyEnvelopeProvider
	AccessAudit      ChannelAccessAuditSink
	// ActivityRing is the node's shared node_activity_read event ring (M2
	// activity capability, caps/nodeactivity.go). Optional: a nil value
	// (the zero value, so every existing construction compiles unchanged)
	// simply means issueGrant's activity-ring tap is a no-op.
	ActivityRing *caps.ActivityRing
}

// datasetPublicationPNMSchemaName is the store schema the dataset-publication
// lane writes its $PNM envelopes under (dataset_publication.go:686). The head
// route reads them back by CID.
const datasetPublicationPNMSchemaName = "PNM.fbs"

const (
	ChannelAuditStreamSubscribe = "stream_subscribe"
	ChannelAuditStreamOpen      = "stream_open"
	ChannelAuditStreamAccess    = "stream_access"
)

type ChannelAccessAuditEvent struct {
	EventType       string                  `json:"event_type"`
	ChannelID       string                  `json:"channel_id"`
	StandardCode    string                  `json:"standard_code"`
	ProviderPeerID  string                  `json:"provider_peer_id,omitempty"`
	Boundary        channels.AccessBoundary `json:"boundary"`
	Subject         string                  `json:"subject,omitempty"`
	GrantID         string                  `json:"grant_id,omitempty"`
	Allowed         bool                    `json:"allowed"`
	GrantState      string                  `json:"grant_state"`
	Reason          string                  `json:"reason,omitempty"`
	Visibility      string                  `json:"visibility,omitempty"`
	EncryptionState string                  `json:"encryption_state,omitempty"`
	PolicyID        string                  `json:"policy_id,omitempty"`
}

type ChannelAccessAuditSink interface {
	RecordChannelAccessAudit(ChannelAccessAuditEvent)
}

type ChannelAccessAuditFunc func(ChannelAccessAuditEvent)

func (f ChannelAccessAuditFunc) RecordChannelAccessAudit(event ChannelAccessAuditEvent) {
	f(event)
}

type PrivateChannelKeyEnvelopeRequest struct {
	Channel        channels.ChannelID
	Subject        string
	GrantID        string
	ContentKeyID   string
	RecipientKeyID string
	Metadata       channels.VerifiedMetadata
}

type PrivateChannelKeyEnvelope struct {
	ContentKeyID       string
	RecipientKeyID     string
	KeyEpoch           string
	Algorithm          string
	WrappedKeyEnvelope []byte
	EnvelopeCID        string
}

type PrivateChannelKeyEnvelopeProvider interface {
	GetPrivateChannelKeyEnvelope(PrivateChannelKeyEnvelopeRequest) (PrivateChannelKeyEnvelope, error)
}

func NewChannelHandler(store *storage.FlatSQLStore) *ChannelHandler {
	return NewChannelHandlerWithOptions(store, ChannelHandlerOptions{})
}

func NewChannelHandlerWithOptions(store *storage.FlatSQLStore, options ChannelHandlerOptions) *ChannelHandler {
	grants := channels.NewChannelGrantRegistry()
	return &ChannelHandler{
		store:            store,
		gate:             channels.NewAccessGate(grants),
		grants:           grants,
		metadata:         channels.NewVerifiedMetadataRegistry(),
		streams:          channels.NewNativeStreamRegistry(),
		subscriptions:    channels.NewSubscriptionRegistry(),
		encryptedStreams: options.EncryptedStreams,
		keyEnvelopes:     options.KeyEnvelopes,
		accessAudit:      options.AccessAudit,
		encryptedIndexes: make(map[string]struct{}),
		activityRing:     options.ActivityRing,
	}
}

func (h *ChannelHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/channels", h.handleCollection)
	mux.HandleFunc("/api/v1/channels/", h.handleChannel)
}

func (h *ChannelHandler) RecordDatasetPublicationChannelUpdate(update DatasetPublicationChannelUpdate) error {
	if h == nil {
		return fmt.Errorf("channel handler is unavailable")
	}
	standardCode, err := channels.StandardCodeFromSchemaName(update.Schema)
	if err != nil {
		return err
	}
	channelID, err := channels.FormatChannelID(channels.ChannelIDInput{
		SourceID:     update.SourceID,
		StandardCode: standardCode,
	})
	if err != nil {
		return err
	}
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
	}
	pnmEvidence, err := channels.VerifySignedPNMEnvelopeWithProviderKey(update.PNMBytes, update.ProviderPublicKey)
	if err != nil {
		return fmt.Errorf("record dataset publication channel: verified PNM required: %w", err)
	}
	dpmEvidence, err := channels.VerifySignedDPMManifestWithProviderKey(update.ManifestBytes, pnmEvidence.FileID, update.ProviderPublicKey)
	if err != nil {
		return fmt.Errorf("record dataset publication channel: verified DPM required: %w", err)
	}
	if pnmEvidence.CID != "" && dpmEvidence.ManifestCID != "" && pnmEvidence.CID != dpmEvidence.ManifestCID {
		return fmt.Errorf("record dataset publication channel: DPM CID %q does not match PNM CID %q", dpmEvidence.ManifestCID, pnmEvidence.CID)
	}
	if pnmEvidence.FileID != "" && dpmEvidence.FileID != "" && pnmEvidence.FileID != dpmEvidence.FileID {
		return fmt.Errorf("record dataset publication channel: DPM FILE_ID %q does not match PNM FILE_ID %q", dpmEvidence.FileID, pnmEvidence.FileID)
	}
	h.metadata.RecordPNM(parsed, pnmEvidence)
	if _, ok := h.metadata.RecordDPM(parsed, dpmEvidence); !ok {
		return fmt.Errorf("record dataset publication channel: verified PNM was not recorded")
	}
	if _, ok := h.metadata.RecordDatasetPublication(parsed, update.PublishedShard.FeedHead, update.PublishedShard.RecordCount, update.PublishedShard.ByteCount); !ok {
		return fmt.Errorf("record dataset publication channel: verified channel metadata was not recorded")
	}
	return nil
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
		h.handlePrivateCollection(w, r, standardFilter)
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
	seen := make(map[string]struct{})
	for _, metadata := range h.restoreVerifiedDatasetPublicationMetadataList(standardFilter) {
		parsed, err := channels.ParseChannelID(metadata.ChannelID)
		if err != nil {
			continue
		}
		row := h.channelDetail(parsed)
		row["topic"] = channels.DiscoveryTopic(parsed.StandardCode)
		seen[metadata.ChannelID] = struct{}{}
		results = append(results, row)
	}
	// Publication-derived channels. This is what makes the collection USABLE as
	// a discovery surface: a consumer that does not already know a channel id
	// cannot guess whether $RFB is published as satnogs-db-RFB or under the
	// relaying node's provider id. Sourced from sdn_dataset_shard_publications
	// (small, indexed) so it costs nothing like the ledger sweep above.
	for _, row := range h.datasetPublicationChannelRows(standardFilter) {
		channelID, _ := row["channelId"].(string)
		if _, ok := seen[channelID]; ok {
			continue
		}
		seen[channelID] = struct{}{}
		results = append(results, row)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":   len(results),
		"results": results,
	})
}

func (h *ChannelHandler) handlePrivateCollection(w http.ResponseWriter, r *http.Request, standardFilter string) {
	if standardFilter != "" {
		if _, err := channels.AssertStandardCode(standardFilter); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	visibility := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("visibility")))
	results := make([]map[string]interface{}, 0)
	for _, metadata := range h.metadata.List() {
		if !isPrivateChannelMetadata(metadata) {
			continue
		}
		if visibility != "" && !strings.EqualFold(metadata.Visibility, visibility) {
			continue
		}
		parsed, err := channels.ParseChannelID(metadata.ChannelID)
		if err != nil {
			continue
		}
		if standardFilter != "" && parsed.StandardCode != standardFilter {
			continue
		}
		decision := h.authorizeGrant(r, parsed, channels.BoundaryListPrivate)
		if !decision.Allowed {
			continue
		}
		row := h.channelDetail(parsed)
		row["grantState"] = decision.GrantState
		row["topic"] = channels.DiscoveryTopic(parsed.StandardCode)
		results = append(results, row)
	}
	if len(results) == 0 {
		h.writeAccessDenied(w, channels.AccessDecision{
			Allowed:    false,
			GrantState: "required",
			Reason:     "verified channel grant required",
		})
		return
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
		if h.requiresPrivateHiddenGrant(parsed) {
			decision := h.authorizeGrant(r, parsed, channels.BoundaryListPrivate)
			if !decision.Allowed {
				h.writeAccessDenied(w, decision)
				return
			}
			payload := h.channelDetail(parsed)
			payload["grantState"] = decision.GrantState
			writeJSON(w, http.StatusOK, payload)
			return
		}
		writeJSON(w, http.StatusOK, h.channelDetail(parsed))
	case "monitor":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if h.isPrivateVisibilityRequest(r) || h.requiresPrivateHiddenGrant(parsed) {
			decision := h.authorizeGrant(r, parsed, channels.BoundaryListPrivate)
			if !decision.Allowed {
				h.writeAccessDenied(w, decision)
				return
			}
			writeJSON(w, http.StatusOK, h.channelMonitorWithDecision(parsed, decision))
			return
		}
		writeJSON(w, http.StatusOK, h.channelMonitor(parsed))
	case "pnm":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.getPNM(w, r, parsed)
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
		h.unwrapPrivateChannelKey(w, r, parsed)
	case "shard-import":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.importVerifiedChannelShard(w, r, parsed)
	case "module-feed":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.deliverVerifiedChannelModuleFeed(w, r, parsed)
	case "cache":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.readLocalCache(w, r, parsed)
	case "grants":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if h.requiresPrivateGrant(r, parsed) {
			if h.requestMatchesVerifiedProvider(r, parsed) {
				h.issueGrant(w, r, parsed)
				return
			}
			decision := h.authorizeGrant(r, parsed, channels.BoundaryGrantIssue)
			if !decision.Allowed {
				h.writeAccessDenied(w, decision)
				return
			}
			h.issueGrant(w, r, parsed)
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

func (h *ChannelHandler) requireGrantUnavailable(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID, boundary channels.AccessBoundary, message string) {
	decision := h.authorizeGrant(r, parsed, boundary)
	if !decision.Allowed {
		h.writeAccessDenied(w, decision)
		return
	}
	writeError(w, http.StatusNotImplemented, message)
}

func (h *ChannelHandler) authorizeGrant(r *http.Request, parsed channels.ChannelID, boundary channels.AccessBoundary) channels.AccessDecision {
	gate := h.gate
	if gate == nil {
		gate = channels.NewAccessGate(nil)
	}
	req := channels.AccessRequest{
		Channel:  parsed,
		Boundary: boundary,
		Subject:  strings.TrimSpace(r.URL.Query().Get("subject")),
		GrantID:  strings.TrimSpace(r.URL.Query().Get("grantId")),
	}
	decision := gate.Authorize(req)
	h.recordAccessAudit(req, decision)
	return decision
}

func (h *ChannelHandler) recordAccessAudit(req channels.AccessRequest, decision channels.AccessDecision) {
	if h == nil || h.accessAudit == nil {
		return
	}
	event := ChannelAccessAuditEvent{
		EventType:    channelAuditEventType(req.Boundary),
		ChannelID:    req.Channel.ChannelID,
		StandardCode: req.Channel.StandardCode,
		Boundary:     req.Boundary,
		Subject:      strings.TrimSpace(req.Subject),
		GrantID:      strings.TrimSpace(req.GrantID),
		Allowed:      decision.Allowed,
		GrantState:   decision.GrantState,
		Reason:       decision.Reason,
	}
	if metadata, ok := h.metadata.Get(req.Channel); ok {
		event.ProviderPeerID = metadata.ProviderPeer
		event.Visibility = metadata.Visibility
		event.EncryptionState = metadata.EncryptionState
		event.PolicyID = metadata.EncryptionPolicy
	}
	h.accessAudit.RecordChannelAccessAudit(event)
}

func channelAuditEventType(boundary channels.AccessBoundary) string {
	switch boundary {
	case channels.BoundarySubscribe:
		return ChannelAuditStreamSubscribe
	case channels.BoundaryStreamOpen:
		return ChannelAuditStreamOpen
	default:
		return ChannelAuditStreamAccess
	}
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
	h.tapGrantIssued(parsed.ChannelID, grant.Subject)
	writeJSON(w, http.StatusCreated, channelGrantResponse(grant))
}

// tapGrantIssued taps the node_activity_read activity ring (M2 activity
// capability, caps/nodeactivity.go) after a channel grant is successfully
// issued. h.activityRing is optional (see ChannelHandlerOptions) and
// ActivityRing.Append is itself nil-safe, so this is always safe to call
// unconditionally.
//
// The grant subject is a peer ID or an operator-chosen alias — not always
// one or the other — so: a value that decodes as a libp2p peer ID goes in
// peer_id (letting the dashboard cross-reference it against other
// peer-scoped events); anything else is folded into detail instead of
// being silently dropped.
func (h *ChannelHandler) tapGrantIssued(channelID, subject string) {
	if _, err := peer.Decode(subject); err == nil {
		h.activityRing.Append("grant_issued", subject, channelID)
		return
	}
	h.activityRing.Append("grant_issued", "", channelID+" subject="+subject)
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

func (h *ChannelHandler) getPNM(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID) {
	if h.requiresPrivateHiddenGrant(parsed) {
		decision := h.authorizeGrant(r, parsed, channels.BoundaryListPrivate)
		if !decision.Allowed {
			h.writeAccessDenied(w, decision)
			return
		}
	}
	metadata, verified := h.metadata.Get(parsed)
	if !verified || len(metadata.PNMBytes) == 0 {
		// The in-memory registry only carries PNM bytes for publications this
		// PROCESS witnessed. A restarted node — or a consumer node that only
		// ever replicated somebody else's publication — has none, which is why
		// this route answered 404 for every published channel on prod. Fall
		// back to the node's own durable publication record. Two indexed reads,
		// no store scan: see restoreVerifiedPNMFromDatasetPublication.
		restored, ok := h.restoreVerifiedPNMFromDatasetPublication(parsed)
		if !ok {
			writeError(w, http.StatusNotFound, "verified PNM unavailable for channel")
			return
		}
		metadata = restored
	}
	w.Header().Set("Content-Type", "application/vnd.sdn.pnm")
	// The head token is content-addressed: the same PNM CID always means the
	// same bytes, so a consumer that already holds this head can skip the body.
	if cid := strings.TrimSpace(metadata.PNMCID); cid != "" {
		w.Header().Set("ETag", `"`+cid+`"`)
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(metadata.PNMBytes)
}

// restoreVerifiedPNMFromDatasetPublication rebuilds a channel's verified $PNM
// head from durable state when the in-memory registry has none.
//
// Cost contract (this route is a browser catch-up token — it must be fast and
// must never scan the record store):
//
//  1. one indexed lookup in sdn_dataset_shard_publications, a table with one
//     row per published window (28 rows on prod), for the newest publication
//     of this channel that carries a PNM CID;
//  2. one record read BY CID for the $PNM itself (520 B on prod).
//
// It deliberately does NOT use restoreVerifiedDatasetPublicationMetadata:
// that path goes through LocalReplicaStats, which counts every raw record in
// the schema — the same cost that makes /api/v1/channels hang.
//
// Trust: the bytes are only served when the $PNM envelope verifies
// structurally AND its CID equals the manifest CID recorded in the node's own
// publication row. That row is written either by this node's own publication
// lane or by materializeDatasetFeedHeadAnnouncement, which admits publications
// only from peers that pass peerRegistry.IsTrusted. Because the PNM is fetched
// BY the CID that row names, no substitution is possible without breaking the
// content address. Full provider-signature verification stays where the key
// lives (the materialization lane, node.go:3857); this route re-checks the
// binding, it does not invent a new trust anchor.
func (h *ChannelHandler) restoreVerifiedPNMFromDatasetPublication(parsed channels.ChannelID) (channels.VerifiedMetadata, bool) {
	if h == nil || h.store == nil {
		return channels.VerifiedMetadata{}, false
	}
	publication, ok := h.latestDatasetPublicationForChannel(parsed)
	if !ok || strings.TrimSpace(publication.PNMCID) == "" {
		return channels.VerifiedMetadata{}, false
	}
	record, err := h.store.GetRecord(datasetPublicationPNMSchemaName, publication.PNMCID)
	if err != nil || record == nil || len(record.Data) == 0 {
		return channels.VerifiedMetadata{}, false
	}
	evidence, err := channels.VerifySignedPNMEnvelope(record.Data)
	if err != nil {
		return channels.VerifiedMetadata{}, false
	}
	// Bind the envelope to the publication the node itself recorded. A $PNM
	// whose CID does not name that manifest is not this channel's head.
	if manifestCID := strings.TrimSpace(publication.ManifestCID); manifestCID != "" &&
		manifestCID != strings.TrimSpace(evidence.CID) {
		return channels.VerifiedMetadata{}, false
	}
	verifiedAt := publication.PublishedAt
	if verifiedAt.IsZero() {
		verifiedAt = record.Timestamp
	}
	return h.metadata.RecordRestored(channels.VerifiedMetadata{
		ChannelID:       parsed.ChannelID,
		ChannelHead:     strings.TrimSpace(publication.FeedHead),
		PNMCID:          strings.TrimSpace(evidence.CID),
		PNMBytes:        record.Data,
		PNMFileID:       evidence.FileID,
		SignatureType:   evidence.SignatureType,
		VerifiedAt:      verifiedAt,
		ProviderPeer:    strings.TrimSpace(record.PeerID),
		Visibility:      "public",
		EncryptionState: "none",
		LocalRows:       publication.RecordCount,
		RemoteRows:      publication.RecordCount,
		SyncedRows:      publication.RecordCount,
		PinnedRows:      publication.RecordCount,
		PinnedBytes:     publication.ByteCount,
		SyncedBytes:     publication.ByteCount,
	}), true
}

// latestDatasetPublicationForChannel returns the newest publication whose
// source identity addresses this channel.
//
// A channel id is "<sourceID>-<STANDARD>". On the node that PUBLISHES, the
// source id is the lane's source_name (satnogs-db-RFB). On a node that
// REPLICATED that publication, the row also carries the relaying node's
// provider_id, and datasetPublicationSourceID prefers provider_id — so the
// same data would be addressed as space-data-network-02-RFB there. Both name
// the same channel, so both resolve here. Consumers (and the $RFB gallery
// cache) address channels by the DATA source, which is the stable half.
func (h *ChannelHandler) latestDatasetPublicationForChannel(parsed channels.ChannelID) (storage.DatasetShardPublication, bool) {
	if h == nil || h.store == nil {
		return storage.DatasetShardPublication{}, false
	}
	schemaName, err := channels.SchemaNameFromStandardCode(parsed.StandardCode)
	if err != nil {
		return storage.DatasetShardPublication{}, false
	}
	publications, err := h.store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   schemaName,
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		return storage.DatasetShardPublication{}, false
	}
	var newest storage.DatasetShardPublication
	found := false
	for _, publication := range publications {
		if !datasetPublicationAddressesChannelSource(publication, parsed.SourceID) {
			continue
		}
		if strings.TrimSpace(publication.PNMCID) == "" {
			continue
		}
		if !found || publication.PublishedAt.After(newest.PublishedAt) {
			newest = publication
			found = true
		}
	}
	return newest, found
}

// datasetPublicationChannelRows lists every channel this node can serve a head
// for, derived from sdn_dataset_shard_publications alone.
//
// This is the discovery half of the head lane: without it a consumer must
// GUESS the channel id (the $RFB gallery guessed satnogs-db-RFB, satnogs-RFB
// and celestrak-RFB in turn).
//
// ⛔ COST CONTRACT: exactly ONE store read, whatever the filter.
//
// It used to be one indexed read PER SUPPORTED SCHEMA, and the comment called
// that free because the table is small. The table is small — 28 rows on prod —
// but the loop is 206 schemas long, and the FlatSQL engine is a single-threaded
// runtime every store call serializes on. Under live load one such call was
// measured on host-01 at 0.5–23 s, so 206 of them is the reason the unfiltered
// route exceeded 240 s. Row COUNT was never the cost; ROUND TRIPS were. Do not
// reintroduce a per-schema loop here.
func (h *ChannelHandler) datasetPublicationChannelRows(standardFilter string) []map[string]interface{} {
	if h == nil || h.store == nil {
		return nil
	}
	standardFilter = strings.TrimSpace(standardFilter)
	schemaFilter := ""
	if standardFilter != "" {
		schemaName, err := channels.SchemaNameFromStandardCode(standardFilter)
		if err != nil {
			return nil
		}
		schemaFilter = schemaName
	}
	publications, err := h.store.ListDatasetShardPublicationsForProfile(storage.DatasetPublicationQueryProfile, schemaFilter)
	if err != nil {
		return nil
	}
	rows := make([]map[string]interface{}, 0)
	seen := make(map[string]struct{})
	for _, publication := range publications {
		// StandardCodeFromSchemaName is also the supported-schema filter: a
		// publication row for a schema this build does not know is skipped,
		// exactly as the per-schema loop skipped it.
		standardCode, err := channels.StandardCodeFromSchemaName(publication.SchemaName)
		if err != nil {
			continue
		}
		sourceID := datasetPublicationSourceID(publication.ProviderID, publication.SourceName)
		for _, candidate := range []string{sourceID, strings.TrimSpace(publication.SourceName)} {
			if candidate == "" {
				continue
			}
			channelID, err := channels.FormatChannelID(channels.ChannelIDInput{
				SourceID:     candidate,
				StandardCode: standardCode,
			})
			if err != nil {
				continue
			}
			if _, ok := seen[channelID]; ok {
				continue
			}
			parsed, err := channels.ParseChannelID(channelID)
			if err != nil {
				continue
			}
			seen[channelID] = struct{}{}
			// ⛔ DO NOT call channelDetail here. It resolves verified
			// metadata, which now falls back to
			// restoreVerifiedPNMFromDatasetPublication — a record read BY
			// CID, PER ROW. Prod reads a record by CID in 12-29 s today
			// (sdn-record-by-cid-read-12-to-29-seconds), so listing ~9
			// channels that way would reintroduce this route's hang from a
			// different cause. A LIST does not need $PNM bytes: every field
			// below comes from the publication row and the subscription
			// registry, both already in hand.
			state := h.subscriptions.Get(parsed)
			rows = append(rows, map[string]interface{}{
				"channelId":       parsed.ChannelID,
				"sourceId":        parsed.SourceID,
				"standardCode":    parsed.StandardCode,
				"feedUuid":        emptyStringAsNil(parsed.FeedUUID),
				"topic":           channels.DiscoveryTopic(standardCode),
				"visibility":      "public",
				"subscribed":      state.Subscribed,
				"grantState":      "not-required",
				"encryptionState": "none",
				"channelHead":     emptyStringAsNil(strings.TrimSpace(publication.FeedHead)),
				// headAvailable says GET /pnm will answer for this channel,
				// established without paying to fetch the head.
				"headAvailable": strings.TrimSpace(publication.PNMCID) != "",
				"recordCount":   publication.RecordCount,
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		left, _ := rows[i]["channelId"].(string)
		right, _ := rows[j]["channelId"].(string)
		return left < right
	})
	return rows
}

// datasetPublicationAddressesChannelSource reports whether a publication row
// belongs to the channel addressed by sourceID. The canonical id is the one
// datasetPublicationSourceID derives; the data source_name is accepted as an
// equal alias so a replicated publication stays addressable by the source that
// produced it rather than by whichever node relayed it.
func datasetPublicationAddressesChannelSource(publication storage.DatasetShardPublication, sourceID string) bool {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return false
	}
	if datasetPublicationSourceID(publication.ProviderID, publication.SourceName) == sourceID {
		return true
	}
	return strings.TrimSpace(publication.SourceName) == sourceID
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
	evidence, err := channels.VerifySignedDPMManifestWithProviderKey(body, metadata.PNMFileID, providerPublicKey)
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
	metadata, ok := h.metadata.RecordDPM(parsed, evidence)
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
	replayKey := ""
	replayReserved := false
	replayCommitted := false
	defer func() {
		if replayReserved && !replayCommitted {
			h.releaseEncryptedNativeStreamRecordIndex(replayKey)
		}
	}()
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
	timings := channelThroughputTimings{
		PNMDPMVerification: time.Since(pnmDPMStarted),
	}
	transferStarted := time.Now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read native FlatBuffer stream: "+err.Error())
		return
	}
	timings.Transfer = time.Since(transferStarted)
	if isPrivateChannelMetadata(metadata) {
		if !h.isEncryptedNativeStreamPublish(r) {
			writeError(w, http.StatusBadRequest, "encrypted private channel stream required")
			return
		}
		header, err := h.parseEncryptedNativeStreamHeader(r, parsed)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		recordIndex, err := h.parseEncryptedNativeStreamRecordIndex(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if h.encryptedStreams == nil {
			writeError(w, http.StatusNotImplemented, "encrypted private channel stream decrypt path unavailable")
			return
		}
		decryptStarted := time.Now()
		body, err = h.encryptedStreams.DecryptNativeStream(EncryptedNativeStreamDecryptRequest{
			Channel:     parsed,
			Header:      header,
			RecordIndex: recordIndex,
			Ciphertext:  body,
			Metadata:    metadata,
		})
		timings.Decrypt = time.Since(decryptStarted)
		if err != nil {
			writeError(w, http.StatusBadRequest, "decrypt encrypted private channel stream: "+err.Error())
			return
		}
		replayKey = encryptedNativeStreamReplayKey(parsed, metadata, header, recordIndex)
		if !h.reserveEncryptedNativeStreamRecordIndex(replayKey) {
			writeError(w, http.StatusConflict, "encrypted private channel stream record index replayed")
			return
		}
		replayReserved = true
	}
	frames, err := channels.SplitNativeStreamFramesForChannel(parsed, body)
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
	gate := evaluateChannelWireSpeedGate(throughputBPS, wireUtilization, timings)
	if gate.Enabled && !gate.TargetMet {
		writeJSON(w, http.StatusTooManyRequests, gate.Response(parsed, len(body), len(frames)))
		return
	}
	importedRows := 0
	if h.store != nil {
		importStarted := time.Now()
		tags := channelSourceTags(parsed, metadata)
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
	timingsMs := timings.AsMilliseconds()
	metadata, _ = h.metadata.RecordNativeStream(parsed, snapshot, throughputBPS, wireUtilization, timingsMs)
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
		"timingsMs":                timingsMs,
	}
	if gate.Enabled {
		response["wireSpeedTarget"] = gate.Target
		response["requiredBytesPerSecond"] = gate.RequiredBytesPerSecond
		response["targetMet"] = gate.TargetMet
	}
	replayCommitted = true
	writeJSON(w, http.StatusAccepted, response)
}

func encryptedNativeStreamReplayKey(parsed channels.ChannelID, metadata channels.VerifiedMetadata, header EncryptedNativeStreamHeader, recordIndex uint64) string {
	parts := []string{
		parsed.ChannelID,
		strings.TrimSpace(metadata.PNMCID),
		strings.TrimSpace(metadata.ContentKeyID),
		strings.TrimSpace(header.Context),
		strings.ToLower(strings.TrimSpace(header.SenderPublicKey)),
		strconv.FormatUint(recordIndex, 10),
	}
	return strings.Join(parts, "\x00")
}

func (h *ChannelHandler) reserveEncryptedNativeStreamRecordIndex(key string) bool {
	if h == nil {
		return false
	}
	h.replayMu.Lock()
	defer h.replayMu.Unlock()
	if h.encryptedIndexes == nil {
		h.encryptedIndexes = make(map[string]struct{})
	}
	if _, ok := h.encryptedIndexes[key]; ok {
		return false
	}
	h.encryptedIndexes[key] = struct{}{}
	return true
}

func (h *ChannelHandler) releaseEncryptedNativeStreamRecordIndex(key string) {
	if h == nil {
		return
	}
	h.replayMu.Lock()
	delete(h.encryptedIndexes, key)
	h.replayMu.Unlock()
}

func channelSourceTags(parsed channels.ChannelID, metadata channels.VerifiedMetadata) storage.SourceTags {
	contentKeyID := strings.TrimSpace(metadata.ContentKeyID)
	if contentKeyID == "" {
		contentKeyID = "public"
	}
	producerPeerID := strings.TrimSpace(metadata.ProviderPeer)
	if producerPeerID == "" {
		producerPeerID = parsed.SourceID
	}
	producerPublicKey := strings.TrimSpace(metadata.ProviderPublicKey)
	if producerPublicKey == "" {
		producerPublicKey = parsed.SourceID
	}
	return storage.SourceTags{
		ProviderID:        parsed.SourceID,
		SourceName:        "channel:" + parsed.ChannelID,
		BatchID:           parsed.ChannelID,
		ContentKeyID:      contentKeyID,
		ProducerPeerID:    producerPeerID,
		ProducerPublicKey: producerPublicKey,
	}
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
	if utilization > 1 {
		utilization = 1
	}
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

const channelWireSpeedTarget = 0.99

func evaluateChannelWireSpeedGate(throughputBPS int64, wireUtilization *float64, timings channelThroughputTimings) channelWireSpeedGate {
	gate := channelWireSpeedGate{
		Target:                channelWireSpeedTarget,
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
	snapshot, ok := h.verifiedNativeStreamSnapshot(parsed)
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
	snapshot, ok := h.verifiedNativeStreamSnapshot(parsed)
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

func (h *ChannelHandler) importVerifiedChannelShard(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID) {
	decision := h.authorizeGrant(r, parsed, channels.BoundaryShardImport)
	if !decision.Allowed {
		h.writeAccessDenied(w, decision)
		return
	}
	if h.store == nil {
		writeError(w, http.StatusNotImplemented, "durable FlatSQL store is unavailable")
		return
	}
	metadata, verified := h.metadata.Get(parsed)
	if !verified || metadata.DPMVerifiedAt.IsZero() || !isPrivateChannelMetadata(metadata) {
		writeError(w, http.StatusNotFound, "verified private encrypted channel metadata unavailable")
		return
	}
	snapshot, ok := h.verifiedNativeStreamSnapshot(parsed)
	if !ok {
		writeError(w, http.StatusNotFound, "verified native FlatBuffer stream unavailable for channel")
		return
	}
	frames, err := channels.SplitNativeStreamFramesForChannel(parsed, snapshot.Bytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid native FlatBuffer stream: "+err.Error())
		return
	}
	schemaName, err := channels.SchemaNameFromStandardCode(parsed.StandardCode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tags := channelSourceTags(parsed, metadata)
	importedRows, err := h.store.StoreBatchWithSourceTags(schemaName, frames, "channel:"+parsed.SourceID, nil, tags)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "durable FlatSQL import failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"channelId":    parsed.ChannelID,
		"standardCode": parsed.StandardCode,
		"grantState":   decision.GrantState,
		"streamBytes":  snapshot.ByteCount,
		"streamFrames": snapshot.FrameCount,
		"importedRows": importedRows,
		"importedAt":   time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (h *ChannelHandler) deliverVerifiedChannelModuleFeed(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID) {
	decision := h.authorizeGrant(r, parsed, channels.BoundaryModuleFeedDelivery)
	if !decision.Allowed {
		h.writeAccessDenied(w, decision)
		return
	}
	metadata, verified := h.metadata.Get(parsed)
	if !verified || metadata.DPMVerifiedAt.IsZero() || !isPrivateChannelMetadata(metadata) {
		writeError(w, http.StatusNotFound, "verified private encrypted channel metadata unavailable")
		return
	}
	snapshot, ok := h.verifiedNativeStreamSnapshot(parsed)
	if !ok {
		writeError(w, http.StatusNotFound, "verified native FlatBuffer stream unavailable for channel")
		return
	}
	if _, err := channels.SplitNativeStreamFramesForChannel(parsed, snapshot.Bytes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid native FlatBuffer stream: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	w.Header().Set("X-SDN-Grant-State", decision.GrantState)
	w.Header().Set("X-SDN-Stream-Frames", strconv.Itoa(snapshot.FrameCount))
	w.Header().Set("X-SDN-Stream-Bytes", strconv.Itoa(snapshot.ByteCount))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(snapshot.Bytes)
}

func (h *ChannelHandler) readLocalCache(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID) {
	if h.requiresLocalCacheReadGrant(r, parsed) {
		decision := h.authorizeGrant(r, parsed, channels.BoundaryLocalCacheRead)
		if !decision.Allowed {
			h.writeAccessDenied(w, decision)
			return
		}
	}
	snapshot, ok := h.verifiedNativeStreamSnapshot(parsed)
	if !ok {
		writeError(w, http.StatusNotFound, "verified native FlatBuffer stream unavailable for channel")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(snapshot.Bytes)
}

type privateChannelKeyUnwrapPayload struct {
	ContentKeyID   string `json:"contentKeyId"`
	RecipientKeyID string `json:"recipientKeyId"`
}

func (h *ChannelHandler) unwrapPrivateChannelKey(w http.ResponseWriter, r *http.Request, parsed channels.ChannelID) {
	decision := h.authorizeGrant(r, parsed, channels.BoundaryKeyUnwrap)
	if !decision.Allowed {
		h.writeAccessDenied(w, decision)
		return
	}
	metadata, verified := h.metadata.Get(parsed)
	if !verified || metadata.DPMVerifiedAt.IsZero() || !isPrivateChannelMetadata(metadata) {
		writeError(w, http.StatusNotFound, "verified private encrypted channel metadata unavailable")
		return
	}
	if h.keyEnvelopes == nil {
		writeError(w, http.StatusNotImplemented, "private channel envelope provider is unavailable")
		return
	}
	var payload privateChannelKeyUnwrapPayload
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&payload); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid key unwrap payload: "+err.Error())
			return
		}
	}
	subject := strings.TrimSpace(r.URL.Query().Get("subject"))
	grantID := strings.TrimSpace(r.URL.Query().Get("grantId"))
	contentKeyID := strings.TrimSpace(payload.ContentKeyID)
	if contentKeyID == "" {
		contentKeyID = strings.TrimSpace(metadata.ContentKeyID)
	}
	if contentKeyID == "" || contentKeyID != strings.TrimSpace(metadata.ContentKeyID) {
		writeError(w, http.StatusForbidden, "verified channel grant does not match requested content key")
		return
	}
	recipientKeyID := strings.TrimSpace(payload.RecipientKeyID)
	if recipientKeyID == "" {
		writeError(w, http.StatusBadRequest, "recipientKeyId is required")
		return
	}
	envelope, err := h.keyEnvelopes.GetPrivateChannelKeyEnvelope(PrivateChannelKeyEnvelopeRequest{
		Channel:        parsed,
		Subject:        subject,
		GrantID:        grantID,
		ContentKeyID:   contentKeyID,
		RecipientKeyID: recipientKeyID,
		Metadata:       metadata,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "wrapped key envelope unavailable for requester")
		return
	}
	if strings.TrimSpace(envelope.ContentKeyID) != contentKeyID || strings.TrimSpace(envelope.RecipientKeyID) != recipientKeyID {
		writeError(w, http.StatusForbidden, "wrapped key envelope does not match verified request")
		return
	}
	if len(envelope.WrappedKeyEnvelope) == 0 && strings.TrimSpace(envelope.EnvelopeCID) == "" {
		writeError(w, http.StatusNotFound, "wrapped key envelope unavailable for requester")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"channelId":                parsed.ChannelID,
		"standardCode":             parsed.StandardCode,
		"grantState":               decision.GrantState,
		"contentKeyId":             contentKeyID,
		"recipientKeyId":           recipientKeyID,
		"keyEpoch":                 strings.TrimSpace(envelope.KeyEpoch),
		"algorithm":                strings.TrimSpace(envelope.Algorithm),
		"envelopeCid":              strings.TrimSpace(envelope.EnvelopeCID),
		"wrappedKeyEnvelopeBase64": base64.StdEncoding.EncodeToString(envelope.WrappedKeyEnvelope),
	})
}

func (h *ChannelHandler) requiresLocalCacheReadGrant(r *http.Request, parsed channels.ChannelID) bool {
	if h.requiresPrivateGrant(r, parsed) {
		return true
	}
	metadata, verified := h.metadata.Get(parsed)
	if !verified {
		return true
	}
	return isPrivateChannelMetadata(metadata)
}

func (h *ChannelHandler) verifiedNativeStreamSnapshot(parsed channels.ChannelID) (channels.NativeStreamSnapshot, bool) {
	if h == nil {
		return channels.NativeStreamSnapshot{}, false
	}
	if snapshot, ok := h.streams.Get(parsed); ok {
		return snapshot, true
	}
	return h.restoreVerifiedNativeStreamSnapshot(parsed)
}

func (h *ChannelHandler) restoreVerifiedNativeStreamSnapshot(parsed channels.ChannelID) (channels.NativeStreamSnapshot, bool) {
	if h == nil || h.store == nil {
		return channels.NativeStreamSnapshot{}, false
	}
	metadata, verified := h.verifiedChannelMetadata(parsed)
	if !verified || metadata.DPMVerifiedAt.IsZero() {
		return channels.NativeStreamSnapshot{}, false
	}
	schemaName, err := channels.SchemaNameFromStandardCode(parsed.StandardCode)
	if err != nil {
		return channels.NativeStreamSnapshot{}, false
	}
	stats, err := h.store.LocalReplicaStats(storage.LocalReplicaStatsQuery{
		SchemaName:   schemaName,
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		return channels.NativeStreamSnapshot{}, false
	}
	for _, stat := range stats {
		if datasetPublicationSourceID(stat.ProviderID, stat.SourceName) != parsed.SourceID {
			continue
		}
		publication, ok := h.latestVerifiedDatasetShardPublication(schemaName, stat, metadata.ChannelHead)
		if !ok {
			continue
		}
		snapshot, ok := h.restoreNativeStreamSnapshotFromPublication(parsed, publication)
		if !ok {
			continue
		}
		h.metadata.RecordNativeStream(parsed, snapshot, metadata.ThroughputBPS, metadata.WireUtilization, metadata.TimingsMs)
		return snapshot, true
	}
	return channels.NativeStreamSnapshot{}, false
}

func (h *ChannelHandler) latestVerifiedDatasetShardPublication(schemaName string, stat storage.LocalReplicaStats, channelHead string) (storage.DatasetShardPublication, bool) {
	if h == nil || h.store == nil {
		return storage.DatasetShardPublication{}, false
	}
	publications, err := h.store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   schemaName,
		ProviderID:   stat.ProviderID,
		SourceName:   stat.SourceName,
		BatchID:      stat.BatchID,
		QueryProfile: stat.QueryProfile,
		RecordCount:  int(stat.PinnedRows),
	})
	if err != nil || len(publications) == 0 {
		return storage.DatasetShardPublication{}, false
	}
	channelHead = strings.TrimSpace(channelHead)
	for i := len(publications) - 1; i >= 0; i-- {
		publication := publications[i]
		if channelHead != "" && strings.TrimSpace(publication.FeedHead) != channelHead {
			continue
		}
		return publication, true
	}
	if channelHead != "" {
		return storage.DatasetShardPublication{}, false
	}
	return publications[len(publications)-1], true
}

func (h *ChannelHandler) restoreNativeStreamSnapshotFromPublication(parsed channels.ChannelID, publication storage.DatasetShardPublication) (channels.NativeStreamSnapshot, bool) {
	if h == nil || h.store == nil {
		return channels.NativeStreamSnapshot{}, false
	}
	shardPath, err := h.store.DatasetPublicationShardPath(publication)
	if err != nil {
		return channels.NativeStreamSnapshot{}, false
	}
	shardBytes, err := os.ReadFile(shardPath)
	if err != nil || len(shardBytes) == 0 {
		return channels.NativeStreamSnapshot{}, false
	}
	if expected := strings.TrimSpace(publication.ShardSHA256); expected != "" {
		actual := sha256.Sum256(shardBytes)
		if hex.EncodeToString(actual[:]) != expected {
			return channels.NativeStreamSnapshot{}, false
		}
	}
	snapshot, err := h.streams.Store(parsed, shardBytes)
	if err != nil {
		return channels.NativeStreamSnapshot{}, false
	}
	return snapshot, true
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
		string(channels.BoundaryListPrivate):        channels.BoundaryListPrivate,
		string(channels.BoundarySubscribe):          channels.BoundarySubscribe,
		string(channels.BoundaryUnsubscribe):        channels.BoundaryUnsubscribe,
		string(channels.BoundaryPublish):            channels.BoundaryPublish,
		string(channels.BoundaryGrantIssue):         channels.BoundaryGrantIssue,
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

func (h *ChannelHandler) requiresPrivateHiddenGrant(parsed channels.ChannelID) bool {
	metadata, ok := h.metadata.Get(parsed)
	return ok && strings.EqualFold(metadata.Visibility, "private-hidden")
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

func (h *ChannelHandler) isEncryptedNativeStreamPublish(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-SDN-Encrypted-Stream")), "true") ||
		strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("encrypted")), "1") ||
		strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("encrypted")), "true") {
		return true
	}
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	return strings.HasPrefix(contentType, "application/vnd.sdn.flatbuffers.encrypted-stream")
}

func (h *ChannelHandler) validateEncryptedNativeStreamHeader(r *http.Request, parsed channels.ChannelID) error {
	_, err := h.parseEncryptedNativeStreamHeader(r, parsed)
	return err
}

func (h *ChannelHandler) parseEncryptedNativeStreamHeader(r *http.Request, parsed channels.ChannelID) (EncryptedNativeStreamHeader, error) {
	raw := strings.TrimSpace(r.Header.Get("X-SDN-Encrypted-Stream-Header"))
	if raw == "" {
		return EncryptedNativeStreamHeader{}, fmt.Errorf("encrypted private channel stream header required")
	}
	var header map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &header); err != nil {
		return EncryptedNativeStreamHeader{}, fmt.Errorf("encrypted private channel stream header must be JSON metadata: %w", err)
	}
	algorithm, ok := encryptedNativeStreamHeaderString(header, "algorithm")
	if !ok {
		return EncryptedNativeStreamHeader{}, fmt.Errorf("encrypted private channel stream header missing algorithm")
	}
	context, ok := encryptedNativeStreamHeaderString(header, "context")
	if !ok {
		return EncryptedNativeStreamHeader{}, fmt.Errorf("encrypted private channel stream header missing context")
	}
	senderPublicKey, ok := encryptedNativeStreamHeaderBytes(header, "senderPublicKey", "ephemeralPublicKey", "ephemeral_public_key")
	if !ok {
		return EncryptedNativeStreamHeader{}, fmt.Errorf("encrypted private channel stream header missing senderPublicKey")
	}
	nonceStart, ok := encryptedNativeStreamHeaderBytes(header, "nonceStart", "nonce_start")
	if !ok {
		return EncryptedNativeStreamHeader{}, fmt.Errorf("encrypted private channel stream header missing nonceStart")
	}
	parsedHeader := EncryptedNativeStreamHeader{
		Algorithm:          algorithm,
		Context:            context,
		EphemeralPublicKey: senderPublicKey,
		SenderPublicKey:    senderPublicKey,
		NonceStart:         nonceStart,
	}
	if recipientKeyID, ok := encryptedNativeStreamHeaderBytes(header, "recipientKeyId", "recipient_key_id"); ok {
		parsedHeader.RecipientKeyID = recipientKeyID
	}
	if err := validateEncryptedNativeStreamHeaderFields(parsedHeader); err != nil {
		return EncryptedNativeStreamHeader{}, err
	}
	if parsedHeader.Context != parsed.ChannelID {
		return EncryptedNativeStreamHeader{}, fmt.Errorf("encrypted private channel stream header context %q does not match channel %q", parsedHeader.Context, parsed.ChannelID)
	}
	return parsedHeader, nil
}

func (h *ChannelHandler) parseEncryptedNativeStreamRecordIndex(r *http.Request) (uint64, error) {
	raw := strings.TrimSpace(r.Header.Get("X-SDN-Encrypted-Record-Index"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("recordIndex"))
	}
	if raw == "" {
		return 0, fmt.Errorf("encrypted private channel stream record index required")
	}
	recordIndex, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("encrypted private channel stream record index must be a uint32: %w", err)
	}
	return recordIndex, nil
}

func validateEncryptedNativeStreamHeaderFields(header EncryptedNativeStreamHeader) error {
	if !strings.EqualFold(header.Algorithm, "x25519") {
		return nil
	}
	if _, err := encryptedNativeStreamHeaderDecodeHexSize(header.SenderPublicKey, 32); err != nil {
		return fmt.Errorf("encrypted private channel stream header senderPublicKey must be 32 bytes: %w", err)
	}
	if _, err := encryptedNativeStreamHeaderDecodeHexSize(header.NonceStart, 12); err != nil {
		return fmt.Errorf("encrypted private channel stream header nonceStart must be 12 bytes: %w", err)
	}
	if strings.TrimSpace(header.RecipientKeyID) != "" {
		if _, err := encryptedNativeStreamHeaderDecodeHexSize(header.RecipientKeyID, 8); err != nil {
			return fmt.Errorf("encrypted private channel stream header recipientKeyId must be 8 bytes: %w", err)
		}
	}
	return nil
}

func encryptedNativeStreamHeaderDecodeHexSize(value string, want int) ([]byte, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}
	if len(decoded) != want {
		return nil, fmt.Errorf("got %d", len(decoded))
	}
	return decoded, nil
}

func encryptedNativeStreamHeaderBytes(header map[string]interface{}, names ...string) (string, bool) {
	for _, name := range names {
		value, ok := header[name]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			trimmed := strings.TrimSpace(typed)
			if trimmed != "" {
				return trimmed, true
			}
		case []interface{}:
			encoded, ok := encryptedNativeStreamHeaderByteArrayHex(typed)
			if ok {
				return encoded, true
			}
		}
	}
	return "", false
}

func encryptedNativeStreamHeaderByteArrayHex(values []interface{}) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	bytes := make([]byte, len(values))
	for i, value := range values {
		number, ok := value.(float64)
		if !ok || number < 0 || number > 255 || number != float64(byte(number)) {
			return "", false
		}
		bytes[i] = byte(number)
	}
	return hex.EncodeToString(bytes), true
}

func encryptedNativeStreamHeaderString(header map[string]interface{}, names ...string) (string, bool) {
	for _, name := range names {
		value, ok := header[name].(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		return value, true
	}
	return "", false
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

func providerPublicKeyHexFromRequest(r *http.Request) (string, bool, error) {
	key, err := providerPublicKeyFromRequest(r)
	if err != nil {
		message := err.Error()
		if strings.Contains(message, "provider public key is required") {
			return "", false, nil
		}
		return "", false, err
	}
	return hex.EncodeToString(key), true, nil
}

func (h *ChannelHandler) requestMatchesVerifiedProvider(r *http.Request, parsed channels.ChannelID) bool {
	metadata, ok := h.metadata.Get(parsed)
	if !ok || strings.TrimSpace(metadata.ProviderPublicKey) == "" {
		return false
	}
	providerKey, present, err := providerPublicKeyHexFromRequest(r)
	if err != nil || !present {
		return false
	}
	return strings.EqualFold(metadata.ProviderPublicKey, providerKey)
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
	metadata, verified := h.verifiedChannelMetadata(parsed)
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
	metadata, verified := h.verifiedChannelMetadata(parsed)
	payload["channelHead"] = firstNonEmptyChannelString(metadata.ChannelHead, metadata.PNMCID)
	payload["providerPeer"] = metadata.ProviderPeer
	payload["localRows"] = metadata.LocalRows
	payload["remoteRows"] = metadata.RemoteRows
	payload["syncedRows"] = metadata.SyncedRows
	payload["missingRows"] = metadata.MissingRows
	payload["pinnedCount"] = metadata.PinnedRows
	payload["pinnedRows"] = metadata.PinnedRows
	payload["pinnedBytes"] = metadata.PinnedBytes
	payload["syncedBytes"] = metadata.SyncedBytes
	payload["throughputBytesPerSecond"] = metadata.ThroughputBPS
	payload["wireSpeedUtilization"] = metadata.WireUtilization
	if linkBytesPerSecond, ok := channelLinkBytesPerSecond(); ok {
		requiredBytesPerSecond := int64(linkBytesPerSecond * channelWireSpeedTarget)
		payload["wireSpeedTarget"] = channelWireSpeedTarget
		payload["requiredBytesPerSecond"] = requiredBytesPerSecond
		payload["targetMet"] = metadata.ThroughputBPS >= requiredBytesPerSecond
	}
	payload["timingsMs"] = channelMonitorTimings(metadata.TimingsMs)
	payload["lastVerifiedUpdate"] = ""
	if verified {
		payload["lastVerifiedUpdate"] = metadata.VerifiedAt.Format(time.RFC3339Nano)
	}
	return payload
}

func (h *ChannelHandler) verifiedChannelMetadata(parsed channels.ChannelID) (channels.VerifiedMetadata, bool) {
	if h == nil {
		return channels.VerifiedMetadata{}, false
	}
	if metadata, ok := h.metadata.Get(parsed); ok {
		return metadata, true
	}
	metadata, ok := h.restoreVerifiedDatasetPublicationMetadata(parsed)
	if !ok {
		// Same durable fallback the $PNM head route uses: a channel whose head
		// is served must not read as unverified in its own detail row.
		return h.restoreVerifiedPNMFromDatasetPublication(parsed)
	}
	return h.metadata.RecordRestored(metadata), true
}

// hasVerifiedPNMPinLedgerEvidence reports whether the pin ledger holds any
// verified pnm-role entry at all — the precondition for the LocalReplicaStats
// sweeps that restore channel metadata from it.
//
// It asks for ONE BIT and pays for one bit: SELECT 1 ... LIMIT 1 on the
// (verification_state, updated_at) index, no row materialized, no ORDER BY.
// The previous shape called ListPinLedgerEntries, which SELECTs 21 columns and
// sorts them into a temp B-tree to answer len(entries) > 0.
func (h *ChannelHandler) hasVerifiedPNMPinLedgerEvidence() bool {
	if h == nil || h.store == nil {
		return false
	}
	present, err := h.store.HasPinLedgerEntry(storage.PinLedgerQuery{
		Role:              verifiedPNMPinLedgerRole,
		VerificationState: verifiedPinLedgerState,
	})
	if err != nil {
		return false
	}
	return present
}

const (
	verifiedPNMPinLedgerRole = "pnm"
	verifiedPinLedgerState   = "verified"
)

func (h *ChannelHandler) restoreVerifiedDatasetPublicationMetadata(parsed channels.ChannelID) (channels.VerifiedMetadata, bool) {
	if h == nil || h.store == nil {
		return channels.VerifiedMetadata{}, false
	}
	if !h.hasVerifiedPNMPinLedgerEvidence() {
		return channels.VerifiedMetadata{}, false
	}
	schemaName, err := channels.SchemaNameFromStandardCode(parsed.StandardCode)
	if err != nil {
		return channels.VerifiedMetadata{}, false
	}
	stats, err := h.store.LocalReplicaStats(storage.LocalReplicaStatsQuery{
		SchemaName:   schemaName,
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		return channels.VerifiedMetadata{}, false
	}
	var newest channels.VerifiedMetadata
	found := false
	for _, stat := range stats {
		if datasetPublicationSourceID(stat.ProviderID, stat.SourceName) != parsed.SourceID {
			continue
		}
		metadata, ok := h.restoreVerifiedDatasetPublicationMetadataFromStat(parsed, schemaName, stat)
		if !ok {
			continue
		}
		if !found || verifiedMetadataIsNewer(metadata, newest) {
			newest = metadata
			found = true
		}
	}
	if found {
		return newest, true
	}
	return channels.VerifiedMetadata{}, false
}

func (h *ChannelHandler) restoreVerifiedDatasetPublicationMetadataList(standardFilter string) []channels.VerifiedMetadata {
	if h == nil || h.store == nil {
		return nil
	}
	// ⛔ COST CONTRACT: the schema set comes from the LEDGER, never from the
	// supported-schema list.
	//
	// Every schema iterated below pays a LocalReplicaStats call, and
	// LocalReplicaStats counts every raw record in the schema (pin_ledger.go
	// localReplicaRawStats -> CountRawRecords + RawRecordHead) — on prod that is
	// ~1.4 M records. Every row this loop can emit requires a VERIFIED pnm-role
	// pin-ledger entry for that schema
	// (restoreVerifiedDatasetPublicationMetadataFromStat), so a schema with no
	// such entry is provably empty and must never be swept.
	//
	// Asking the ledger for its distinct schemas is therefore both cheaper AND
	// exact: one indexed read replaces a 206-schema fan-out, and an empty ledger
	// (prod's state, 0 rows verified on host-01) costs exactly that one read.
	// The previous shape asked a boolean and then swept ALL 206 schemas anyway
	// the moment a single ledger row appeared — a latent 206 x CountRawRecords
	// bomb armed by the first pin.
	standardFilter = strings.TrimSpace(standardFilter)
	ledgerQuery := storage.PinLedgerQuery{
		Role:              verifiedPNMPinLedgerRole,
		VerificationState: verifiedPinLedgerState,
	}
	if standardFilter != "" {
		schemaName, err := channels.SchemaNameFromStandardCode(standardFilter)
		if err != nil {
			return nil
		}
		ledgerQuery.SchemaName = schemaName
	}
	schemaNames, err := h.store.DistinctPinLedgerSchemas(ledgerQuery)
	if err != nil || len(schemaNames) == 0 {
		return nil
	}
	restoredByChannelID := make(map[string]channels.VerifiedMetadata)
	for _, schemaName := range schemaNames {
		standardCode, err := channels.StandardCodeFromSchemaName(schemaName)
		if err != nil {
			continue
		}
		stats, err := h.store.LocalReplicaStats(storage.LocalReplicaStatsQuery{
			SchemaName:   schemaName,
			QueryProfile: storage.DatasetPublicationQueryProfile,
		})
		if err != nil {
			continue
		}
		for _, stat := range stats {
			sourceID := datasetPublicationSourceID(stat.ProviderID, stat.SourceName)
			if sourceID == "" {
				continue
			}
			channelID, err := channels.FormatChannelID(channels.ChannelIDInput{
				SourceID:     sourceID,
				StandardCode: standardCode,
			})
			if err != nil {
				continue
			}
			parsed, err := channels.ParseChannelID(channelID)
			if err != nil {
				continue
			}
			metadata, ok := h.restoreVerifiedDatasetPublicationMetadataFromStat(parsed, schemaName, stat)
			if !ok {
				continue
			}
			if existing, ok := restoredByChannelID[metadata.ChannelID]; ok && !verifiedMetadataIsNewer(metadata, existing) {
				continue
			}
			restoredByChannelID[metadata.ChannelID] = metadata
		}
	}
	restored := make([]channels.VerifiedMetadata, 0, len(restoredByChannelID))
	for _, metadata := range restoredByChannelID {
		restored = append(restored, h.metadata.RecordRestored(metadata))
	}
	sort.Slice(restored, func(i, j int) bool {
		return restored[i].ChannelID < restored[j].ChannelID
	})
	return restored
}

func verifiedMetadataIsNewer(candidate, existing channels.VerifiedMetadata) bool {
	candidateTime := candidate.VerifiedAt
	if candidateTime.IsZero() {
		candidateTime = candidate.DPMVerifiedAt
	}
	existingTime := existing.VerifiedAt
	if existingTime.IsZero() {
		existingTime = existing.DPMVerifiedAt
	}
	if candidateTime.Equal(existingTime) {
		return candidate.ChannelHead > existing.ChannelHead
	}
	return candidateTime.After(existingTime)
}

func (h *ChannelHandler) restoreVerifiedDatasetPublicationMetadataFromStat(parsed channels.ChannelID, schemaName string, stat storage.LocalReplicaStats) (channels.VerifiedMetadata, bool) {
	if h == nil || h.store == nil {
		return channels.VerifiedMetadata{}, false
	}
	pnm, ok := h.latestVerifiedPinLedgerEntry(storage.PinLedgerQuery{
		SchemaName:        schemaName,
		ProviderPeerID:    stat.ProviderPeerID,
		ProviderPublicKey: stat.ProviderPublicKey,
		ProviderID:        stat.ProviderID,
		SourceName:        stat.SourceName,
		BatchID:           stat.BatchID,
		QueryProfile:      stat.QueryProfile,
		Role:              "pnm",
		VerificationState: "verified",
	})
	if !ok {
		return channels.VerifiedMetadata{}, false
	}
	dpm, ok := h.latestVerifiedPinLedgerEntry(storage.PinLedgerQuery{
		SchemaName:        schemaName,
		ProviderPeerID:    stat.ProviderPeerID,
		ProviderPublicKey: stat.ProviderPublicKey,
		ProviderID:        stat.ProviderID,
		SourceName:        stat.SourceName,
		BatchID:           stat.BatchID,
		QueryProfile:      stat.QueryProfile,
		Role:              "manifest",
		VerificationState: "verified",
	})
	if !ok {
		return channels.VerifiedMetadata{}, false
	}
	verifiedAt := pnm.VerifiedAt
	if verifiedAt.IsZero() {
		verifiedAt = pnm.UpdatedAt
	}
	dpmVerifiedAt := dpm.VerifiedAt
	if dpmVerifiedAt.IsZero() {
		dpmVerifiedAt = dpm.UpdatedAt
	}
	return channels.VerifiedMetadata{
		ChannelID:         parsed.ChannelID,
		ChannelHead:       firstNonEmptyChannelString(stat.Head, pnm.Head, dpm.Head),
		PNMCID:            pnm.CID,
		PNMFileID:         parsed.StandardCode,
		DPMFileID:         parsed.StandardCode,
		VerifiedAt:        verifiedAt,
		DPMVerifiedAt:     dpmVerifiedAt,
		ProviderPeer:      firstNonEmptyChannelString(stat.ProviderPeerID, pnm.ProviderPeerID, dpm.ProviderPeerID),
		ProviderPublicKey: firstNonEmptyChannelString(stat.ProviderPublicKey, pnm.ProviderPublicKey, dpm.ProviderPublicKey),
		Visibility:        "public",
		EncryptionState:   "none",
		LocalRows:         int(stat.LocalRows),
		RemoteRows:        int(stat.PinnedRows),
		SyncedRows:        int(stat.PinnedRows),
		MissingRows:       0,
		PinnedRows:        int(stat.PinnedRows),
		PinnedBytes:       stat.PinnedBytes,
		SyncedBytes:       stat.PinnedBytes,
	}, true
}

func (h *ChannelHandler) latestVerifiedPinLedgerEntry(query storage.PinLedgerQuery) (storage.PinLedgerEntry, bool) {
	if h == nil || h.store == nil {
		return storage.PinLedgerEntry{}, false
	}
	entries, err := h.store.ListPinLedgerEntries(query)
	if err != nil || len(entries) == 0 {
		return storage.PinLedgerEntry{}, false
	}
	return entries[0], true
}

func datasetPublicationSourceID(providerID, sourceName string) string {
	return datasetPublicationChannelSourceID(DatasetPublicationRequest{ProviderID: providerID, SourceName: sourceName}, datasetPublicationSourceIdentity{
		ProviderID: providerID,
		SourceName: sourceName,
	})
}

func channelMonitorTimings(timings map[string]int64) map[string]int64 {
	if timings == nil {
		return channelThroughputTimings{}.AsMilliseconds()
	}
	return timings
}

func firstNonEmptyChannelString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (h *ChannelHandler) channelMonitorWithDecision(parsed channels.ChannelID, decision channels.AccessDecision) map[string]interface{} {
	payload := h.channelMonitor(parsed)
	payload["grantState"] = decision.GrantState
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
