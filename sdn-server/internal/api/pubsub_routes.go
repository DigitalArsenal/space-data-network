package api

// HTTP pubsub, FlatBuffers only:
//
//	POST /api/v1/pubsub/publish   body: one size-prefixed SDS record frame
//	                              (application/vnd.sdn.flatbuffers.stream);
//	                              the schema is the frame's file identifier.
//	                              Reply: one $QRP Page (SCHEMA_NAME,
//	                              RECORD_COUNT=1, GENERATED_AT_MS).
//	GET  /api/v1/pubsub/messages?schema=&limit=
//	                              [$QRP Page SCHEMA_NAME, RECORD_COUNT=N, LIMIT][N record frames]
//	GET  /api/v1/pubsub/topics    one $QRP Page, TABLES: NAME=topic, KIND="topic"
//
// Every error is one $QRP{KIND=Error} frame.

import (
	"context"
	"encoding/binary"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/abac"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// pubsubPublishMaxBytes bounds one published record frame.
const pubsubPublishMaxBytes = 10 << 20

// topicPublisher is the minimal interface needed to publish to a named topic.
type topicPublisher interface {
	PublishToTopic(ctx context.Context, topicName string, data []byte) error
}

type schemaPublisher interface {
	Publish(schema string, data []byte) error
}

func publishSchemaPubSubMessage(ctx context.Context, publisher topicPublisher, schema string, data []byte) (string, error) {
	topicName := sdnpubsub.TopicName(schema)
	if schemaPub, ok := publisher.(schemaPublisher); ok {
		return topicName, schemaPub.Publish(schema, data)
	}
	return topicName, publisher.PublishToTopic(ctx, topicName, data)
}

func (h *CoreAPIHandler) registerPubSubRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/pubsub/topics", h.withRL(h.handleTopics))
	// Publish requires standard auth when authHandler is present.
	mux.HandleFunc("/api/v1/pubsub/publish", h.withRL(h.requireAuth(peers.Standard, h.handlePubSubPublish)))
	mux.HandleFunc("/api/v1/pubsub/messages", h.withRL(h.handlePubSubMessages))
}

// recordFrameSchema resolves the store-form schema of one size-prefixed SDS
// record frame from its own file identifier.
func recordFrameSchema(validator *sds.Validator, frame []byte) (string, bool) {
	identifier := syncwire.FrameIdentifier(frame)
	if identifier == "" {
		return "", false
	}
	return validator.SchemaForIdentifier(identifier)
}

func (h *CoreAPIHandler) handleTopics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "topics takes GET", 0)
		return
	}
	var tables []QRPTableField
	if h.pubsubSvc != nil {
		for _, topic := range h.pubsubSvc.GetTopics() {
			tables = append(tables, QRPTableField{Name: topic, Kind: "topic", Records: -1})
		}
	}
	WriteFrameStream(w, http.StatusOK, [][]byte{BuildQRP(QRPFields{
		Kind:          QRPKindPage,
		Status:        QRPStatusOk,
		TotalCount:    uint64(len(tables)),
		Tables:        tables,
		GeneratedAtMs: time.Now().UnixMilli(),
	})}, nil)
}

func (h *CoreAPIHandler) handlePubSubPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "publish takes POST with one record frame", 0)
		return
	}
	if h.publisher == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "service_unavailable", "pubsub publishing not available", 0)
		return
	}
	if h.validator == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "service_unavailable", "the schema index is not available", 0)
		return
	}
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != StreamContentType {
		WriteErrorFrame(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"publish takes one size-prefixed SDS record frame as "+StreamContentType, 0)
		return
	}
	frames, err := ReadFrames(r.Body, pubsubPublishMaxBytes)
	if err != nil || len(frames) != 1 {
		WriteErrorFrame(w, http.StatusBadRequest, "invalid_request", "publish takes exactly one size-prefixed SDS record frame", 0)
		return
	}
	data := frames[0]
	schema, ok := recordFrameSchema(h.validator, data)
	if !ok {
		WriteErrorFrame(w, http.StatusBadRequest, "invalid_schema", "the frame's file identifier names no known standard", 0)
		return
	}
	if err := h.validator.Validate(r.Context(), schema, data); err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "validation_failed", "validation failed: "+err.Error(), 0)
		return
	}

	// ABAC policy check — runs after trust gate (defense in depth).
	if h.policyEngine != nil {
		session := auth.SessionFromContext(r.Context())
		if session == nil {
			WriteErrorFrame(w, http.StatusUnauthorized, "unauthorized", "not authenticated", 0)
			return
		}
		sub := abac.Subject{
			XPub:       session.XPub,
			TrustLevel: int(session.TrustLevel),
			Attrs:      map[string]string{},
		}
		decision := h.policyEngine.Evaluate(sub, abac.ActionPublish, abac.Resource{Schema: schema})
		if !decision.Allowed {
			WriteErrorFrame(w, http.StatusForbidden, "policy_denied", "access denied by policy: "+decision.Reason, 0)
			return
		}
	}

	if _, err := publishSchemaPubSubMessage(r.Context(), h.publisher, schema, data); err != nil {
		WriteErrorFrame(w, http.StatusInternalServerError, "publish_failed", "failed to publish: "+err.Error(), 0)
		return
	}
	WriteFrameStream(w, http.StatusOK, [][]byte{BuildQRP(QRPFields{
		Kind:          QRPKindPage,
		Status:        QRPStatusOk,
		SchemaName:    schema,
		RecordCount:   1,
		GeneratedAtMs: time.Now().UnixMilli(),
	})}, map[string]string{StreamSchemaHeader: schema})
}

func (h *CoreAPIHandler) handlePubSubMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "messages takes GET", 0)
		return
	}
	schema := strings.TrimSpace(r.URL.Query().Get("schema"))
	limit := 50
	if parsed, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && parsed > 0 {
		limit = min(parsed, 1000)
	}
	if schema == "" {
		WriteErrorFrame(w, http.StatusBadRequest, "invalid_request", "schema query parameter is required", 0)
		return
	}
	if err := sds.ValidateSchemaName(schema); err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "invalid_schema", "invalid schema name: "+err.Error(), 0)
		return
	}
	var raw [][]byte
	if h.store != nil {
		var err error
		if raw, err = h.store.QueryAll(schema, limit); err != nil {
			WriteErrorFrame(w, http.StatusInternalServerError, "query_failed", "query failed: "+err.Error(), 0)
			return
		}
	}
	frames := make([][]byte, 0, 1+len(raw))
	frames = append(frames, BuildQRP(QRPFields{
		Kind:          QRPKindPage,
		Status:        QRPStatusOk,
		SchemaName:    schema,
		RecordCount:   uint32(len(raw)),
		Limit:         uint32(limit),
		GeneratedAtMs: time.Now().UnixMilli(),
	}))
	for _, data := range raw {
		frame := make([]byte, framePrefixLength+len(data))
		binary.LittleEndian.PutUint32(frame, uint32(len(data)))
		copy(frame[framePrefixLength:], data)
		frames = append(frames, frame)
	}
	WriteFrameStream(w, http.StatusOK, frames, map[string]string{StreamSchemaHeader: schema})
}
