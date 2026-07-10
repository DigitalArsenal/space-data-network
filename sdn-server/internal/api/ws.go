package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
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

// wsClientMsg is the envelope sent from the browser / client to the server.
type wsClientMsg struct {
	Type   string `json:"type"`   // "subscribe" | "publish" | "unsubscribe"
	Schema string `json:"schema"` // e.g. "OMM.fbs"
	Data   string `json:"data"`   // base64-encoded FlatBuffer bytes (publish only)
}

// wsServerMsg is the envelope sent from the server to the client.
type wsServerMsg struct {
	Type   string `json:"type"` // "message" | "subscribed" | "unsubscribed" | "error"
	Schema string `json:"schema,omitempty"`
	Data   string `json:"data,omitempty"` // base64-encoded bytes
	From   string `json:"from,omitempty"` // originating peer ID string
	TS     string `json:"ts,omitempty"`   // RFC3339 timestamp
	Error  string `json:"error,omitempty"`
}

// wsConn wraps a single WebSocket connection.
type wsConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *wsConn) send(msg wsServerMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, data)
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

// broadcast sends a server message to all connections subscribed to schema.
func (h *wsHub) broadcast(schema string, msg wsServerMsg) {
	h.mu.RLock()
	conns := make([]*wsConn, len(h.subs[schema]))
	copy(conns, h.subs[schema])
	h.mu.RUnlock()

	for _, c := range conns {
		_ = c.send(msg)
	}
}

// WSHandler is an http.Handler that upgrades connections to WebSocket and
// handles subscribe / publish messages.
type WSHandler struct {
	hub *wsHub
}

// NewWSHandler creates a WSHandler with the given publisher and validator.
// Either may be nil — the handler degrades gracefully.
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
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var msg wsClientMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			_ = c.send(wsServerMsg{Type: "error", Error: "invalid JSON"})
			continue
		}

		switch msg.Type {
		case "subscribe":
			if msg.Schema == "" {
				_ = c.send(wsServerMsg{Type: "error", Error: "schema is required"})
				continue
			}
			if err := sds.ValidateSchemaName(msg.Schema); err != nil {
				_ = c.send(wsServerMsg{Type: "error", Error: "invalid schema: " + err.Error()})
				continue
			}
			wh.hub.subscribe(msg.Schema, c)
			_ = c.send(wsServerMsg{Type: "subscribed", Schema: msg.Schema})

		case "unsubscribe":
			if msg.Schema == "" {
				_ = c.send(wsServerMsg{Type: "error", Error: "schema is required"})
				continue
			}
			wh.hub.unsubscribe(msg.Schema, c)
			_ = c.send(wsServerMsg{Type: "unsubscribed", Schema: msg.Schema})

		case "publish":
			if msg.Schema == "" {
				_ = c.send(wsServerMsg{Type: "error", Error: "schema is required"})
				continue
			}
			if msg.Data == "" {
				_ = c.send(wsServerMsg{Type: "error", Error: "data is required"})
				continue
			}
			if err := sds.ValidateSchemaName(msg.Schema); err != nil {
				_ = c.send(wsServerMsg{Type: "error", Error: "invalid schema: " + err.Error()})
				continue
			}

			data, err := base64.StdEncoding.DecodeString(msg.Data)
			if err != nil {
				data, err = base64.URLEncoding.DecodeString(msg.Data)
				if err != nil {
					_ = c.send(wsServerMsg{Type: "error", Error: "data must be base64-encoded"})
					continue
				}
			}

			if wh.hub.validator != nil {
				if err := wh.hub.validator.Validate(r.Context(), msg.Schema, data); err != nil {
					_ = c.send(wsServerMsg{Type: "error", Error: "validation failed: " + err.Error()})
					continue
				}
			}

			// Broadcast to local subscribers first (zero-copy, same process).
			wh.hub.broadcast(msg.Schema, wsServerMsg{
				Type:   "message",
				Schema: msg.Schema,
				Data:   base64.StdEncoding.EncodeToString(data),
				TS:     time.Now().UTC().Format(time.RFC3339),
			})

			// Then propagate over libp2p pubsub if available.
			if wh.hub.publisher != nil {
				_, _ = publishSchemaPubSubMessage(r.Context(), wh.hub.publisher, msg.Schema, data)
			}

		default:
			_ = c.send(wsServerMsg{Type: "error", Error: "unknown message type: " + msg.Type})
		}
	}
}
