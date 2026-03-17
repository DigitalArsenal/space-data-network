package ailogplugin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

var log = logging.Logger("ailog")

const (
	ID = "spaceaware-ai-log"

	// Dashboard UUID — change to revoke access.
	dashboardUUID = "eaf6c52a-40f1-462d-aa22-b22f015b74f2"

	maxEntries = 50_000
)

// QueryEntry is a single logged AI query.
type QueryEntry struct {
	Timestamp   string `json:"ts"`
	IP          string `json:"ip"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Mode        string `json:"mode"`
	Query       string `json:"query"`
	SQL         string `json:"sql,omitempty"`
	ToolCalls   any    `json:"toolCalls,omitempty"`
	Explanation string `json:"explanation,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Plugin implements the SDN plugin interface for AI query diagnostics.
type Plugin struct {
	mu      sync.RWMutex
	entries []QueryEntry
	logFile *os.File
}

// New returns a new unstarted AI log plugin.
func New() *Plugin {
	return &Plugin{
		entries: make([]QueryEntry, 0, 1024),
	}
}

func (p *Plugin) ID() string { return ID }

func (p *Plugin) Start(_ context.Context, runtime plugins.RuntimeContext) error {
	logPath := os.Getenv("AI_LOG_PATH")
	if logPath == "" {
		logPath = runtime.BaseDataPath + "/ai-queries.ndjson"
	}

	// Load existing entries from NDJSON file.
	if data, err := os.ReadFile(logPath); err == nil {
		for _, line := range splitLines(data) {
			var entry QueryEntry
			if json.Unmarshal(line, &entry) == nil {
				p.entries = append(p.entries, entry)
			}
		}
		if len(p.entries) > maxEntries {
			p.entries = p.entries[len(p.entries)-maxEntries:]
		}
		log.Infof("Loaded %d existing AI query log entries", len(p.entries))
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Warnf("Cannot open log file %s: %v (logging to memory only)", logPath, err)
	} else {
		p.logFile = f
	}

	return nil
}

func (p *Plugin) RegisterRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("/diag/"+dashboardUUID, p.handleDashboard)
	log.Infof("AI diag dashboard: /diag/%s", dashboardUUID)
}

func (p *Plugin) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.logFile != nil {
		return p.logFile.Close()
	}
	return nil
}

// UIDescriptor makes the plugin visible on the SDN Plugins page.
func (p *Plugin) UIDescriptor() plugins.UIDescriptor {
	return plugins.UIDescriptor{
		Title:       "AI Query Log",
		Description: "Diagnostic logging for AI-powered satellite queries",
		Icon:        "🤖",
		Color:       "#1e3a5f",
		TextColor:   "#e0e0e0",
		URL:         "/diag/" + dashboardUUID,
	}
}

// LogEntry appends a query entry from an internal caller (e.g. WebSocket protocol handler).
func (p *Plugin) LogEntry(entry QueryEntry) {
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	p.mu.Lock()
	p.entries = append(p.entries, entry)
	if len(p.entries) > maxEntries {
		p.entries = p.entries[len(p.entries)-maxEntries:]
	}
	if p.logFile != nil {
		line, _ := json.Marshal(entry)
		p.logFile.Write(append(line, '\n'))
	}
	p.mu.Unlock()
}

// handleDashboard serves a JSON or HTML dashboard of logged queries.
func (p *Plugin) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	p.mu.RLock()
	grouped := groupByIP(p.entries)
	p.mu.RUnlock()

	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(grouped)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(buildDashboardHTML(grouped)))
}

// --- helpers ---

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func groupByIP(entries []QueryEntry) map[string][]QueryEntry {
	grouped := make(map[string][]QueryEntry)
	for _, e := range entries {
		grouped[e.IP] = append(grouped[e.IP], e)
	}
	return grouped
}

func buildDashboardHTML(grouped map[string][]QueryEntry) string {
	html := `<!DOCTYPE html><html><head><meta charset="utf-8"><title>AI Query Log</title>
<style>
body{background:#0a0a0f;color:#c8d0dc;font-family:system-ui,sans-serif;margin:2rem}
h1{color:#7eb8f0}h2{color:#5a9fd4;margin-top:2rem}
table{border-collapse:collapse;width:100%;margin-bottom:1rem}
th,td{border:1px solid #2a2e3a;padding:6px 10px;text-align:left;font-size:13px}
th{background:#14161e;color:#8ecaff}
tr:nth-child(even){background:#11131a}
.error{color:#f87171}
.count{color:#94a3b8;font-size:14px}
</style></head><body><h1>AI Query Diagnostics</h1>`

	for ip, entries := range grouped {
		html += `<h2>` + ip + ` <span class="count">(` + itoa(len(entries)) + ` queries)</span></h2>`
		html += `<table><tr><th>Time</th><th>Provider</th><th>Model</th><th>Mode</th><th>Query</th><th>SQL</th><th>Error</th></tr>`
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			errClass := ""
			if e.Error != "" {
				errClass = ` class="error"`
			}
			html += `<tr><td>` + e.Timestamp + `</td><td>` + e.Provider + `</td><td>` + e.Model +
				`</td><td>` + e.Mode + `</td><td>` + truncate(e.Query, 120) +
				`</td><td>` + truncate(e.SQL, 120) + `</td><td` + errClass + `>` + truncate(e.Error, 100) + `</td></tr>`
		}
		html += `</table>`
	}

	html += `</body></html>`
	return html
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
