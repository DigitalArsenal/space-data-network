// Package subscription provides admin API handlers for routing and streaming management.
package subscription

import (
	"encoding/json"
	"net/http"
	"strings"
)

// AdminAPIHandler provides HTTP handlers for the full admin interface
// including subscriptions, routing configuration, streaming sessions, and edge relay filters.
type AdminAPIHandler struct {
	topicRouter *TopicRouter
	subHandler  *APIHandler
}

// NewAdminAPIHandler creates a new admin API handler
func NewAdminAPIHandler(topicRouter *TopicRouter) *AdminAPIHandler {
	return &AdminAPIHandler{
		topicRouter: topicRouter,
		subHandler:  NewAPIHandler(topicRouter.Manager()),
	}
}

// RegisterRoutes registers all admin API routes
func (h *AdminAPIHandler) RegisterRoutes(mux *http.ServeMux) {
	// Subscription routes (delegate to existing handler)
	h.subHandler.RegisterRoutes(mux)

	// Routing configuration
	mux.HandleFunc("/api/routing/config", h.handleRoutingConfig)
	mux.HandleFunc("/api/routing/topics", h.handleRoutingTopics)

	// Streaming session management
	mux.HandleFunc("/api/streaming/sessions", h.handleStreamingSessions)
	mux.HandleFunc("/api/streaming/sessions/", h.handleStreamingSession)
	mux.HandleFunc("/api/streaming/stats", h.handleStreamingStats)

	// Edge relay filter management
	mux.HandleFunc("/api/relay/filters", h.handleEdgeRelayFilters)

	// Admin UI pages
	mux.HandleFunc("/admin/subscriptions", h.handleSubscriptionsPage)
	mux.HandleFunc("/admin/subscriptions/new", h.handleNewSubscriptionPage)
	mux.HandleFunc("/admin/routing", h.handleRoutingPage)
	mux.HandleFunc("/admin/streaming", h.handleStreamingPage)
}

// --- Routing Configuration ---

// RoutingConfigResponse is the routing configuration API response
type RoutingConfigResponse struct {
	LocalPeerID    string   `json:"localPeerId"`
	RelayMode      bool     `json:"relayMode"`
	ActiveTopics   []string `json:"activeTopics"`
	SchemaTopics   []string `json:"schemaTopics"`
	PeerTopics     []string `json:"peerTopics"`
}

