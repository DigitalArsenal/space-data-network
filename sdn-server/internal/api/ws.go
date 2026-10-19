package api

import (
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/gorilla/websocket"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Gap B10.3(b): this handler is registered outside the /api/ prefix the
	// top-level auth wall's isAPIOrPlugin check inspects, so
	// cmd/spacedatanetwork/main.go self-gates the whole /ws route behind
	// RequireAuth(Standard) before a connection ever reaches here (same
	// pattern as /webui). CheckOrigin is a second, independent layer: a
	// same-origin-only check so a session cookie cannot be ridden by a
	// cross-origin page's browser-initiated WebSocket handshake (a classic
	// CSWSH — cross-site WebSocket hijacking — vector). There is no
	// separate configured-origins allowlist elsewhere in this codebase to
	// reuse, so "same host as the request" is the whole policy; a
	// same-origin connection can both subscribe AND publish (state-
	// changing: publish fans out into local and libp2p pubsub) — anonymous
	// or cross-origin READ-only access, if ever wanted, must be a
	// deliberate future decision, not an accidental default.
	CheckOrigin: checkSameOriginHost,
}

// checkSameOriginHost rejects a WebSocket upgrade whose Origin header names
// a different host than the request itself. Browsers always send Origin on
// a WebSocket handshake; non-browser clients (curl, server-side tooling,
// this codebase's own test dialers) typically omit it entirely and are let
// through, matching gorilla/websocket's own documented secure default.
func checkSameOriginHost(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// /ws speaks binary FlatBuffer frames only; every message is one
// size-prefixed SDS frame.
//
//	client -> node: $DSS{REQUESTED_ACTION=Subscribe|Unsubscribe, SCHEMA_NAME}
//	                subscribes or unsubscribes; any other SDS record frame is
//	                a publish (schema = its file identifier).
//	node -> client: $DSS{SCHEMA_NAME, SUBSCRIBED} acknowledges; a delivered
//	                message is the published record frame verbatim; an error
//	                is one $QRP{KIND=Error} frame. Text frames are refused.

// $DSS dssAction values.
const (
	dssActionSubscribe   int8 = 2
	dssActionUnsubscribe int8 = 3
)

// dssAckFrame is the $DSS{SCHEMA_NAME, SUBSCRIBED} acknowledgement.
func dssAckFrame(schema string, subscribed bool) []byte {
	b := flatbuffers.NewBuilder(64)
	name := b.CreateString(schema)
	DSS.DSSStart(b)
	DSS.DSSAddSCHEMA_NAME(b, name)
	DSS.DSSAddSUBSCRIBED(b, subscribed)
	DSS.FinishSizePrefixedDSSBuffer(b, DSS.DSSEnd(b))
	return b.FinishedBytes()
}

func wsErrorFrame(code, msg string) []byte {
	return syncwire.ErrorFrame(syncwire.QRPStatusError, code, msg, http.StatusBadRequest, 0)
}

// wsConn wraps a single WebSocket connection.
type wsConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *wsConn) send(frame []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.BinaryMessage, frame)
}

// wsHub manages WebSocket connections, routing published messages to subscribers.
type wsHub struct {
	mu        sync.RWMutex
	subs      map[string][]*wsConn // schema -> connections
	publisher topicPublisher
	validator *sds.Validator
}

func newWSHub(publisher topicPublisher, validator *sds.Validator) *wsHub {
	return &wsHub{
		subs:      make(map[string][]*wsConn),
		publisher: publisher,
		validator: validator,
	}
}

// subscribe registers conn for the given schema.
func (h *wsHub) subscribe(schema string, c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[schema] = append(h.subs[schema], c)
}

// unsubscribe removes conn from the given schema.
func (h *wsHub) unsubscribe(schema string, c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	existing := h.subs[schema]
	filtered := existing[:0]
	for _, x := range existing {
		if x != c {
			filtered = append(filtered, x)
		}
	}
	h.subs[schema] = filtered
}

// remove removes conn from all schema subscriptions.
func (h *wsHub) remove(c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for schema, conns := range h.subs {
		filtered := conns[:0]
		for _, x := range conns {
			if x != c {
				filtered = append(filtered, x)
			}
		}
		h.subs[schema] = filtered
	}
}

// broadcast sends a frame to all connections subscribed to schema.
func (h *wsHub) broadcast(schema string, frame []byte) {
	h.mu.RLock()
	conns := make([]*wsConn, len(h.subs[schema]))
	copy(conns, h.subs[schema])
	h.mu.RUnlock()

	for _, c := range conns {
		_ = c.send(frame)
	}
}

// WSHandler is an http.Handler that upgrades connections to WebSocket and
// handles subscribe / publish frames.
type WSHandler struct {
	hub *wsHub
}

// NewWSHandler creates a WSHandler with the given publisher and validator.
// The publisher may be nil (local delivery only); without a validator every
// frame is refused, since no schema can be resolved.
func NewWSHandler(publisher topicPublisher, validator *sds.Validator) *WSHandler {
	return &WSHandler{hub: newWSHub(publisher, validator)}
}

func (wh *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote the error response.
		return
	}

	c := &wsConn{conn: conn}
	defer func() {
		wh.hub.remove(c)
		conn.Close()
	}()

	for {
		kind, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if kind != websocket.BinaryMessage {
			_ = c.send(wsErrorFrame("text_frame_refused", "/ws takes binary size-prefixed FlatBuffer frames only"))
			continue
		}
		frames, err := SplitFrames(raw)
		if err != nil || len(frames) != 1 {
			_ = c.send(wsErrorFrame("invalid_frame", "each message is exactly one size-prefixed SDS frame"))
			continue
		}
		frame := frames[0]
		if wh.hub.validator == nil {
			_ = c.send(wsErrorFrame("service_unavailable", "the schema index is not available"))
			continue
		}
		if DSS.SizePrefixedDSSBufferHasIdentifier(frame) {
			if err := sds.VerifySizePrefixedStructure(SyncSchemaName, frame); err != nil {
				_ = c.send(wsErrorFrame("invalid_frame", "malformed $DSS frame: "+err.Error()))
				continue
			}
			request := DSS.GetSizePrefixedRootAsDSS(frame, 0)
			schema := syncwire.NormalizeSchema(string(request.SCHEMA_NAME()))
			action := int8(request.REQUESTED_ACTION())
			if action == dssActionSubscribe || action == dssActionUnsubscribe {
				if err := sds.ValidateSchemaName(schema); err != nil {
					_ = c.send(wsErrorFrame("invalid_schema", "invalid schema: "+err.Error()))
					continue
				}
				if action == dssActionSubscribe {
					wh.hub.subscribe(schema, c)
				} else {
					wh.hub.unsubscribe(schema, c)
				}
				_ = c.send(dssAckFrame(schema, action == dssActionSubscribe))
				continue
			}
		}
		schema, ok := recordFrameSchema(wh.hub.validator, frame)
		if !ok {
			_ = c.send(wsErrorFrame("invalid_schema", "the frame's file identifier names no known standard"))
			continue
		}
		if err := wh.hub.validator.Validate(r.Context(), schema, frame); err != nil {
			_ = c.send(wsErrorFrame("validation_failed", "validation failed: "+err.Error()))
			continue
		}
		// Local subscribers first (same process), then libp2p pubsub.
		wh.hub.broadcast(schema, frame)
		if wh.hub.publisher != nil {
			_, _ = publishSchemaPubSubMessage(r.Context(), wh.hub.publisher, schema, frame)
		}
	}
}
