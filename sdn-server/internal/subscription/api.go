// Package subscription provides HTTP API handlers for subscription management.
package subscription

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// APIHandler provides HTTP handlers for subscription management
type APIHandler struct {
	manager *Manager
}

// NewAPIHandler creates a new API handler
func NewAPIHandler(manager *Manager) *APIHandler {
	return &APIHandler{manager: manager}
}

// RegisterRoutes registers API routes with an HTTP mux
func (h *APIHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/subscriptions", h.handleSubscriptions)
	mux.HandleFunc("/api/subscriptions/", h.handleSubscription)
	mux.HandleFunc("/api/subscriptions/topics", h.handleTopics)
	mux.HandleFunc("/api/subscriptions/stats", h.handleStats)
}

// handleSubscriptions handles GET (list) and POST (create) for subscriptions
func (h *APIHandler) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listSubscriptions(w, r)
	case http.MethodPost:
		h.createSubscription(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSubscription handles individual subscription operations
func (h *APIHandler) handleSubscription(w http.ResponseWriter, r *http.Request) {
	// Extract subscription ID from path
	id := strings.TrimPrefix(r.URL.Path, "/api/subscriptions/")
	if id == "" {
		http.Error(w, "Subscription ID required", http.StatusBadRequest)
		return
	}

	// Handle action suffixes
	if strings.HasSuffix(id, "/pause") {
		id = strings.TrimSuffix(id, "/pause")
		if r.Method == http.MethodPost {
			h.pauseSubscription(w, r, id)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if strings.HasSuffix(id, "/resume") {
		id = strings.TrimSuffix(id, "/resume")
		if r.Method == http.MethodPost {
			h.resumeSubscription(w, r, id)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getSubscription(w, r, id)
	case http.MethodPut:
		h.updateSubscription(w, r, id)
	case http.MethodDelete:
		h.deleteSubscription(w, r, id)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// listSubscriptions returns all subscriptions
func (h *APIHandler) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs := h.manager.ListSubscriptions()

	response := SubscriptionListResponse{
		Subscriptions: make([]SubscriptionResponse, len(subs)),
		Total:         len(subs),
	}

	for i, sub := range subs {
		response.Subscriptions[i] = toSubscriptionResponse(sub)
	}

	writeJSON(w, http.StatusOK, response)
}

// createSubscription creates a new subscription
func (h *APIHandler) createSubscription(w http.ResponseWriter, r *http.Request) {
	var req CreateSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	config := SubscriptionConfig{
		DataTypes:   req.DataTypes,
		SourcePeers: req.SourcePeers,
		Encrypted:   req.Encrypted,
		Streaming:   req.Streaming,
		Filters:     req.Filters,
		RateLimit:   req.RateLimit,
		TTL:         req.TTL,
	}

	sub, err := h.manager.CreateSubscription(config)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, toSubscriptionResponse(sub))
}

// getSubscription returns a single subscription
func (h *APIHandler) getSubscription(w http.ResponseWriter, r *http.Request, id string) {
	sub, err := h.manager.GetSubscription(id)
	if err != nil {
		if err == ErrSubscriptionNotFound {
			writeError(w, http.StatusNotFound, "Subscription not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, toSubscriptionResponse(sub))
}

// updateSubscription updates an existing subscription
func (h *APIHandler) updateSubscription(w http.ResponseWriter, r *http.Request, id string) {
	var req CreateSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	config := SubscriptionConfig{
		DataTypes:   req.DataTypes,
		SourcePeers: req.SourcePeers,
		Encrypted:   req.Encrypted,
		Streaming:   req.Streaming,
		Filters:     req.Filters,
		RateLimit:   req.RateLimit,
		TTL:         req.TTL,
	}

	sub, err := h.manager.UpdateSubscription(id, config)
	if err != nil {
		if err == ErrSubscriptionNotFound {
			writeError(w, http.StatusNotFound, "Subscription not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, toSubscriptionResponse(sub))
}

// deleteSubscription removes a subscription
func (h *APIHandler) deleteSubscription(w http.ResponseWriter, r *http.Request, id string) {
	err := h.manager.DeleteSubscription(id)
	if err != nil {
		if err == ErrSubscriptionNotFound {
			writeError(w, http.StatusNotFound, "Subscription not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// pauseSubscription pauses a subscription
func (h *APIHandler) pauseSubscription(w http.ResponseWriter, r *http.Request, id string) {
	err := h.manager.PauseSubscription(id)
	if err != nil {
		if err == ErrSubscriptionNotFound {
			writeError(w, http.StatusNotFound, "Subscription not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	sub, _ := h.manager.GetSubscription(id)
	writeJSON(w, http.StatusOK, toSubscriptionResponse(sub))
}

// resumeSubscription resumes a subscription
func (h *APIHandler) resumeSubscription(w http.ResponseWriter, r *http.Request, id string) {
	err := h.manager.ResumeSubscription(id)
	if err != nil {
		if err == ErrSubscriptionNotFound {
			writeError(w, http.StatusNotFound, "Subscription not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	sub, _ := h.manager.GetSubscription(id)
	writeJSON(w, http.StatusOK, toSubscriptionResponse(sub))
}

// handleTopics returns all required PubSub topics
func (h *APIHandler) handleTopics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	topics := h.manager.GetRequiredTopics()
	writeJSON(w, http.StatusOK, TopicsResponse{Topics: topics})
}

// handleStats returns subscription statistics
func (h *APIHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	subs := h.manager.ListSubscriptions()

	stats := SubscriptionStats{
		TotalSubscriptions: len(subs),
		ActiveSubscriptions: 0,
		PausedSubscriptions: 0,
		TotalMessages:      0,
		SchemaBreakdown:    make(map[string]int),
	}

	for _, sub := range subs {
		switch sub.Status {
		case StatusActive:
			stats.ActiveSubscriptions++
		case StatusPaused:
			stats.PausedSubscriptions++
		}

		stats.TotalMessages += sub.MessageCount

		for _, dt := range sub.Config.DataTypes {
			stats.SchemaBreakdown[dt]++
		}
	}

	writeJSON(w, http.StatusOK, stats)
}

// Request/Response types

// CreateSubscriptionRequest is the request body for creating a subscription
type CreateSubscriptionRequest struct {
	DataTypes   []string      `json:"dataTypes"`
	SourcePeers []string      `json:"sourcePeers"`
	Encrypted   bool          `json:"encrypted"`
	Streaming   bool          `json:"streaming"`
	Filters     []QueryFilter `json:"filters,omitempty"`
	RateLimit   int           `json:"rateLimit,omitempty"`
	TTL         int64         `json:"ttl,omitempty"`
}

// SubscriptionResponse is the API response for a subscription
type SubscriptionResponse struct {
	ID            string             `json:"id"`
	Config        SubscriptionConfig `json:"config"`
	CreatedAt     string             `json:"createdAt"`
	MessageCount  int64              `json:"messageCount"`
	LastMessageAt *string            `json:"lastMessageAt,omitempty"`
	Status        string             `json:"status"`
	ErrorMessage  string             `json:"errorMessage,omitempty"`
}

// SubscriptionListResponse is the API response for listing subscriptions
type SubscriptionListResponse struct {
	Subscriptions []SubscriptionResponse `json:"subscriptions"`
	Total         int                    `json:"total"`
}

// TopicsResponse is the API response for topics
type TopicsResponse struct {
	Topics []string `json:"topics"`
}

// SubscriptionStats contains subscription statistics
type SubscriptionStats struct {
	TotalSubscriptions  int            `json:"totalSubscriptions"`
	ActiveSubscriptions int            `json:"activeSubscriptions"`
	PausedSubscriptions int            `json:"pausedSubscriptions"`
	TotalMessages       int64          `json:"totalMessages"`
	SchemaBreakdown     map[string]int `json:"schemaBreakdown"`
}

// ErrorResponse is the API error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code"`
	Details string `json:"details,omitempty"`
}

// Helper functions

func toSubscriptionResponse(sub *Subscription) SubscriptionResponse {
	resp := SubscriptionResponse{
		ID:           sub.ID,
		Config:       sub.Config,
		CreatedAt:    sub.CreatedAt.Format(time.RFC3339),
		MessageCount: sub.MessageCount,
		Status:       string(sub.Status),
		ErrorMessage: sub.ErrorMessage,
	}

	if sub.LastMessageAt != nil {
		ts := sub.LastMessageAt.Format(time.RFC3339)
		resp.LastMessageAt = &ts
	}

	return resp
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, ErrorResponse{
		Error: message,
		Code:  status,
	})
}

// AdminUI provides an HTML admin interface for subscription management
type AdminUI struct {
	handler *APIHandler
}

// NewAdminUI creates a new admin UI
func NewAdminUI(manager *Manager) *AdminUI {
	return &AdminUI{
		handler: NewAPIHandler(manager),
	}
}

// RegisterRoutes registers admin UI routes
func (ui *AdminUI) RegisterRoutes(mux *http.ServeMux) {
	// API routes
	ui.handler.RegisterRoutes(mux)

	// UI routes
	mux.HandleFunc("/admin/subscriptions", ui.handleSubscriptionsPage)
	mux.HandleFunc("/admin/subscriptions/new", ui.handleNewSubscriptionPage)
}

// handleSubscriptionsPage serves the subscriptions management page
func (ui *AdminUI) handleSubscriptionsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(subscriptionsPageHTML))
}

// handleNewSubscriptionPage serves the new subscription form
func (ui *AdminUI) handleNewSubscriptionPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(newSubscriptionPageHTML))
}

// HTML templates for admin UI
const subscriptionsPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SDN Subscription Management</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #1a1a2e; color: #eee; line-height: 1.6; }
        .container { max-width: 1200px; margin: 0 auto; padding: 20px; }
        h1 { color: #00d9ff; margin-bottom: 20px; }
        .stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 20px; margin-bottom: 30px; }
        .stat-card { background: #16213e; padding: 20px; border-radius: 8px; border-left: 4px solid #00d9ff; }
        .stat-value { font-size: 2em; font-weight: bold; color: #00d9ff; }
        .stat-label { color: #888; font-size: 0.9em; }
        .actions { margin-bottom: 20px; }
        .btn { display: inline-block; padding: 10px 20px; background: #00d9ff; color: #1a1a2e; border: none; border-radius: 4px; cursor: pointer; text-decoration: none; font-weight: bold; }
        .btn:hover { background: #00b8d9; }
        .btn-danger { background: #ff4757; color: white; }
        .btn-danger:hover { background: #ff3344; }
        .btn-secondary { background: #4a4a6a; color: #eee; }
        table { width: 100%; border-collapse: collapse; background: #16213e; border-radius: 8px; overflow: hidden; }
        th, td { padding: 15px; text-align: left; border-bottom: 1px solid #2a2a4a; }
        th { background: #0f0f23; color: #00d9ff; font-weight: 600; }
        tr:hover { background: #1a1a3e; }
        .status { display: inline-block; padding: 4px 12px; border-radius: 20px; font-size: 0.85em; font-weight: 500; }
        .status-active { background: #00d97f22; color: #00d97f; }
        .status-paused { background: #ffc10722; color: #ffc107; }
        .status-error { background: #ff475722; color: #ff4757; }
        .data-types { display: flex; gap: 5px; flex-wrap: wrap; }
        .tag { background: #00d9ff22; color: #00d9ff; padding: 2px 8px; border-radius: 4px; font-size: 0.8em; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Subscription Management</h1>

        <div class="stats" id="stats">
            <div class="stat-card">
                <div class="stat-value" id="total-subs">-</div>
                <div class="stat-label">Total Subscriptions</div>
            </div>
            <div class="stat-card">
                <div class="stat-value" id="active-subs">-</div>
                <div class="stat-label">Active</div>
            </div>
            <div class="stat-card">
                <div class="stat-value" id="paused-subs">-</div>
                <div class="stat-label">Paused</div>
            </div>
            <div class="stat-card">
                <div class="stat-value" id="total-msgs">-</div>
                <div class="stat-label">Total Messages</div>
            </div>
        </div>

        <div class="actions">
            <a href="/admin/subscriptions/new" class="btn">+ New Subscription</a>
        </div>

        <table>
            <thead>
                <tr>
                    <th>ID</th>
                    <th>Data Types</th>
                    <th>Source Peers</th>
                    <th>Status</th>
                    <th>Messages</th>
                    <th>Actions</th>
                </tr>
            </thead>
            <tbody id="subscriptions-table">
                <tr><td colspan="6">Loading...</td></tr>
            </tbody>
        </table>
    </div>

    <script>
        async function loadStats() {
            const res = await fetch('/api/subscriptions/stats');
            const stats = await res.json();
            document.getElementById('total-subs').textContent = stats.totalSubscriptions;
            document.getElementById('active-subs').textContent = stats.activeSubscriptions;
            document.getElementById('paused-subs').textContent = stats.pausedSubscriptions;
            document.getElementById('total-msgs').textContent = stats.totalMessages.toLocaleString();
        }

        async function loadSubscriptions() {
            const res = await fetch('/api/subscriptions');
            const data = await res.json();
            const tbody = document.getElementById('subscriptions-table');

            if (data.subscriptions.length === 0) {
                tbody.innerHTML = '<tr><td colspan="6">No subscriptions. Create one to get started.</td></tr>';
                return;
            }

            tbody.innerHTML = data.subscriptions.map(sub => ` + "`" + `
                <tr>
                    <td><code>${sub.id}</code></td>
                    <td>
                        <div class="data-types">
                            ${sub.config.dataTypes.map(dt => ` + "`" + `<span class="tag">${dt}</span>` + "`" + `).join('')}
                        </div>
                    </td>
                    <td>${sub.config.sourcePeers.join(', ')}</td>
                    <td><span class="status status-${sub.status}">${sub.status}</span></td>
                    <td>${sub.messageCount.toLocaleString()}</td>
                    <td>
                        ${sub.status === 'active'
                            ? ` + "`" + `<button class="btn btn-secondary" onclick="pauseSub('${sub.id}')">Pause</button>` + "`" + `
                            : ` + "`" + `<button class="btn btn-secondary" onclick="resumeSub('${sub.id}')">Resume</button>` + "`" + `
                        }
                        <button class="btn btn-danger" onclick="deleteSub('${sub.id}')">Delete</button>
                    </td>
                </tr>
            ` + "`" + `).join('');
        }

        async function pauseSub(id) {
            await fetch(` + "`" + `/api/subscriptions/${id}/pause` + "`" + `, { method: 'POST' });
            loadSubscriptions();
            loadStats();
        }

        async function resumeSub(id) {
            await fetch(` + "`" + `/api/subscriptions/${id}/resume` + "`" + `, { method: 'POST' });
            loadSubscriptions();
            loadStats();
        }

        async function deleteSub(id) {
            if (!confirm('Are you sure you want to delete this subscription?')) return;
            await fetch(` + "`" + `/api/subscriptions/${id}` + "`" + `, { method: 'DELETE' });
            loadSubscriptions();
            loadStats();
        }

        loadStats();
        loadSubscriptions();
        setInterval(loadStats, 5000);
    </script>
</body>
</html>`

const newSubscriptionPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>New Subscription - SDN</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #1a1a2e; color: #eee; line-height: 1.6; }
        .container { max-width: 800px; margin: 0 auto; padding: 20px; }
        h1 { color: #00d9ff; margin-bottom: 20px; }
        .form-group { margin-bottom: 20px; }
        label { display: block; margin-bottom: 8px; color: #00d9ff; font-weight: 500; }
        input, select, textarea { width: 100%; padding: 12px; background: #16213e; border: 1px solid #2a2a4a; border-radius: 4px; color: #eee; font-size: 1em; }
        input:focus, select:focus, textarea:focus { outline: none; border-color: #00d9ff; }
        .checkbox-group { display: flex; flex-wrap: wrap; gap: 10px; }
        .checkbox-item { display: flex; align-items: center; gap: 8px; background: #16213e; padding: 10px 15px; border-radius: 4px; cursor: pointer; }
        .checkbox-item:hover { background: #1a1a3e; }
        .checkbox-item input { width: auto; }
        .toggle { display: flex; align-items: center; gap: 10px; }
        .toggle input { width: auto; }
        .btn { display: inline-block; padding: 12px 24px; background: #00d9ff; color: #1a1a2e; border: none; border-radius: 4px; cursor: pointer; font-weight: bold; font-size: 1em; }
        .btn:hover { background: #00b8d9; }
        .btn-secondary { background: #4a4a6a; color: #eee; }
        .actions { display: flex; gap: 10px; margin-top: 30px; }
        .error { color: #ff4757; margin-top: 10px; }
        .success { color: #00d97f; margin-top: 10px; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Create New Subscription</h1>

        <form id="subscription-form">
            <div class="form-group">
                <label>Data Types</label>
                <div class="checkbox-group" id="data-types">
                    <label class="checkbox-item"><input type="checkbox" name="dataTypes" value="OMM.fbs"> OMM</label>
                    <label class="checkbox-item"><input type="checkbox" name="dataTypes" value="CDM.fbs"> CDM</label>
                    <label class="checkbox-item"><input type="checkbox" name="dataTypes" value="EPM.fbs"> EPM</label>
                    <label class="checkbox-item"><input type="checkbox" name="dataTypes" value="OEM.fbs"> OEM</label>
                    <label class="checkbox-item"><input type="checkbox" name="dataTypes" value="TDM.fbs"> TDM</label>
                    <label class="checkbox-item"><input type="checkbox" name="dataTypes" value="CAT.fbs"> CAT</label>
                    <label class="checkbox-item"><input type="checkbox" name="dataTypes" value="VCM.fbs"> VCM</label>
                    <label class="checkbox-item"><input type="checkbox" name="dataTypes" value="PNM.fbs"> PNM</label>
                </div>
            </div>

            <div class="form-group">
                <label for="sourcePeers">Source Peers (comma-separated, or "all")</label>
                <input type="text" id="sourcePeers" name="sourcePeers" value="all" placeholder="all, or peer1, peer2">
            </div>

            <div class="form-group">
                <div class="toggle">
                    <input type="checkbox" id="encrypted" name="encrypted" checked>
                    <label for="encrypted">Receive encrypted data</label>
                </div>
            </div>

            <div class="form-group">
                <div class="toggle">
                    <input type="checkbox" id="streaming" name="streaming" checked>
                    <label for="streaming">Real-time streaming mode</label>
                </div>
            </div>

            <div class="form-group">
                <label for="rateLimit">Rate Limit (messages per minute, 0 = unlimited)</label>
                <input type="number" id="rateLimit" name="rateLimit" value="1000" min="0">
            </div>

            <div class="form-group">
                <label for="ttl">Message TTL (milliseconds, 0 = default)</label>
                <input type="number" id="ttl" name="ttl" value="86400000" min="0">
            </div>

            <div id="message"></div>

            <div class="actions">
                <button type="submit" class="btn">Create Subscription</button>
                <a href="/admin/subscriptions" class="btn btn-secondary">Cancel</a>
            </div>
        </form>
    </div>

    <script>
        document.getElementById('subscription-form').addEventListener('submit', async (e) => {
            e.preventDefault();

            const form = e.target;
            const dataTypes = Array.from(form.querySelectorAll('input[name="dataTypes"]:checked')).map(cb => cb.value);
            const sourcePeers = form.sourcePeers.value.split(',').map(s => s.trim()).filter(Boolean);

            if (dataTypes.length === 0) {
                document.getElementById('message').innerHTML = '<p class="error">Please select at least one data type.</p>';
                return;
            }

            const body = {
                dataTypes,
                sourcePeers,
                encrypted: form.encrypted.checked,
                streaming: form.streaming.checked,
                rateLimit: parseInt(form.rateLimit.value) || 0,
                ttl: parseInt(form.ttl.value) || 0
            };

            try {
                const res = await fetch('/api/subscriptions', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(body)
                });

                if (res.ok) {
                    document.getElementById('message').innerHTML = '<p class="success">Subscription created successfully! Redirecting...</p>';
                    setTimeout(() => window.location.href = '/admin/subscriptions', 1000);
                } else {
                    const err = await res.json();
                    document.getElementById('message').innerHTML = ` + "`" + `<p class="error">${err.error}</p>` + "`" + `;
                }
            } catch (err) {
                document.getElementById('message').innerHTML = ` + "`" + `<p class="error">Error: ${err.message}</p>` + "`" + `;
            }
        });
    </script>
</body>
</html>`

// MetricsCollector collects and exposes subscription metrics
type MetricsCollector struct {
	manager *Manager
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector(manager *Manager) *MetricsCollector {
	return &MetricsCollector{manager: manager}
}

// CollectMetrics returns current metrics
func (mc *MetricsCollector) CollectMetrics() map[string]interface{} {
	subs := mc.manager.ListSubscriptions()

	metrics := map[string]interface{}{
		"subscriptions_total":  len(subs),
		"subscriptions_active": 0,
		"subscriptions_paused": 0,
		"messages_total":       int64(0),
		"schemas":              make(map[string]int),
	}

	for _, sub := range subs {
		switch sub.Status {
		case StatusActive:
			metrics["subscriptions_active"] = metrics["subscriptions_active"].(int) + 1
		case StatusPaused:
			metrics["subscriptions_paused"] = metrics["subscriptions_paused"].(int) + 1
		}

		metrics["messages_total"] = metrics["messages_total"].(int64) + sub.MessageCount

		schemas := metrics["schemas"].(map[string]int)
		for _, dt := range sub.Config.DataTypes {
			schemas[dt]++
		}
	}

	return metrics
}

// PrometheusMetrics returns Prometheus-formatted metrics
func (mc *MetricsCollector) PrometheusMetrics() string {
	metrics := mc.CollectMetrics()

	var sb strings.Builder

	sb.WriteString("# HELP sdn_subscriptions_total Total number of subscriptions\n")
	sb.WriteString("# TYPE sdn_subscriptions_total gauge\n")
	sb.WriteString("sdn_subscriptions_total " + strconv.Itoa(metrics["subscriptions_total"].(int)) + "\n")

	sb.WriteString("# HELP sdn_subscriptions_active Active subscriptions\n")
	sb.WriteString("# TYPE sdn_subscriptions_active gauge\n")
	sb.WriteString("sdn_subscriptions_active " + strconv.Itoa(metrics["subscriptions_active"].(int)) + "\n")

	sb.WriteString("# HELP sdn_subscriptions_paused Paused subscriptions\n")
	sb.WriteString("# TYPE sdn_subscriptions_paused gauge\n")
	sb.WriteString("sdn_subscriptions_paused " + strconv.Itoa(metrics["subscriptions_paused"].(int)) + "\n")

	sb.WriteString("# HELP sdn_messages_total Total messages received\n")
	sb.WriteString("# TYPE sdn_messages_total counter\n")
	sb.WriteString("sdn_messages_total " + strconv.FormatInt(metrics["messages_total"].(int64), 10) + "\n")

	sb.WriteString("# HELP sdn_subscriptions_by_schema Subscriptions per schema type\n")
	sb.WriteString("# TYPE sdn_subscriptions_by_schema gauge\n")
	for schema, count := range metrics["schemas"].(map[string]int) {
		sb.WriteString("sdn_subscriptions_by_schema{schema=\"" + schema + "\"} " + strconv.Itoa(count) + "\n")
	}

	return sb.String()
}