func (h *AdminAPIHandler) handleRoutingConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		topics := h.topicRouter.GetRequiredTopics()
		var schemaTopics, peerTopics []string
		for _, t := range topics {
			if strings.HasPrefix(t, "/sdn/data/") {
				schemaTopics = append(schemaTopics, t)
			} else if strings.HasPrefix(t, "/sdn/peer/") {
				peerTopics = append(peerTopics, t)
			}
		}

		resp := RoutingConfigResponse{
			LocalPeerID:  h.topicRouter.localPeer,
			RelayMode:    h.topicRouter.router.relayMode,
			ActiveTopics: topics,
			SchemaTopics: schemaTopics,
			PeerTopics:   peerTopics,
		}
		writeJSON(w, http.StatusOK, resp)

	case http.MethodPut:
		var req struct {
			RelayMode bool `json:"relayMode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		h.topicRouter.router.SetRelayMode(req.RelayMode)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success":   true,
			"relayMode": req.RelayMode,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *AdminAPIHandler) handleRoutingTopics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	topics := h.topicRouter.GetRequiredTopics()
	writeJSON(w, http.StatusOK, TopicsResponse{Topics: topics})
}

// --- Streaming Sessions ---

// CreateSessionRequest is the request to create a streaming session
type CreateSessionRequest struct {
	SubscriptionID string   `json:"subscriptionId"`
	PeerID         string   `json:"peerId"`
	SchemaTypes    []string `json:"schemaTypes"`
	Mode           int      `json:"mode"` // 0=single, 1=streaming, 2=batch
	EncryptionMode int      `json:"encryptionMode"` // 0=none, 1=ECIES, 2=sessionKey, 3=hybrid
}

// SessionResponse is the API response for a streaming session
type SessionResponse struct {
	ID             string   `json:"id"`
	SubscriptionID string   `json:"subscriptionId"`
	PeerID         string   `json:"peerId"`
	SchemaTypes    []string `json:"schemaTypes"`
	Mode           int      `json:"mode"`
	EncryptionMode int      `json:"encryptionMode"`
	SessionKeyID   string   `json:"sessionKeyId,omitempty"`
	CreatedAt      string   `json:"createdAt"`
	LastActivity   string   `json:"lastActivity"`
	MessagesSent   int64    `json:"messagesSent"`
	BytesSent      int64    `json:"bytesSent"`
	Active         bool     `json:"active"`
}

func toSessionResponse(s *StreamingSession) SessionResponse {
	return SessionResponse{
		ID:             s.ID,
		SubscriptionID: s.SubscriptionID,
		PeerID:         s.PeerID,
		SchemaTypes:    s.SchemaTypes,
		Mode:           int(s.Mode),
		EncryptionMode: int(s.EncMode),
		SessionKeyID:   s.SessionKeyID,
		CreatedAt:      s.CreatedAt.Format("2006-01-02T15:04:05Z"),
		LastActivity:   s.LastActivity.Format("2006-01-02T15:04:05Z"),
		MessagesSent:   s.MessagesSent,
		BytesSent:      s.BytesSent,
		Active:         s.Active,
	}
}

func (h *AdminAPIHandler) handleStreamingSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sessions := h.topicRouter.Streaming().ListSessions()
		resp := make([]SessionResponse, len(sessions))
		for i, s := range sessions {
			resp[i] = toSessionResponse(s)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"sessions": resp,
			"total":    len(resp),
		})

	case http.MethodPost:
		var req CreateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}

		session, err := h.topicRouter.Streaming().CreateSession(
			req.SubscriptionID,
			req.PeerID,
			req.SchemaTypes,
			StreamMode(req.Mode),
			EncryptionMode(req.EncryptionMode),
		)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		writeJSON(w, http.StatusCreated, toSessionResponse(session))

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *AdminAPIHandler) handleStreamingSession(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/streaming/sessions/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "Session ID required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		session, err := h.topicRouter.Streaming().GetSession(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "Session not found")
			return
		}
		writeJSON(w, http.StatusOK, toSessionResponse(session))

	case http.MethodDelete:
		err := h.topicRouter.Streaming().CloseSession(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "Session not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *AdminAPIHandler) handleStreamingStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats := h.topicRouter.Streaming().Stats()
	writeJSON(w, http.StatusOK, stats)
}

// --- Edge Relay Filters ---

func (h *AdminAPIHandler) handleEdgeRelayFilters(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Return current default filter configuration
		writeJSON(w, http.StatusOK, DefaultEdgeRelayFilter())

	case http.MethodPut:
		var filter EdgeRelayFilter
		if err := json.NewDecoder(r.Body).Decode(&filter); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid filter configuration: "+err.Error())
			return
		}

		// Apply filter globally
		h.topicRouter.ClearTopicFilters("*")
		h.topicRouter.AddTopicFilter("*", filter.ToTopicFilter())

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"filter":  filter,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Admin UI Pages ---

func (h *AdminAPIHandler) handleSubscriptionsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(subscriptionsPageHTML))
}

func (h *AdminAPIHandler) handleNewSubscriptionPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(newSubscriptionPageHTML))
}

func (h *AdminAPIHandler) handleRoutingPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(routingPageHTML))
}

func (h *AdminAPIHandler) handleStreamingPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(streamingPageHTML))
}

// Routing admin page
const routingPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SDN Routing Configuration</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #1a1a2e; color: #eee; line-height: 1.6; }
        .container { max-width: 1200px; margin: 0 auto; padding: 20px; }
        h1 { color: #00d9ff; margin-bottom: 20px; }
        h2 { color: #00d9ff; margin: 20px 0 10px; }
        .nav { display: flex; gap: 15px; margin-bottom: 30px; }
        .nav a { color: #00d9ff; text-decoration: none; padding: 8px 16px; border: 1px solid #00d9ff33; border-radius: 4px; }
        .nav a:hover, .nav a.active { background: #00d9ff22; }
        .card { background: #16213e; padding: 20px; border-radius: 8px; margin-bottom: 20px; }
        .card-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 15px; }
        .info-row { display: flex; justify-content: space-between; padding: 10px 0; border-bottom: 1px solid #2a2a4a; }
        .info-label { color: #888; }
        .info-value { color: #00d9ff; font-family: monospace; }
        .topic-list { list-style: none; }
        .topic-list li { padding: 8px 12px; margin: 4px 0; background: #0f0f23; border-radius: 4px; font-family: monospace; font-size: 0.9em; }
        .topic-schema { color: #00d97f; }
        .topic-peer { color: #ffc107; }
        .btn { display: inline-block; padding: 8px 16px; background: #00d9ff; color: #1a1a2e; border: none; border-radius: 4px; cursor: pointer; font-weight: bold; }
        .btn:hover { background: #00b8d9; }
        .toggle { display: flex; align-items: center; gap: 10px; }
        .toggle input { width: auto; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Routing Configuration</h1>
        <div class="nav">
            <a href="/admin/subscriptions">Subscriptions</a>
            <a href="/admin/routing" class="active">Routing</a>
            <a href="/admin/streaming">Streaming</a>
        </div>

        <div class="card">
            <div class="card-header"><h2>Node Info</h2></div>
            <div class="info-row"><span class="info-label">Local Peer ID</span><span class="info-value" id="peer-id">-</span></div>
            <div class="info-row">
                <span class="info-label">Relay Mode</span>
                <span class="toggle">
                    <input type="checkbox" id="relay-mode" onchange="toggleRelayMode()">
                    <label for="relay-mode" id="relay-label">Disabled</label>
                </span>
            </div>
        </div>

        <div class="card">
            <div class="card-header"><h2>Active Topics</h2></div>
            <h3 style="color: #00d97f; margin-bottom: 10px;">Schema Topics</h3>
            <ul class="topic-list" id="schema-topics"></ul>
            <h3 style="color: #ffc107; margin: 15px 0 10px;">Peer Topics</h3>
            <ul class="topic-list" id="peer-topics"></ul>
        </div>

        <div class="card">
            <div class="card-header"><h2>Edge Relay Filter</h2></div>
            <form id="filter-form">
                <div class="toggle" style="margin-bottom: 10px;">
                    <input type="checkbox" id="allow-encrypted" checked>
                    <label for="allow-encrypted">Allow Encrypted</label>
                </div>
                <div class="toggle" style="margin-bottom: 10px;">
                    <input type="checkbox" id="allow-unencrypted" checked>
                    <label for="allow-unencrypted">Allow Unencrypted</label>
                </div>
                <button type="submit" class="btn">Update Filter</button>
            </form>
        </div>
    </div>

    <script>
        async function loadConfig() {
            const res = await fetch('/api/routing/config');
            const data = await res.json();
            document.getElementById('peer-id').textContent = data.localPeerId;
            document.getElementById('relay-mode').checked = data.relayMode;
            document.getElementById('relay-label').textContent = data.relayMode ? 'Enabled' : 'Disabled';

            const schemaList = document.getElementById('schema-topics');
            schemaList.innerHTML = (data.schemaTopics || []).map(t =>
                '<li class="topic-schema">' + t + '</li>'
            ).join('') || '<li>No schema topics</li>';

            const peerList = document.getElementById('peer-topics');
            peerList.innerHTML = (data.peerTopics || []).map(t =>
                '<li class="topic-peer">' + t + '</li>'
            ).join('') || '<li>No peer topics</li>';
        }

        async function toggleRelayMode() {
            const enabled = document.getElementById('relay-mode').checked;
            await fetch('/api/routing/config', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ relayMode: enabled })
            });
            document.getElementById('relay-label').textContent = enabled ? 'Enabled' : 'Disabled';
        }

        document.getElementById('filter-form').addEventListener('submit', async (e) => {
            e.preventDefault();
            await fetch('/api/relay/filters', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    allowEncrypted: document.getElementById('allow-encrypted').checked,
                    allowUnencrypted: document.getElementById('allow-unencrypted').checked,
                    minPriority: 0
                })
            });
            alert('Filter updated');
        });

        loadConfig();
    </script>
</body>
</html>`

// Streaming admin page
const streamingPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SDN Streaming Sessions</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #1a1a2e; color: #eee; line-height: 1.6; }
        .container { max-width: 1200px; margin: 0 auto; padding: 20px; }
        h1 { color: #00d9ff; margin-bottom: 20px; }
        .nav { display: flex; gap: 15px; margin-bottom: 30px; }
        .nav a { color: #00d9ff; text-decoration: none; padding: 8px 16px; border: 1px solid #00d9ff33; border-radius: 4px; }
        .nav a:hover, .nav a.active { background: #00d9ff22; }
        .stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 20px; margin-bottom: 30px; }
        .stat-card { background: #16213e; padding: 20px; border-radius: 8px; border-left: 4px solid #00d9ff; }
        .stat-value { font-size: 2em; font-weight: bold; color: #00d9ff; }
        .stat-label { color: #888; font-size: 0.9em; }
        table { width: 100%; border-collapse: collapse; background: #16213e; border-radius: 8px; overflow: hidden; }
        th, td { padding: 12px; text-align: left; border-bottom: 1px solid #2a2a4a; }
        th { background: #0f0f23; color: #00d9ff; }
        .btn-danger { padding: 6px 12px; background: #ff4757; color: white; border: none; border-radius: 4px; cursor: pointer; }
        .mode-tag { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 0.8em; }
        .mode-0 { background: #00d9ff22; color: #00d9ff; }
        .mode-1 { background: #00d97f22; color: #00d97f; }
        .mode-2 { background: #ffc10722; color: #ffc107; }
        .enc-0 { background: #88888822; color: #888; }
        .enc-1 { background: #ff475722; color: #ff4757; }
        .enc-2 { background: #9b59b622; color: #9b59b6; }
        .enc-3 { background: #e67e2222; color: #e67e22; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Streaming Sessions</h1>
        <div class="nav">
            <a href="/admin/subscriptions">Subscriptions</a>
            <a href="/admin/routing">Routing</a>
            <a href="/admin/streaming" class="active">Streaming</a>
        </div>

        <div class="stats" id="stats">
            <div class="stat-card">
                <div class="stat-value" id="active-sessions">-</div>
                <div class="stat-label">Active Sessions</div>
            </div>
            <div class="stat-card">
                <div class="stat-value" id="total-messages">-</div>
                <div class="stat-label">Messages Sent</div>
            </div>
            <div class="stat-card">
                <div class="stat-value" id="total-bytes">-</div>
                <div class="stat-label">Bytes Sent</div>
            </div>
        </div>

        <table>
            <thead>
                <tr>
                    <th>Session ID</th>
                    <th>Peer</th>
                    <th>Schemas</th>
                    <th>Mode</th>
                    <th>Encryption</th>
                    <th>Messages</th>
                    <th>Actions</th>
                </tr>
            </thead>
            <tbody id="sessions-table">
                <tr><td colspan="7">Loading...</td></tr>
            </tbody>
        </table>
    </div>

    <script>
        const modeNames = ['Single', 'Streaming', 'Batch'];
        const encNames = ['None', 'ECIES', 'Session Key', 'Hybrid'];

        async function loadStats() {
            const res = await fetch('/api/streaming/stats');
            const stats = await res.json();
            document.getElementById('active-sessions').textContent = stats.activeSessions;
            document.getElementById('total-messages').textContent = stats.totalMessagesSent.toLocaleString();
            document.getElementById('total-bytes').textContent = formatBytes(stats.totalBytesSent);
        }

        async function loadSessions() {
            const res = await fetch('/api/streaming/sessions');
            const data = await res.json();
            const tbody = document.getElementById('sessions-table');

            if (!data.sessions || data.sessions.length === 0) {
                tbody.innerHTML = '<tr><td colspan="7">No active sessions.</td></tr>';
                return;
            }

            tbody.innerHTML = data.sessions.map(s => ` + "`" + `
                <tr>
                    <td><code>${s.id.substring(0, 16)}...</code></td>
                    <td><code>${s.peerId.substring(0, 12)}...</code></td>
                    <td>${s.schemaTypes.join(', ')}</td>
                    <td><span class="mode-tag mode-${s.mode}">${modeNames[s.mode]}</span></td>
                    <td><span class="mode-tag enc-${s.encryptionMode}">${encNames[s.encryptionMode]}</span></td>
                    <td>${s.messagesSent.toLocaleString()}</td>
                    <td><button class="btn-danger" onclick="closeSession('${s.id}')">Close</button></td>
                </tr>
            ` + "`" + `).join('');
        }

        async function closeSession(id) {
            if (!confirm('Close this streaming session?')) return;
            await fetch('/api/streaming/sessions/' + id, { method: 'DELETE' });
            loadSessions();
            loadStats();
        }

        function formatBytes(bytes) {
            if (bytes === 0) return '0 B';
            const k = 1024;
            const sizes = ['B', 'KB', 'MB', 'GB'];
            const i = Math.floor(Math.log(bytes) / Math.log(k));
            return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
        }

        loadStats();
        loadSessions();
        setInterval(() => { loadStats(); loadSessions(); }, 5000);
    </script>
</body>
</html>`
