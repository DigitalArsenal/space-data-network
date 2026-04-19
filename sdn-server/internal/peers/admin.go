// Package peers provides trusted peer registry and management for the SDN.
package peers

import (
	"html/template"
	"net/http"
)

// AdminUI provides the admin web interface for peer management.
type AdminUI struct {
	apiHandler    *APIHandler
	templates     *template.Template
	mux           *http.ServeMux
	walletJSFile  string
	walletCSSFile string
}

// AdminTemplateData is passed to the admin template.
type AdminTemplateData struct {
	WalletJSFile  string
	WalletCSSFile string
}

// NewAdminUI creates a new admin UI handler.
func NewAdminUI(registry *Registry, gater *TrustedConnectionGater) (*AdminUI, error) {
	tmpl := template.Must(template.New("admin").Parse(adminTemplate))

	ui := &AdminUI{
		apiHandler: NewAPIHandler(registry, gater),
		templates:  tmpl,
		mux:        http.NewServeMux(),
	}

	ui.setupRoutes()
	return ui, nil
}

// SetWalletAssets sets the wallet-ui JS and CSS file names for the admin template.
func (ui *AdminUI) SetWalletAssets(jsFile, cssFile string) {
	ui.walletJSFile = jsFile
	ui.walletCSSFile = cssFile
}

// ServeHTTP implements http.Handler.
func (ui *AdminUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ui.mux.ServeHTTP(w, r)
}

func (ui *AdminUI) setupRoutes() {
	ui.mux.Handle("/api/", ui.apiHandler)
	ui.mux.HandleFunc("/admin", ui.handleAdmin)
	ui.mux.HandleFunc("/admin/", ui.handleAdmin)
}

func (ui *AdminUI) handleAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	ui.templates.ExecuteTemplate(w, "admin", AdminTemplateData{
		WalletJSFile:  ui.walletJSFile,
		WalletCSSFile: ui.walletCSSFile,
	})
}

const adminTemplate = `<!DOCTYPE html>
{{define "admin"}}
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>SDN Admin</title>
<style>
:root {
  --navy:       #041727;
  --navy-mid:   #0d2137;
  --navy-light: #244166;
  --aqua:       #69c4cd;
  --aqua-dim:   #3d8a93;
  --white:      #ffffff;
  --text:       #b7c5cf;
  --text-dim:   #6b8090;
  --border:     #1c3347;
  --green:      #3fb950;
  --yellow:     #d29922;
  --red:        #f85149;
  --purple:     #a371f7;
  --bg-card:    #0a1e30;
  --bg-input:   #0d2137;
  --radius:     6px;
  --sidebar-w:  72px;
  --topbar-h:   52px;
}
*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
html, body { height: 100%; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  background: var(--navy);
  color: var(--text);
  font-size: 14px;
  line-height: 1.5;
  display: flex;
  flex-direction: column;
}

/* ── Top bar ────────────────────────────────────────────────────────── */
#topbar {
  position: fixed; top: 0; left: 0; right: 0; height: var(--topbar-h);
  background: var(--navy-mid);
  border-bottom: 1px solid var(--border);
  display: flex; align-items: center; gap: 8px; padding: 0 12px;
  z-index: 200;
}
#topbar .logo {
  display: flex; align-items: center; gap: 8px;
  color: var(--white); font-weight: 700; font-size: 13px;
  letter-spacing: .05em; text-transform: uppercase; text-decoration: none;
  flex-shrink: 0;
}
#topbar .logo svg { flex-shrink: 0; }
#topbar .spacer { flex: 1; }
.mode-switch {
  display: flex; align-items: center; gap: 6px;
  background: var(--navy); border: 1px solid var(--border);
  border-radius: 20px; padding: 3px; flex-shrink: 0;
}
.mode-btn {
  border: none; cursor: pointer; padding: 4px 12px; border-radius: 16px;
  font-size: 12px; font-weight: 600; transition: all .15s;
  background: transparent; color: var(--text-dim);
}
.mode-btn.active { background: var(--navy-light); color: var(--white); }
#topbar-btns { display: flex; align-items: center; gap: 6px; }
.tb-btn {
  background: var(--navy); border: 1px solid var(--border); border-radius: var(--radius);
  color: var(--text); cursor: pointer; padding: 5px 10px; font-size: 12px;
  display: flex; align-items: center; gap: 5px; transition: border-color .15s;
  white-space: nowrap;
}
.tb-btn:hover { border-color: var(--aqua); color: var(--white); }
.tb-btn.accent { background: var(--aqua-dim); border-color: var(--aqua); color: var(--white); }
#peer-count-badge {
  background: var(--navy); border: 1px solid var(--border); border-radius: 12px;
  padding: 3px 10px; font-size: 12px; color: var(--aqua); flex-shrink: 0;
}

/* ── Layout ─────────────────────────────────────────────────────────── */
#layout {
  display: flex; height: 100vh; padding-top: var(--topbar-h);
}

/* ── Sidebar ─────────────────────────────────────────────────────────── */
#sidebar {
  width: var(--sidebar-w); flex-shrink: 0;
  background: var(--navy-mid);
  border-right: 1px solid var(--border);
  display: flex; flex-direction: column; align-items: center;
  padding: 12px 0; gap: 4px; overflow-y: auto;
  position: fixed; top: var(--topbar-h); bottom: 0; left: 0;
  z-index: 100;
}
.nav-item {
  width: 56px; display: flex; flex-direction: column; align-items: center;
  gap: 3px; padding: 8px 4px; border-radius: var(--radius); cursor: pointer;
  color: var(--text-dim); transition: all .15s; border: none; background: transparent;
  font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: .04em;
}
.nav-item:hover { background: var(--navy-light); color: var(--white); }
.nav-item.active { background: var(--navy-light); color: var(--aqua); }
.nav-item svg { width: 22px; height: 22px; }
.nav-sep { width: 36px; height: 1px; background: var(--border); margin: 4px 0; }

/* ── Main content ────────────────────────────────────────────────────── */
#main {
  flex: 1; margin-left: var(--sidebar-w);
  overflow-y: auto; padding: 20px 24px;
}

/* ── Cards / panels ──────────────────────────────────────────────────── */
.panel {
  background: var(--bg-card); border: 1px solid var(--border);
  border-radius: 8px; padding: 16px; margin-bottom: 16px;
}
.panel-title {
  font-size: 13px; font-weight: 700; color: var(--white);
  text-transform: uppercase; letter-spacing: .06em; margin-bottom: 14px;
  display: flex; align-items: center; justify-content: space-between;
}
.grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
.grid-3 { display: grid; grid-template-columns: repeat(3, 1fr); gap: 14px; }
.grid-4 { display: grid; grid-template-columns: repeat(4, 1fr); gap: 14px; }
@media (max-width: 800px) { .grid-3, .grid-4 { grid-template-columns: 1fr 1fr; } }
.stat-card {
  background: var(--navy-mid); border: 1px solid var(--border);
  border-radius: 8px; padding: 14px 18px; text-align: center;
}
.stat-value { font-size: 28px; font-weight: 700; color: var(--aqua); }
.stat-label { font-size: 11px; color: var(--text-dim); text-transform: uppercase; letter-spacing: .05em; margin-top: 2px; }

/* ── Status dot ──────────────────────────────────────────────────────── */
.dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; flex-shrink: 0; }
.dot-green { background: var(--green); }
.dot-yellow { background: var(--yellow); }
.dot-red { background: var(--red); }
.dot-dim { background: var(--text-dim); }

/* ── Badges ──────────────────────────────────────────────────────────── */
.badge {
  display: inline-flex; align-items: center; padding: 2px 9px;
  border-radius: 12px; font-size: 11px; font-weight: 600; text-transform: uppercase;
}
.badge-admin   { background: var(--purple);      color: #fff; }
.badge-trusted { background: var(--green);        color: #fff; }
.badge-standard{ background: var(--navy-light);   color: var(--text); border: 1px solid var(--border); }
.badge-limited { background: var(--yellow);       color: #000; }
.badge-blocked { background: var(--red);          color: #fff; }
.badge-aqua    { background: var(--aqua-dim);     color: #fff; }

/* ── Tables ──────────────────────────────────────────────────────────── */
table { width: 100%; border-collapse: collapse; }
th {
  text-align: left; padding: 8px 10px;
  font-size: 11px; font-weight: 600; color: var(--text-dim);
  text-transform: uppercase; letter-spacing: .05em;
  border-bottom: 1px solid var(--border);
}
td { padding: 10px 10px; border-bottom: 1px solid var(--border); vertical-align: middle; }
tr:last-child td { border-bottom: none; }
tr:hover td { background: rgba(255,255,255,.02); }
.mono { font-family: 'SFMono-Regular', Consolas, monospace; font-size: 11px; color: var(--aqua); }
.truncate { max-width: 180px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* ── Buttons ─────────────────────────────────────────────────────────── */
.btn {
  padding: 7px 14px; border-radius: var(--radius); border: 1px solid var(--border);
  cursor: pointer; font-size: 13px; font-weight: 500; transition: all .15s;
  background: var(--navy-mid); color: var(--text); display: inline-flex;
  align-items: center; gap: 5px;
}
.btn:hover { border-color: var(--aqua); color: var(--white); }
.btn-primary { background: var(--aqua-dim); border-color: var(--aqua); color: var(--white); }
.btn-primary:hover { background: var(--aqua); }
.btn-danger { background: #2a0f0f; border-color: var(--red); color: var(--red); }
.btn-danger:hover { background: var(--red); color: #fff; }
.btn-sm { padding: 4px 9px; font-size: 12px; }

/* ── Inputs ──────────────────────────────────────────────────────────── */
input, select, textarea {
  background: var(--bg-input); border: 1px solid var(--border); border-radius: var(--radius);
  padding: 8px 11px; color: var(--text); font-size: 13px; width: 100%;
}
input:focus, select:focus, textarea:focus { outline: none; border-color: var(--aqua); }
.form-row { margin-bottom: 12px; }
.form-label { display: block; font-size: 11px; color: var(--text-dim); margin-bottom: 4px; text-transform: uppercase; letter-spacing: .04em; }
.form-row-inline { display: flex; gap: 8px; align-items: flex-end; }
.form-row-inline input, .form-row-inline select { flex: 1; }

/* ── Search bar ──────────────────────────────────────────────────────── */
.search-bar {
  display: flex; gap: 8px; align-items: center; margin-bottom: 14px;
}
.search-bar input { flex: 1; }

/* ── Modals ──────────────────────────────────────────────────────────── */
.modal-overlay {
  display: none; position: fixed; inset: 0;
  background: rgba(0,0,0,.65); z-index: 500;
  justify-content: center; align-items: center;
}
.modal-overlay.open { display: flex; }
.modal-box {
  background: var(--navy-mid); border: 1px solid var(--border);
  border-radius: 10px; padding: 22px 24px; width: 100%; max-width: 480px;
  max-height: 90vh; overflow-y: auto;
}
.modal-box.wide { max-width: 640px; }
.modal-head {
  display: flex; justify-content: space-between; align-items: center;
  margin-bottom: 16px;
}
.modal-head h2 { font-size: 16px; color: var(--white); }
.modal-close {
  background: none; border: none; color: var(--text-dim); font-size: 20px;
  cursor: pointer; line-height: 1; padding: 2px 6px; border-radius: 4px;
}
.modal-close:hover { color: var(--white); background: var(--navy); }
.modal-foot { display: flex; gap: 8px; justify-content: flex-end; margin-top: 16px; }

/* ── Timeline ────────────────────────────────────────────────────────── */
.timeline { display: flex; flex-direction: column; gap: 0; }
.tl-item {
  display: flex; gap: 12px; padding: 10px 0; position: relative;
}
.tl-item::before {
  content: ''; position: absolute; left: 15px; top: 32px; bottom: -10px;
  width: 2px; background: var(--border);
}
.tl-item:last-child::before { display: none; }
.tl-icon {
  width: 30px; height: 30px; border-radius: 50%; display: flex; align-items: center;
  justify-content: center; flex-shrink: 0; border: 2px solid var(--border);
  background: var(--navy-mid); font-size: 13px; z-index: 1;
}
.tl-icon.done   { border-color: var(--green);  background: #0d2a14; }
.tl-icon.active { border-color: var(--aqua);   background: #0a2232; }
.tl-icon.fail   { border-color: var(--red);    background: #2a0f0f; }
.tl-icon.wait   { border-color: var(--border); opacity: .5; }
.tl-body { flex: 1; padding-top: 5px; }
.tl-title { font-weight: 600; color: var(--white); font-size: 13px; }
.tl-meta  { font-size: 11px; color: var(--text-dim); margin-top: 2px; }
.tl-detail {
  margin-top: 6px; font-size: 11px; font-family: monospace;
  background: var(--navy); border: 1px solid var(--border);
  border-radius: 4px; padding: 6px 8px; color: var(--aqua);
  word-break: break-all;
}

/* ── Store / module browser ──────────────────────────────────────────── */
.store-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; }
.store-card {
  background: var(--navy-mid); border: 1px solid var(--border); border-radius: 8px;
  padding: 14px; cursor: pointer; transition: border-color .15s;
  display: flex; flex-direction: column; gap: 8px;
}
.store-card:hover { border-color: var(--aqua); }
.store-card-title { font-weight: 700; color: var(--white); font-size: 13px; }
.store-card-desc  { font-size: 12px; color: var(--text-dim); flex: 1; }
.store-card-meta  { font-size: 11px; color: var(--text-dim); display: flex; justify-content: space-between; }
.store-card-price { color: var(--aqua); font-weight: 600; }
.empty-state {
  text-align: center; padding: 60px 20px; color: var(--text-dim);
}
.empty-state-icon { font-size: 48px; margin-bottom: 12px; opacity: .4; }
.empty-state-title { font-size: 16px; color: var(--text); margin-bottom: 6px; }
.empty-state-desc { font-size: 13px; }

/* ── Workspace ───────────────────────────────────────────────────────── */
#workspace-layout {
  display: flex; gap: 0; height: calc(100vh - var(--topbar-h) - 60px);
  min-height: 400px; border: 1px solid var(--border); border-radius: 8px; overflow: hidden;
}
#file-tree {
  width: 220px; flex-shrink: 0; border-right: 1px solid var(--border);
  background: var(--navy-mid); overflow-y: auto; display: flex; flex-direction: column;
}
#file-tree-header {
  padding: 8px 10px; font-size: 11px; font-weight: 700; color: var(--text-dim);
  text-transform: uppercase; letter-spacing: .06em; border-bottom: 1px solid var(--border);
  display: flex; align-items: center; justify-content: space-between; flex-shrink: 0;
}
.file-item {
  padding: 5px 10px; font-size: 12px; cursor: pointer; display: flex; gap: 6px;
  align-items: center; color: var(--text); transition: background .1s; white-space: nowrap;
  overflow: hidden; text-overflow: ellipsis;
}
.file-item:hover { background: var(--navy-light); }
.file-item.active { background: var(--navy-light); color: var(--aqua); }
#editor-area { flex: 1; display: flex; flex-direction: column; overflow: hidden; }
#editor-toolbar {
  padding: 6px 10px; border-bottom: 1px solid var(--border); background: var(--navy-mid);
  display: flex; gap: 8px; align-items: center; flex-shrink: 0; flex-wrap: wrap;
}
#editor-toolbar span { font-size: 11px; color: var(--text-dim); flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; }
#monaco-container { flex: 1; overflow: hidden; }
#code-editor-fallback {
  flex: 1; resize: none; border: none; border-radius: 0;
  font-family: 'SFMono-Regular', Consolas, monospace; font-size: 13px;
  background: #060f18; color: #b7c5cf; padding: 10px;
  width: 100%; height: 100%;
}

/* ── Event log ───────────────────────────────────────────────────────── */
.event-log {
  font-family: 'SFMono-Regular', Consolas, monospace; font-size: 11px;
  background: #060f18; border: 1px solid var(--border); border-radius: 6px;
  padding: 10px; max-height: 280px; overflow-y: auto; color: var(--text);
  line-height: 1.7;
}
.event-log .ev-ts  { color: var(--text-dim); }
.event-log .ev-ok  { color: var(--green); }
.event-log .ev-err { color: var(--red); }
.event-log .ev-info{ color: var(--aqua); }

/* ── Node lookup ─────────────────────────────────────────────────────── */
.lookup-result {
  margin-top: 12px; padding: 12px; background: var(--navy-mid);
  border: 1px solid var(--border); border-radius: var(--radius);
  font-size: 12px; line-height: 2;
}

/* ── Peers / AD view ─────────────────────────────────────────────────── */
#ad-layout { display: flex; gap: 14px; }
#ad-tree {
  width: 200px; flex-shrink: 0; background: var(--bg-card);
  border: 1px solid var(--border); border-radius: 8px;
  overflow-y: auto; max-height: 600px;
}
#ad-content { flex: 1; }
.ad-section { padding: 6px 10px; font-size: 11px; font-weight: 700; color: var(--text-dim); text-transform: uppercase; letter-spacing: .06em; }
.ad-item {
  padding: 6px 14px; font-size: 12px; cursor: pointer; display: flex; gap: 6px;
  align-items: center; color: var(--text); transition: background .1s;
}
.ad-item:hover { background: var(--navy-light); }
.ad-item.active { background: var(--navy-light); color: var(--aqua); }

/* ── Sightings feed ──────────────────────────────────────────────────── */
.sighting-item {
  display: flex; gap: 10px; align-items: flex-start;
  padding: 8px 0; border-bottom: 1px solid var(--border); font-size: 12px;
}
.sighting-item:last-child { border-bottom: none; }
.sighting-ts { color: var(--text-dim); white-space: nowrap; flex-shrink: 0; font-size: 11px; padding-top: 1px; }

/* ── Toggle switch ───────────────────────────────────────────────────── */
.toggle-wrap { display: flex; align-items: center; gap: 10px; }
.toggle-box { position: relative; width: 44px; height: 24px; }
.toggle-box input { opacity: 0; width: 0; height: 0; }
.toggle-slider {
  position: absolute; inset: 0; background: var(--navy);
  border: 1px solid var(--border); border-radius: 12px; cursor: pointer; transition: .2s;
}
.toggle-slider::before {
  content: ''; position: absolute; width: 16px; height: 16px;
  background: var(--text-dim); border-radius: 50%; top: 3px; left: 3px; transition: .2s;
}
.toggle-box input:checked + .toggle-slider { background: var(--aqua-dim); border-color: var(--aqua); }
.toggle-box input:checked + .toggle-slider::before { transform: translateX(20px); background: var(--white); }

/* ── Misc ────────────────────────────────────────────────────────────── */
.page { display: none; }
.page.active { display: block; }
.row { display: flex; gap: 10px; align-items: center; }
.row-between { display: flex; justify-content: space-between; align-items: center; }
.gap-2 { gap: 8px; }
.mt-2 { margin-top: 8px; }
.mt-3 { margin-top: 12px; }
.text-dim { color: var(--text-dim); }
.text-white { color: var(--white); }
.text-aqua  { color: var(--aqua); }
.text-green { color: var(--green); }
.text-red   { color: var(--red); }
.text-sm    { font-size: 12px; }
.text-xs    { font-size: 11px; }
.fw-7 { font-weight: 700; }
.spinner {
  display: inline-block; width: 14px; height: 14px;
  border: 2px solid var(--border); border-top-color: var(--aqua);
  border-radius: 50%; animation: spin .7s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }
hr.sep { border: none; border-top: 1px solid var(--border); margin: 14px 0; }
</style>
{{if .WalletCSSFile}}<link rel="stylesheet" href="/wallet-ui/{{.WalletCSSFile}}">{{end}}
<script>
// Auto-discover wallet-ui assets at runtime if not injected by server
(function(){
  {{if not .WalletCSSFile}}
  fetch('/wallet-ui/').then(r=>r.text()).then(html=>{
    var css = html.match(/href="([^"]*\.css)"/);
    var js  = html.match(/src="([^"]*\.js)"/);
    if(css && css[1]){ var l=document.createElement('link'); l.rel='stylesheet'; l.href='/wallet-ui/'+css[1]; document.head.appendChild(l); }
    if(js  && js[1]) { var s=document.createElement('script'); s.src='/wallet-ui/'+js[1]; document.head.appendChild(s); }
  }).catch(()=>{});
  {{end}}
})();
</script>
</head>
<body>

<!-- ── Top bar ────────────────────────────────────────────────────── -->
<div id="topbar">
  <a class="logo" href="#status">
    <svg width="28" height="28" viewBox="0 0 28 28" fill="none">
      <circle cx="14" cy="14" r="13" stroke="#69c4cd" stroke-width="1.5"/>
      <path d="M7 14 A7 7 0 0 1 21 14" stroke="#69c4cd" stroke-width="1.5" fill="none"/>
      <circle cx="14" cy="10" r="3" fill="#69c4cd"/>
      <circle cx="8"  cy="18" r="2" fill="#244166" stroke="#69c4cd" stroke-width="1"/>
      <circle cx="20" cy="18" r="2" fill="#244166" stroke="#69c4cd" stroke-width="1"/>
    </svg>
    <span>Space Data Network</span>
  </a>
  <div class="spacer"></div>
  <div class="mode-switch">
    <button class="mode-btn active" id="btn-mode-local"  onclick="setMode('local')">Local</button>
    <button class="mode-btn"        id="btn-mode-server" onclick="setMode('server')">Server</button>
  </div>
  <div id="topbar-btns">
    <div id="peer-count-badge"><span id="peer-count">—</span> peers</div>
    <button class="tb-btn" id="btn-connect" onclick="openModal('modal-connect')" title="Connect to SDN node">
      <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor"><path d="M1 3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v2H1V3Zm0 4h6v6H2a1 1 0 0 1-1-1V7Zm8 0h6v5a1 1 0 0 1-1 1H9V7Z"/></svg>
      Connect
    </button>
    <button class="tb-btn" id="btn-account" onclick="openModal('modal-account')" title="Account / Identity">
      <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor"><path d="M8 8a3 3 0 1 0 0-6 3 3 0 0 0 0 6Zm2 1H6C3.79 9 2 10.79 2 13v1h12v-1c0-2.21-1.79-4-4-4Z"/></svg>
      Account
    </button>
    <a class="tb-btn" href="/webui/" target="_blank" title="IPFS Dashboard">
      <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor"><path d="M8 1a7 7 0 1 0 0 14A7 7 0 0 0 8 1Zm1 10H7V7h2v4Zm0-6H7V3h2v2Z"/></svg>
      IPFS
    </a>
  </div>
</div>

<!-- ── Layout ──────────────────────────────────────────────────────── -->
<div id="layout">

<!-- Sidebar -->
<nav id="sidebar">
  <button class="nav-item active" data-page="status" onclick="nav('status',this)">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/></svg>
    Overview
  </button>
  <button class="nav-item" data-page="peers" onclick="nav('peers',this)">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="9" cy="7" r="3"/><path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2"/><circle cx="18" cy="8" r="2.5"/><path d="M21 21v-1.5a3.5 3.5 0 0 0-3-3.47"/></svg>
    Peers
  </button>
  <button class="nav-item" data-page="store" onclick="nav('store',this)">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M6 2 3 6v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V6l-3-4z"/><line x1="3" y1="6" x2="21" y2="6"/><path d="M16 10a4 4 0 0 1-8 0"/></svg>
    Store
  </button>
  <button class="nav-item" data-page="delivery" onclick="nav('delivery',this)">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><line x1="8" y1="6" x2="21" y2="6"/><line x1="8" y1="12" x2="21" y2="12"/><line x1="8" y1="18" x2="21" y2="18"/><line x1="3" y1="6" x2="3.01" y2="6"/><line x1="3" y1="12" x2="3.01" y2="12"/><line x1="3" y1="18" x2="3.01" y2="18"/></svg>
    Delivery
  </button>
  <button class="nav-item" data-page="workspace" onclick="nav('workspace',this)">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg>
    Workspace
  </button>
  <div class="nav-sep"></div>
  <button class="nav-item" data-page="users" onclick="nav('users',this)">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>
    Users
  </button>
  <button class="nav-item" data-page="settings" onclick="nav('settings',this)">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
    Settings
  </button>
</nav>

<!-- Main content -->
<main id="main">

<!-- ══ STATUS PAGE ══════════════════════════════════════════════════ -->
<div id="page-status" class="page active">
  <div class="grid-4" style="margin-bottom:16px">
    <div class="stat-card"><div class="stat-value" id="stat-peers">—</div><div class="stat-label">Connected Peers</div></div>
    <div class="stat-card"><div class="stat-value" id="stat-listings">—</div><div class="stat-label">Store Listings</div></div>
    <div class="stat-card"><div class="stat-value" id="stat-uptime">—</div><div class="stat-label">Uptime</div></div>
    <div class="stat-card"><div class="stat-value" id="stat-relay">—</div><div class="stat-label">Relay Load</div></div>
  </div>
  <div class="grid-2">
    <div class="panel">
      <div class="panel-title">Node Identity</div>
      <div id="node-info-content"><div class="spinner"></div></div>
    </div>
    <div class="panel">
      <div class="panel-title">
        Recent Peer Sightings
        <button class="btn btn-sm" onclick="fetchPeerGraph()">Refresh</button>
      </div>
      <div id="sightings-feed"><div class="text-dim text-sm">Loading…</div></div>
    </div>
  </div>
  <div class="panel mt-3">
    <div class="panel-title row-between">
      Live Events
      <button class="btn btn-sm" onclick="clearLog()">Clear</button>
    </div>
    <div class="event-log" id="event-log"></div>
  </div>
</div>

<!-- ══ PEERS PAGE ═══════════════════════════════════════════════════ -->
<div id="page-peers" class="page">
  <div id="ad-layout">
    <div id="ad-tree">
      <div class="ad-section">Directory</div>
      <div class="ad-item active" data-ad="all" onclick="adSelect('all',this)">
        <span>🌐</span> All Peers
      </div>
      <div class="ad-item" data-ad="trusted" onclick="adSelect('trusted',this)">
        <span>✅</span> Trusted
      </div>
      <div class="ad-item" data-ad="admin" onclick="adSelect('admin',this)">
        <span>🔑</span> Admins
      </div>
      <div class="ad-item" data-ad="standard" onclick="adSelect('standard',this)">
        <span>👤</span> Standard
      </div>
      <div class="ad-item" data-ad="limited" onclick="adSelect('limited',this)">
        <span>⚠️</span> Limited
      </div>
      <div class="ad-item" data-ad="blocked" onclick="adSelect('blocked',this)">
        <span>🚫</span> Blocked
      </div>
    </div>
    <div id="ad-content">
      <div class="panel">
        <div class="panel-title row-between">
          <span id="ad-title">All Peers</span>
          <div class="row gap-2">
            <input id="peer-search" placeholder="Search peer ID or name…" style="width:220px" oninput="renderPeerTable()">
            <button class="btn btn-primary btn-sm" onclick="openModal('modal-add-peer')">+ Add Peer</button>
          </div>
        </div>
        <table>
          <thead><tr>
            <th>Peer ID</th><th>Name</th><th>Trust</th><th>Addresses</th><th>Actions</th>
          </tr></thead>
          <tbody id="peer-tbody"><tr><td colspan="5" class="text-dim">Loading…</td></tr></tbody>
        </table>
      </div>
      <div class="panel mt-3">
        <div class="panel-title row-between">
          Groups
          <button class="btn btn-primary btn-sm" onclick="openModal('modal-add-group')">+ Add Group</button>
        </div>
        <div id="groups-list"><div class="text-dim text-sm">Loading…</div></div>
      </div>
    </div>
  </div>
  <!-- Node lookup by crypto address -->
  <div class="panel mt-3">
    <div class="panel-title">Node Lookup by Crypto Address / Peer ID</div>
    <div class="form-row-inline">
      <input id="lookup-input" placeholder="Peer ID (12D3Koo…) or xpub…">
      <button class="btn btn-primary" onclick="doLookup()">Lookup</button>
    </div>
    <div id="lookup-result"></div>
  </div>
</div>

<!-- ══ STORE PAGE ═══════════════════════════════════════════════════ -->
<div id="page-store" class="page">
  <div class="panel">
    <div class="panel-title row-between">
      Module Store
      <div class="row gap-2">
        <input id="store-search" placeholder="Search modules…" style="width:200px" oninput="filterStore()">
        <select id="store-filter" style="width:140px" onchange="filterStore()">
          <option value="">All types</option>
          <option value="OneTime">One-Time</option>
          <option value="Subscription">Subscription</option>
          <option value="Streaming">Streaming</option>
          <option value="Free">Free</option>
        </select>
      </div>
    </div>
    <div id="store-grid-wrap"></div>
  </div>
</div>

<!-- ══ DELIVERY PAGE ════════════════════════════════════════════════ -->
<div id="page-delivery" class="page">
  <div class="grid-2">
    <div class="panel">
      <div class="panel-title row-between">
        Module Delivery Timeline
        <button class="btn btn-primary btn-sm" onclick="openModal('modal-new-delivery')">New Delivery</button>
      </div>
      <div class="timeline" id="delivery-timeline">
        <!-- rendered by JS -->
      </div>
    </div>
    <div class="panel">
      <div class="panel-title">Provider Descriptor</div>
      <div id="provider-detail"><div class="spinner"></div></div>
    </div>
  </div>
  <div class="panel mt-3">
    <div class="panel-title row-between">
      Raw Technical Events
      <button class="btn btn-sm" onclick="clearEventDetail()">Clear</button>
    </div>
    <div class="event-log" id="event-detail-log"></div>
  </div>
</div>

<!-- ══ WORKSPACE PAGE ═══════════════════════════════════════════════ -->
<div id="page-workspace" class="page">
  <!-- Top row: preview + file tree side by side -->
  <div class="grid-2" style="margin-bottom:14px" ondragover="event.preventDefault()" ondrop="handleDrop(event)">
    <!-- Site preview iframe -->
    <div class="panel" style="display:flex;flex-direction:column;min-height:360px">
      <div class="panel-title row-between" style="margin-bottom:8px">
        Preview
        <button class="btn btn-sm" onclick="reloadPreview()" title="Reload preview">↻</button>
      </div>
      <iframe id="preview-frame" src="/" style="flex:1;width:100%;min-height:300px;border:none;border-radius:4px;background:#fff" title="Site preview"></iframe>
    </div>
    <!-- File tree -->
    <div class="panel" style="display:flex;flex-direction:column;min-height:360px">
      <div class="panel-title row-between" style="margin-bottom:8px">
        Files
        <div class="row gap-2">
          <button class="btn btn-sm" onclick="fetchFiles()">↻</button>
          <button class="btn btn-sm" onclick="openModal('modal-new-file')">+ New</button>
          <label class="btn btn-sm" style="cursor:pointer">
            Upload<input type="file" multiple style="display:none" onchange="uploadFiles(this)">
          </label>
          <button class="btn btn-sm" onclick="openModal('modal-git-import')">Git</button>
        </div>
      </div>
      <div id="file-list" style="flex:1;overflow-y:auto"><div class="text-dim text-sm" style="padding:10px">Loading…</div></div>
    </div>
  </div>
  <!-- Editor panel — hidden until a file is opened -->
  <div id="editor-panel" class="panel" style="display:none">
    <div class="panel-title row-between" style="margin-bottom:8px">
      <span id="editor-filename" class="mono text-aqua"></span>
      <div class="row gap-2">
        <button class="btn btn-sm btn-primary" onclick="saveFile()" id="btn-save">Save</button>
        <button class="btn btn-sm" onclick="closeEditor()" id="btn-close-editor">Close</button>
      </div>
    </div>
    <div id="monaco-container" style="height:420px;display:none"></div>
    <textarea id="code-editor-fallback" spellcheck="false" style="height:420px;display:block"></textarea>
  </div>
  <!-- Git identity -->
  <div class="panel mt-3">
    <div class="panel-title">Git SSH Identity (deterministic from HD wallet)</div>
    <div class="text-dim text-sm" style="margin-bottom:8px">Signing key path: m/44'/0'/0'/0'/0' (Ed25519)</div>
    <div class="row gap-2">
      <button class="btn btn-sm" onclick="showGitKey()">Show Public Key</button>
      <button class="btn btn-sm" onclick="copyGitKey()">Copy</button>
    </div>
    <div id="git-key-display" style="display:none;margin-top:8px">
      <div class="event-log" id="git-key-content" style="max-height:80px"></div>
    </div>
  </div>
</div>

<!-- ══ USERS PAGE ═══════════════════════════════════════════════════ -->
<div id="page-users" class="page">
  <div class="panel">
    <div class="panel-title row-between">
      User Roster
      <button class="btn btn-primary btn-sm" onclick="openModal('modal-add-user')">+ Add User</button>
    </div>
    <table>
      <thead><tr><th>xPub (Identity)</th><th>Name</th><th>Role</th><th>Actions</th></tr></thead>
      <tbody id="user-tbody"><tr><td colspan="4" class="text-dim">Loading…</td></tr></tbody>
    </table>
  </div>
</div>

<!-- ══ SETTINGS PAGE ════════════════════════════════════════════════ -->
<div id="page-settings" class="page">
  <div class="panel">
    <div class="panel-title">Connection</div>
    <div class="form-row">
      <label class="form-label">Mode</label>
      <div class="mode-switch" style="justify-content:flex-start">
        <button class="mode-btn active" id="settings-mode-local"  onclick="setMode('local')">Local (Helia)</button>
        <button class="mode-btn"        id="settings-mode-server" onclick="setMode('server')">Remote Server</button>
      </div>
    </div>
    <div id="settings-server-url-row" class="form-row" style="display:none">
      <label class="form-label">Server URL</label>
      <div class="form-row-inline">
        <input id="settings-server-url" placeholder="https://my-sdn-node.example.com">
        <button class="btn btn-primary" onclick="saveServerUrl()">Save</button>
      </div>
    </div>
  </div>
  <div class="panel mt-3">
    <div class="panel-title">Peer Registry Settings</div>
    <div id="registry-settings"><div class="spinner"></div></div>
  </div>
  <div class="panel mt-3">
    <div class="panel-title">Blocklist</div>
    <div id="blocklist-content"><div class="spinner"></div></div>
    <div class="form-row-inline mt-2">
      <input id="block-input" placeholder="Peer ID to block…">
      <button class="btn btn-danger" onclick="blockPeer()">Block</button>
    </div>
  </div>
</div>

</main><!-- /main -->
</div><!-- /layout -->

<!-- ═══════════ MODALS ══════════════════════════════════════════════ -->

<!-- Connect to server -->
<div class="modal-overlay" id="modal-connect">
  <div class="modal-box">
    <div class="modal-head"><h2>Connect to SDN Node</h2><button class="modal-close" onclick="closeModal('modal-connect')">&times;</button></div>
    <div class="form-row">
      <label class="form-label">Server URL</label>
      <input id="connect-url" placeholder="https://my-sdn-node.example.com">
    </div>
    <div id="connect-status" class="text-sm mt-2"></div>
    <div class="modal-foot">
      <button class="btn" onclick="closeModal('modal-connect')">Cancel</button>
      <button class="btn btn-primary" onclick="connectToServer()">Connect</button>
    </div>
  </div>
</div>

<!-- Wallet UI mount — always present so the wallet JS can initialize -->
<div id="wallet-ui-mount" style="display:none;position:absolute"></div>

<!-- Account / Identity modal -->
<div class="modal-overlay" id="modal-account">
  <div class="modal-box wide">
    <div class="modal-head"><h2>Account &amp; Identity</h2><button class="modal-close" onclick="closeModal('modal-account')">&times;</button></div>
    <div id="account-content"><div class="spinner"></div></div>
    <div id="wallet-ui-inline" class="mt-3"></div>
    <div class="modal-foot">
      <button class="btn btn-danger" onclick="signOut()">Sign Out</button>
      <button class="btn" onclick="closeModal('modal-account')">Close</button>
    </div>
  </div>
</div>

<!-- Add peer -->
<div class="modal-overlay" id="modal-add-peer">
  <div class="modal-box">
    <div class="modal-head"><h2>Add Peer</h2><button class="modal-close" onclick="closeModal('modal-add-peer')">&times;</button></div>
    <div class="form-row"><label class="form-label">Peer ID *</label><input id="new-peer-id" placeholder="12D3KooW…"></div>
    <div class="form-row"><label class="form-label">Name</label><input id="new-peer-name" placeholder="Friendly name"></div>
    <div class="form-row"><label class="form-label">Organization</label><input id="new-peer-org" placeholder="Org name"></div>
    <div class="form-row"><label class="form-label">Addresses (one per line)</label><textarea id="new-peer-addrs" rows="3" placeholder="/ip4/…/tcp/4001"></textarea></div>
    <div class="form-row"><label class="form-label">Trust Level</label>
      <select id="new-peer-trust"><option value="standard">Standard</option><option value="limited">Limited</option><option value="trusted">Trusted</option><option value="admin">Admin</option></select>
    </div>
    <div class="form-row"><label class="form-label">Notes</label><textarea id="new-peer-notes" rows="2"></textarea></div>
    <div class="modal-foot">
      <button class="btn" onclick="closeModal('modal-add-peer')">Cancel</button>
      <button class="btn btn-primary" onclick="addPeer()">Add</button>
    </div>
  </div>
</div>

<!-- Add group -->
<div class="modal-overlay" id="modal-add-group">
  <div class="modal-box">
    <div class="modal-head"><h2>Add Group</h2><button class="modal-close" onclick="closeModal('modal-add-group')">&times;</button></div>
    <div class="form-row"><label class="form-label">Name *</label><input id="new-group-name" placeholder="e.g. satellite-operators"></div>
    <div class="form-row"><label class="form-label">Description</label><input id="new-group-desc" placeholder="Optional description"></div>
    <div class="form-row"><label class="form-label">Default Trust</label>
      <select id="new-group-trust"><option value="standard">Standard</option><option value="limited">Limited</option><option value="trusted">Trusted</option></select>
    </div>
    <div class="modal-foot">
      <button class="btn" onclick="closeModal('modal-add-group')">Cancel</button>
      <button class="btn btn-primary" onclick="addGroup()">Create</button>
    </div>
  </div>
</div>

<!-- Add user -->
<div class="modal-overlay" id="modal-add-user">
  <div class="modal-box">
    <div class="modal-head"><h2>Add User</h2><button class="modal-close" onclick="closeModal('modal-add-user')">&times;</button></div>
    <div class="form-row"><label class="form-label">xPub *</label><input id="new-user-xpub" placeholder="xpub…"></div>
    <div class="form-row"><label class="form-label">Name</label><input id="new-user-name" placeholder="Display name"></div>
    <div class="form-row"><label class="form-label">Role</label>
      <select id="new-user-role"><option value="user">User</option><option value="admin">Admin</option></select>
    </div>
    <div class="modal-foot">
      <button class="btn" onclick="closeModal('modal-add-user')">Cancel</button>
      <button class="btn btn-primary" onclick="addUser()">Add</button>
    </div>
  </div>
</div>

<!-- New delivery -->
<div class="modal-overlay" id="modal-new-delivery">
  <div class="modal-box">
    <div class="modal-head"><h2>Initiate Module Delivery</h2><button class="modal-close" onclick="closeModal('modal-new-delivery')">&times;</button></div>
    <div class="form-row"><label class="form-label">Listing ID</label><input id="delivery-listing-id" placeholder="listing CID or ID…"></div>
    <div class="form-row"><label class="form-label">Provider Peer ID (optional)</label><input id="delivery-peer-id" placeholder="12D3KooW…"></div>
    <div class="modal-foot">
      <button class="btn" onclick="closeModal('modal-new-delivery')">Cancel</button>
      <button class="btn btn-primary" onclick="startDelivery()">Start</button>
    </div>
  </div>
</div>

<!-- New file -->
<div class="modal-overlay" id="modal-new-file">
  <div class="modal-box">
    <div class="modal-head"><h2>New File</h2><button class="modal-close" onclick="closeModal('modal-new-file')">&times;</button></div>
    <div class="form-row"><label class="form-label">Path</label><input id="new-file-path" placeholder="index.html"></div>
    <div class="modal-foot">
      <button class="btn" onclick="closeModal('modal-new-file')">Cancel</button>
      <button class="btn btn-primary" onclick="createFile()">Create</button>
    </div>
  </div>
</div>

<!-- Git import -->
<div class="modal-overlay" id="modal-git-import">
  <div class="modal-box">
    <div class="modal-head"><h2>Git Import</h2><button class="modal-close" onclick="closeModal('modal-git-import')">&times;</button></div>
    <div class="form-row"><label class="form-label">Repository URL</label><input id="git-url" placeholder="https://github.com/…"></div>
    <div class="form-row"><label class="form-label">Branch</label><input id="git-branch" placeholder="main"></div>
    <div class="modal-foot">
      <button class="btn" onclick="closeModal('modal-git-import')">Cancel</button>
      <button class="btn btn-primary" id="btn-git-import" onclick="doGitImport()">Import</button>
    </div>
  </div>
</div>

<!-- ═══════════ SCRIPTS ═════════════════════════════════════════════ -->
{{if .WalletJSFile}}<script src="/wallet-ui/{{.WalletJSFile}}"></script>{{end}}

<script>
// ── State ──────────────────────────────────────────────────────────
const S = {
  mode: localStorage.getItem('sdn_mode') || 'local',
  serverUrl: localStorage.getItem('sdn_server_url') || '',
  peers: [],
  adFilter: 'all',
  storeListings: [],
  files: [],
  currentFile: null,
  currentFileContent: '',
  deliveryPhases: [],
  editor: null,  // Monaco instance
};

// ── API helper ──────────────────────────────────────────────────────
function apiBase() {
  return S.mode === 'server' && S.serverUrl ? S.serverUrl.replace(/\/$/, '') : '';
}
async function apiFetch(path, opts) {
  const base = apiBase();
  const headers = Object.assign({ 'X-Requested-With': 'XMLHttpRequest' }, (opts && opts.headers) || {});
  try {
    const r = await fetch(base + path, Object.assign({}, opts, { headers }));
    return r;
  } catch(e) {
    logEvent('err', path + ': ' + e.message);
    throw e;
  }
}

// ── Navigation ──────────────────────────────────────────────────────
function nav(page, btn) {
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  document.querySelectorAll('.nav-item').forEach(b => b.classList.remove('active'));
  const el = document.getElementById('page-' + page);
  if (el) el.classList.add('active');
  if (btn) btn.classList.add('active');
  onPageShow(page);
}

function onPageShow(page) {
  if (page === 'status')    { fetchNodeInfo(); fetchPeerGraph(); fetchProviderDesc(); fetchRelayStatus(); }
  if (page === 'peers')     { fetchPeers(); fetchGroups(); }
  if (page === 'store')     { fetchStore(); }
  if (page === 'delivery')  { renderDeliveryTimeline(); fetchProviderDetail(); }
  if (page === 'workspace') { fetchFiles(); }
  if (page === 'users')     { fetchUsers(); }
  if (page === 'settings')  { fetchSettings(); fetchBlocklist(); renderSettingsMode(); }
}

// ── Mode switch ─────────────────────────────────────────────────────
function setMode(m) {
  S.mode = m;
  localStorage.setItem('sdn_mode', m);
  ['local','server'].forEach(x => {
    const a = document.getElementById('btn-mode-' + x);
    const b = document.getElementById('settings-mode-' + x);
    if (a) a.classList.toggle('active', x === m);
    if (b) b.classList.toggle('active', x === m);
  });
  const row = document.getElementById('settings-server-url-row');
  if (row) row.style.display = m === 'server' ? '' : 'none';
  logEvent('info', 'Mode: ' + m + (m === 'server' && S.serverUrl ? ' → ' + S.serverUrl : ''));
}

function saveServerUrl() {
  const v = document.getElementById('settings-server-url').value.trim();
  S.serverUrl = v;
  localStorage.setItem('sdn_server_url', v);
  logEvent('info', 'Server URL saved: ' + v);
}

function renderSettingsMode() {
  setMode(S.mode);
  const inp = document.getElementById('settings-server-url');
  if (inp) inp.value = S.serverUrl;
}

// ── Connect to server modal ─────────────────────────────────────────
async function connectToServer() {
  const url = document.getElementById('connect-url').value.trim().replace(/\/$/, '');
  const st = document.getElementById('connect-status');
  if (!url) { st.innerHTML = '<span class="text-red">Enter a server URL</span>'; return; }
  st.innerHTML = '<span class="spinner"></span> Connecting…';
  try {
    const r = await fetch(url + '/api/node/info', { headers: { 'X-Requested-With': 'XMLHttpRequest' } });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const data = await r.json();
    S.serverUrl = url;
    localStorage.setItem('sdn_server_url', url);
    setMode('server');
    closeModal('modal-connect');
    logEvent('ok', 'Connected to ' + url + ' — PeerID: ' + (data.peer_id || data.id || '?'));
    fetchNodeInfo();
  } catch(e) {
    st.innerHTML = '<span class="text-red">Failed: ' + e.message + '</span>';
  }
}

// ── Node info ───────────────────────────────────────────────────────
async function fetchNodeInfo() {
  try {
    const r = await apiFetch('/api/node/info');
    if (!r.ok) { setNodeInfoError(r.status); return; }
    const d = await r.json();
    renderNodeInfo(d);
  } catch(e) {
    setNodeInfoError(e.message);
  }
}
function renderNodeInfo(d) {
  const el = document.getElementById('node-info-content');
  if (!el) return;
  const row = (k, v) => '<div style="display:flex;gap:8px;padding:4px 0;border-bottom:1px solid var(--border)"><span style="color:var(--text-dim);width:120px;flex-shrink:0;font-size:12px">' + k + '</span><span class="mono truncate">' + (v || '—') + '</span></div>';
  el.innerHTML = [
    row('Peer ID',  d.peer_id || d.id),
    row('Version',  d.version),
    row('Uptime',   d.uptime || d.uptime_seconds),
    row('Addresses', (d.addresses || d.listen_addresses || []).join(', ')),
    row('Network',  d.network),
  ].join('');
  if (d.uptime) document.getElementById('stat-uptime').textContent = fmtDuration(d.uptime);
  if (d.peer_id) document.getElementById('stat-peers').textContent = d.connected_peers ?? '—';
}
function setNodeInfoError(e) {
  const el = document.getElementById('node-info-content');
  if (el) el.innerHTML = '<span class="text-dim text-sm">Unavailable (' + e + ')</span>';
}

// ── Provider descriptor ─────────────────────────────────────────────
async function fetchProviderDesc() {
  try {
    const r = await apiFetch('/api/module-delivery/provider');
    if (!r.ok) { ['provider-desc'].forEach(id => setEl(id,'<span class="text-dim text-sm">Unavailable</span>')); return; }
    const d = await r.json();
    renderProviderDesc('provider-desc', d);
  } catch(e) {}
}
async function fetchProviderDetail() {
  try {
    const r = await apiFetch('/api/module-delivery/provider');
    if (!r.ok) { setEl('provider-detail','<span class="text-dim text-sm">Unavailable</span>'); return; }
    const d = await r.json();
    renderProviderDesc('provider-detail', d);
  } catch(e) {}
}
function renderProviderDesc(id, d) {
  const el = document.getElementById(id);
  if (!el) return;
  const row = (k, v) => '<div style="display:flex;gap:8px;padding:4px 0;border-bottom:1px solid var(--border)"><span style="color:var(--text-dim);width:140px;flex-shrink:0;font-size:12px">' + k + '</span><span class="text-sm">' + (v ?? '—') + '</span></div>';
  el.innerHTML = [
    row('Provider ID',    d.provider_id || d.peer_id),
    row('Capabilities',   (d.capabilities || []).join(', ')),
    row('Protocols',      (d.protocols || []).join(', ')),
    row('Max Concurrent', d.max_concurrent_deliveries),
    row('Challenge Type', d.challenge_type),
    row('Grant TTL',      d.grant_ttl),
    row('Region',         d.region),
  ].join('') + '<details class="mt-2"><summary class="text-xs text-dim" style="cursor:pointer">Raw JSON</summary><pre class="event-log" style="margin-top:6px;white-space:pre-wrap">' + JSON.stringify(d, null, 2) + '</pre></details>';
}

// ── Relay status ────────────────────────────────────────────────────
async function fetchRelayStatus() {
  try {
    const r = await apiFetch('/api/relay/status');
    if (!r.ok) return;
    const d = await r.json();
    const el = document.getElementById('stat-relay');
    if (el) el.textContent = d.load != null ? Math.round(d.load * 100) + '%' : (d.status || '?');
  } catch(e) {}
}

// ── Peer graph / sightings ──────────────────────────────────────────
async function fetchPeerGraph() {
  try {
    const r = await apiFetch('/api/peers/graph');
    if (!r.ok) { setEl('sightings-feed','<div class="text-dim text-sm">Unavailable</div>'); return; }
    const d = await r.json();
    renderSightings(d);
    const peers = d.nodes || d.peers || [];
    document.getElementById('peer-count').textContent = peers.length;
    document.getElementById('stat-peers').textContent = peers.length;
  } catch(e) {
    setEl('sightings-feed','<div class="text-dim text-sm">Error: ' + e.message + '</div>');
  }
}
function renderSightings(d) {
  const feed = document.getElementById('sightings-feed');
  if (!feed) return;
  const nodes = (d.nodes || d.peers || []).slice(0, 20);
  if (!nodes.length) { feed.innerHTML = '<div class="text-dim text-sm">No peers observed</div>'; return; }
  feed.innerHTML = nodes.map(n => {
    const pid = (n.id || n.peer_id || '').substring(0, 20) + '…';
    const ts  = n.last_seen ? new Date(n.last_seen).toLocaleTimeString() : 'now';
    const trust = n.trust || n.trust_level || 'unknown';
    return '<div class="sighting-item">' +
      '<span class="dot dot-' + dotClass(trust) + '"></span>' +
      '<span class="sighting-ts">' + ts + '</span>' +
      '<span class="mono truncate">' + pid + '</span>' +
      '<span class="badge badge-' + trust + '" style="margin-left:auto;flex-shrink:0">' + trust + '</span>' +
    '</div>';
  }).join('');
}
function dotClass(t) {
  return { admin:'green', trusted:'green', standard:'dim', limited:'yellow', blocked:'red' }[t] || 'dim';
}

// ── Peers ───────────────────────────────────────────────────────────
async function fetchPeers() {
  try {
    const r = await apiFetch('/api/v1/admin/peers');
    if (!r.ok) { S.peers = []; renderPeerTable(); return; }
    const d = await r.json();
    S.peers = d.peers || d || [];
    renderPeerTable();
  } catch(e) { S.peers = []; renderPeerTable(); }
}
function adSelect(filter, btn) {
  S.adFilter = filter;
  document.querySelectorAll('.ad-item').forEach(b => b.classList.remove('active'));
  if (btn) btn.classList.add('active');
  document.getElementById('ad-title').textContent =
    { all:'All Peers', trusted:'Trusted', admin:'Admins', standard:'Standard', limited:'Limited', blocked:'Blocked' }[filter] || filter;
  renderPeerTable();
}
function renderPeerTable() {
  const q = (document.getElementById('peer-search') || {}).value || '';
  const tbody = document.getElementById('peer-tbody');
  if (!tbody) return;
  let rows = S.peers;
  if (S.adFilter !== 'all') rows = rows.filter(p => (p.trust || p.trust_level) === S.adFilter);
  if (q) rows = rows.filter(p => JSON.stringify(p).toLowerCase().includes(q.toLowerCase()));
  if (!rows.length) {
    tbody.innerHTML = '<tr><td colspan="5"><div class="empty-state" style="padding:30px"><div class="empty-state-icon">👥</div><div class="empty-state-title">No peers</div><div class="empty-state-desc">No peers match this filter.</div></div></td></tr>';
    return;
  }
  tbody.innerHTML = rows.map(p => {
    const trust = p.trust || p.trust_level || 'standard';
    const pid   = p.peer_id || p.id || '';
    const addrs = (p.addresses || []).join(', ') || '—';
    return '<tr>' +
      '<td><span class="mono truncate" title="' + pid + '">' + pid.substring(0,16) + '…</span></td>' +
      '<td>' + (p.name || p.display_name || '—') + '</td>' +
      '<td><span class="badge badge-' + trust + '">' + trust + '</span></td>' +
      '<td class="text-xs text-dim truncate" style="max-width:160px" title="' + addrs + '">' + addrs + '</td>' +
      '<td><button class="btn btn-sm btn-danger" onclick="removePeer(\'' + pid + '\')">Remove</button></td>' +
    '</tr>';
  }).join('');
}
async function addPeer() {
  const body = {
    peer_id:   document.getElementById('new-peer-id').value.trim(),
    name:      document.getElementById('new-peer-name').value.trim(),
    org:       document.getElementById('new-peer-org').value.trim(),
    addresses: document.getElementById('new-peer-addrs').value.trim().split('\n').filter(Boolean),
    trust:     document.getElementById('new-peer-trust').value,
    notes:     document.getElementById('new-peer-notes').value.trim(),
  };
  if (!body.peer_id) { alert('Peer ID required'); return; }
  const r = await apiFetch('/api/v1/admin/peers', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body) });
  if (r.ok) { closeModal('modal-add-peer'); fetchPeers(); logEvent('ok', 'Peer added: ' + body.peer_id); }
  else { alert('Failed: ' + await r.text()); }
}
async function removePeer(pid) {
  if (!confirm('Remove peer ' + pid + '?')) return;
  const r = await apiFetch('/api/v1/admin/peers/' + encodeURIComponent(pid), { method:'DELETE' });
  if (r.ok) { fetchPeers(); logEvent('ok', 'Peer removed: ' + pid); }
}

// ── Node lookup ──────────────────────────────────────────────────────
async function doLookup() {
  const q = document.getElementById('lookup-input').value.trim();
  const el = document.getElementById('lookup-result');
  if (!q) return;
  el.innerHTML = '<div class="spinner"></div>';
  // Try local peer list first
  const match = S.peers.find(p => (p.peer_id||p.id) === q || (p.xpub || '') === q);
  if (match) {
    el.innerHTML = '<div class="lookup-result">' + renderPeerDetail(match) + '</div>';
    return;
  }
  // Try EPM lookup
  try {
    const r = await apiFetch('/api/node/epm/json');
    if (r.ok) {
      const d = await r.json();
      if (d.peer_id === q) { el.innerHTML = '<div class="lookup-result">' + renderPeerDetail(d) + '</div>'; return; }
    }
  } catch(e) {}
  el.innerHTML = '<div class="lookup-result text-dim">No result found for: ' + q + '</div>';
}
function renderPeerDetail(p) {
  const pairs = Object.entries(p).map(([k,v]) => '<div><strong style="color:var(--text-dim)">' + k + ':</strong> ' + JSON.stringify(v) + '</div>');
  return pairs.join('');
}

// ── Groups ───────────────────────────────────────────────────────────
async function fetchGroups() {
  try {
    const r = await apiFetch('/api/v1/admin/groups');
    const el = document.getElementById('groups-list');
    if (!el) return;
    if (!r.ok) { el.innerHTML = '<div class="text-dim text-sm">Unavailable</div>'; return; }
    const d = await r.json();
    const groups = d.groups || d || [];
    if (!groups.length) { el.innerHTML = '<div class="text-dim text-sm">No groups defined</div>'; return; }
    el.innerHTML = groups.map(g =>
      '<div class="row row-between" style="padding:8px 0;border-bottom:1px solid var(--border)">' +
      '<div><strong>' + g.name + '</strong>' + (g.description ? ' <span class="text-dim text-sm">– ' + g.description + '</span>' : '') + '</div>' +
      '<div class="row gap-2">' +
      '<span class="badge badge-' + (g.trust||'standard') + '">' + (g.trust||'standard') + '</span>' +
      '<button class="btn btn-sm btn-danger" onclick="removeGroup(\'' + g.name + '\')">Delete</button>' +
      '</div></div>'
    ).join('');
  } catch(e) {
    setEl('groups-list', '<div class="text-dim text-sm">Error: ' + e.message + '</div>');
  }
}
async function addGroup() {
  const body = {
    name:        document.getElementById('new-group-name').value.trim(),
    description: document.getElementById('new-group-desc').value.trim(),
    trust:       document.getElementById('new-group-trust').value,
  };
  if (!body.name) { alert('Name required'); return; }
  const r = await apiFetch('/api/v1/admin/groups', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body) });
  if (r.ok) { closeModal('modal-add-group'); fetchGroups(); }
  else { alert('Failed: ' + await r.text()); }
}
async function removeGroup(name) {
  if (!confirm('Delete group ' + name + '?')) return;
  const r = await apiFetch('/api/v1/admin/groups/' + encodeURIComponent(name), { method:'DELETE' });
  if (r.ok) fetchGroups();
}

// ── Store ────────────────────────────────────────────────────────────
async function fetchStore() {
  const wrap = document.getElementById('store-grid-wrap');
  if (!wrap) return;
  wrap.innerHTML = '<div class="spinner"></div>';
  try {
    const r = await apiFetch('/api/storefront/listings');
    if (r.status === 404 || r.status === 501) {
      S.storeListings = [];
      renderStore();
      return;
    }
    if (!r.ok) {
      wrap.innerHTML = '<div class="empty-state"><div class="empty-state-icon">🛒</div><div class="empty-state-title">Store unavailable</div><div class="empty-state-desc">Could not load listings (HTTP ' + r.status + ')</div></div>';
      return;
    }
    const d = await r.json();
    S.storeListings = d.listings || d || [];
    document.getElementById('stat-listings').textContent = S.storeListings.length;
    renderStore();
  } catch(e) {
    wrap.innerHTML = '<div class="empty-state"><div class="empty-state-icon">🛒</div><div class="empty-state-title">Store unavailable</div><div class="empty-state-desc">' + e.message + '</div></div>';
  }
}
function filterStore() { renderStore(); }
function renderStore() {
  const wrap = document.getElementById('store-grid-wrap');
  if (!wrap) return;
  const q  = (document.getElementById('store-search') || {}).value || '';
  const ft = (document.getElementById('store-filter') || {}).value || '';
  let items = S.storeListings.slice();
  if (q)  items = items.filter(l => JSON.stringify(l).toLowerCase().includes(q.toLowerCase()));
  if (ft) items = items.filter(l => (l.access_type || '') === ft);
  if (!items.length) {
    wrap.innerHTML =
      '<div class="empty-state">' +
      '<div class="empty-state-icon">📦</div>' +
      '<div class="empty-state-title">' + (S.storeListings.length ? 'No matching modules' : 'No modules listed yet') + '</div>' +
      '<div class="empty-state-desc">' + (S.storeListings.length ? 'Try a different filter.' : 'Publish a signed PLG to list your first module.') + '</div>' +
      '</div>';
    return;
  }
  wrap.innerHTML = '<div class="store-grid">' + items.map(l => {
    const price = l.access_type === 'Free' ? 'Free' : (l.price ? l.price + ' ' + (l.payment_currency || 'SDN') : '—');
    return '<div class="store-card" onclick="viewListing(\'' + (l.id || l.listing_id) + '\')">' +
      '<div class="store-card-title">' + (l.title || l.name || 'Unnamed') + '</div>' +
      '<div class="store-card-desc">' + (l.description || '').substring(0, 90) + '</div>' +
      '<div class="store-card-meta">' +
      '<span class="badge badge-aqua">' + (l.access_type || 'OneTime') + '</span>' +
      '<span class="store-card-price">' + price + '</span>' +
      '</div>' +
    '</div>';
  }).join('') + '</div>';
}
function viewListing(id) {
  logEvent('info', 'Viewing listing: ' + id);
}

// ── Delivery timeline ────────────────────────────────────────────────
const PHASES = [
  { id:'discover',  label:'Provider Discovery',    icon:'🔍', desc:'Scanning DHT and known peers for module provider' },
  { id:'connect',   label:'Connect to Provider',   icon:'🔗', desc:'Establishing libp2p connection' },
  { id:'challenge', label:'Challenge',             icon:'🔐', desc:'Provider issues authentication challenge' },
  { id:'grant',     label:'Grant',                 icon:'✅', desc:'Challenge verified; access grant issued' },
  { id:'fetch',     label:'Encrypted CID Fetch',   icon:'📥', desc:'Fetching encrypted content from IPFS' },
  { id:'unwrap',    label:'Unwrap',                icon:'📦', desc:'Decoding outer envelope' },
  { id:'decrypt',   label:'Decrypt',               icon:'🔓', desc:'Decrypting payload with derived key' },
  { id:'load',      label:'SDK Load',              icon:'⚡', desc:'Loading and verifying WASM module' },
  { id:'invoke',    label:'Invoke / Result',       icon:'🎯', desc:'Module invoked; result available' },
];

function renderDeliveryTimeline() {
  const tl = document.getElementById('delivery-timeline');
  if (!tl) return;
  if (!S.deliveryPhases.length) {
    tl.innerHTML = '<div class="text-dim text-sm" style="padding:16px 0">No active delivery. Click <strong>New Delivery</strong> to begin.</div>';
    return;
  }
  tl.innerHTML = PHASES.map((ph, i) => {
    const state = S.deliveryPhases[i] || 'wait';
    const cls   = state === 'done' ? 'done' : state === 'active' ? 'active' : state === 'fail' ? 'fail' : 'wait';
    const detail = S.deliveryPhases['detail_' + ph.id] || '';
    return '<div class="tl-item">' +
      '<div class="tl-icon ' + cls + '">' + ph.icon + '</div>' +
      '<div class="tl-body">' +
      '<div class="tl-title">' + ph.label + '</div>' +
      '<div class="tl-meta">' + ph.desc + '</div>' +
      (detail ? '<div class="tl-detail">' + detail + '</div>' : '') +
      '</div></div>';
  }).join('');
}

async function startDelivery() {
  const listingId = document.getElementById('delivery-listing-id').value.trim();
  const peerId    = document.getElementById('delivery-peer-id').value.trim();
  closeModal('modal-new-delivery');
  logEventDetail('info', 'Initiating delivery for listing: ' + (listingId || '(none)'));

  // Simulate the pipeline phases with real API hooks where available
  S.deliveryPhases = new Array(PHASES.length).fill('wait');
  renderDeliveryTimeline();
  for (let i = 0; i < PHASES.length; i++) {
    S.deliveryPhases[i] = 'active';
    renderDeliveryTimeline();
    await sleep(400);
    // Real API call for grant phase
    if (PHASES[i].id === 'grant' && listingId) {
      try {
        const r = await apiFetch('/api/storefront/purchases', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ listing_id: listingId, provider_peer_id: peerId }),
        });
        const txt = r.ok ? await r.text() : ('HTTP ' + r.status);
        S.deliveryPhases['detail_grant'] = txt.substring(0, 200);
        logEventDetail('ok', 'Grant phase: ' + txt.substring(0,100));
      } catch(e) {
        S.deliveryPhases['detail_grant'] = 'Error: ' + e.message;
        logEventDetail('err', 'Grant error: ' + e.message);
      }
    }
    S.deliveryPhases[i] = 'done';
    logEventDetail('ok', PHASES[i].label + ' complete');
    renderDeliveryTimeline();
    await sleep(200);
  }
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

// ── Workspace ────────────────────────────────────────────────────────
let monacoReady = false;
function initMonaco(onReady) {
  if (monacoReady) { if (onReady) onReady(); return; }
  if (S._monacoLoading) { if (onReady) S._monacoCallbacks.push(onReady); return; }
  S._monacoLoading = true;
  S._monacoCallbacks = onReady ? [onReady] : [];
  const CDN = 'https://cdnjs.cloudflare.com/ajax/libs/monaco-editor/0.44.0/min/vs';
  const s = document.createElement('script');
  s.src = CDN + '/loader.min.js';
  s.onload = () => {
    window.require.config({ paths: { 'vs': CDN } });
    window.require(['vs/editor/editor.main'], () => {
      window.monaco.editor.defineTheme('sdn-dark', {
        base: 'vs-dark', inherit: true, rules: [],
        colors: { 'editor.background': '#060f18', 'editor.lineHighlightBackground': '#0d2137' }
      });
      const mc = document.getElementById('monaco-container');
      if (mc) {
        S.editor = window.monaco.editor.create(mc, {
          theme: 'sdn-dark', automaticLayout: true,
          minimap: { enabled: false }, scrollBeyondLastLine: false, fontSize: 13,
          value: '',
        });
      }
      monacoReady = true;
      S._monacoLoading = false;
      S._monacoCallbacks.forEach(cb => cb());
      S._monacoCallbacks = [];
    });
  };
  s.onerror = () => { S._monacoLoading = false; monacoReady = false; };
  document.head.appendChild(s);
}

function reloadPreview() {
  const f = document.getElementById('preview-frame');
  if (f) { f.src = f.src; }
}
async function fetchFiles() {
  const list = document.getElementById('file-list');
  if (!list) return;
  try {
    const r = await apiFetch('/api/admin/frontend/files');
    if (!r.ok) { list.innerHTML = '<div class="text-dim text-sm" style="padding:10px">Unavailable</div>'; return; }
    const d = await r.json();
    S.files = d.files || d || [];
    renderFileTree();
  } catch(e) {
    list.innerHTML = '<div class="text-dim text-sm" style="padding:10px">Error: ' + e.message + '</div>';
  }
}
function renderFileTree() {
  const list = document.getElementById('file-list');
  if (!list) return;
  if (!S.files.length) { list.innerHTML = '<div class="text-dim text-sm" style="padding:10px">No files</div>'; return; }
  const ICONS = { html:'📄', js:'📜', css:'🎨', json:'{}', md:'📝', ts:'🔷', go:'🐹', wasm:'⚙️', txt:'📃' };
  list.innerHTML = S.files.map(f => {
    const name = f.path || f.name || f;
    const ext  = name.split('.').pop();
    const icon = ICONS[ext] || '📄';
    const active = S.currentFile === name ? ' active' : '';
    return '<div class="file-item' + active + '" onclick="openFile(\'' + name.replace(/'/g,"\\'") + '\')">' + icon + ' ' + name + '</div>';
  }).join('');
}
async function openFile(path) {
  S.currentFile = path;
  renderFileTree();
  const panel = document.getElementById('editor-panel');
  if (panel) panel.style.display = '';
  try {
    const r = await apiFetch('/api/admin/frontend/files/' + encodeURIComponent(path));
    const content = r.ok ? await r.text() : '';
    S.currentFileContent = content;
    setEl('editor-filename', path);
    // Try to load Monaco; fall back to textarea if it fails or is slow
    initMonaco(() => setEditorContent(path, content));
    // Also set textarea immediately so editor is usable while Monaco loads
    const ta = document.getElementById('code-editor-fallback');
    if (ta && !monacoReady) { ta.value = content; ta.style.display = ''; }
  } catch(e) { alert('Error opening file: ' + e.message); }
}
function setEditorContent(path, content) {
  const mc = document.getElementById('monaco-container');
  const ta = document.getElementById('code-editor-fallback');
  if (S.editor && mc) {
    const langMap = { js:'javascript', ts:'typescript', html:'html', css:'css', json:'json', go:'go', md:'markdown', mjs:'javascript', cjs:'javascript' };
    const lang = langMap[path.split('.').pop()] || 'plaintext';
    if (S.editor.getModel()) S.editor.getModel().dispose();
    S.editor.setModel(window.monaco.editor.createModel(content, lang));
    mc.style.display = '';
    if (ta) ta.style.display = 'none';
  } else {
    if (ta) { ta.value = content; ta.style.display = ''; }
    if (mc) mc.style.display = 'none';
  }
}
function getEditorContent() {
  if (S.editor) return S.editor.getValue();
  const ta = document.getElementById('code-editor-fallback');
  return ta ? ta.value : '';
}
async function saveFile() {
  if (!S.currentFile) return;
  const content = getEditorContent();
  const r = await apiFetch('/api/admin/frontend/files/' + encodeURIComponent(S.currentFile), {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  });
  if (r.ok) { logEvent('ok', 'Saved: ' + S.currentFile); reloadPreview(); }
  else { alert('Save failed: ' + await r.text()); }
}
function closeEditor() {
  S.currentFile = null;
  renderFileTree();
  const panel = document.getElementById('editor-panel');
  if (panel) panel.style.display = 'none';
  if (S.editor) { if (S.editor.getModel()) S.editor.getModel().dispose(); }
  else { const ta = document.getElementById('code-editor-fallback'); if (ta) ta.value = ''; }
}
async function createFile() {
  const path = document.getElementById('new-file-path').value.trim();
  if (!path) return;
  const r = await apiFetch('/api/admin/frontend/files/' + encodeURIComponent(path), {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ content: '' }),
  });
  if (r.ok) { closeModal('modal-new-file'); fetchFiles(); openFile(path); }
  else { alert('Error: ' + await r.text()); }
}
async function uploadFiles(input) {
  const form = new FormData();
  for (const f of input.files) form.append('files', f);
  const r = await apiFetch('/api/admin/frontend/upload', { method:'POST', body: form });
  if (r.ok) fetchFiles();
  else alert('Upload failed: ' + await r.text());
  input.value = '';
}
async function doGitImport() {
  const btn = document.getElementById('btn-git-import');
  btn.disabled = true; btn.textContent = 'Importing…';
  try {
    const r = await apiFetch('/api/admin/frontend/git-import', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url: document.getElementById('git-url').value, branch: document.getElementById('git-branch').value }),
    });
    if (r.ok) { closeModal('modal-git-import'); fetchFiles(); }
    else { alert('Import failed: ' + await r.text()); }
  } finally { btn.disabled = false; btn.textContent = 'Import'; }
}
function handleDrop(ev) {
  ev.preventDefault();
  const files = ev.dataTransfer.files;
  if (!files.length) return;
  const form = new FormData();
  for (const f of files) form.append('files', f);
  apiFetch('/api/admin/frontend/upload', { method:'POST', body: form }).then(r => { if (r.ok) fetchFiles(); });
}

// ── Git identity ──────────────────────────────────────────────────────
function showGitKey() {
  const box = document.getElementById('git-key-display');
  const content = document.getElementById('git-key-content');
  if (!box || !content) return;
  // Fetch identity from EPM (the signing key IS the libp2p identity)
  apiFetch('/api/node/epm/json').then(r => r.json()).then(d => {
    const key = d.signing_pubkey || d.peer_id || 'Unavailable — check node EPM';
    content.textContent = 'ssh-ed25519 ' + (d.ssh_pubkey || key) + ' sdn@' + (d.peer_id || 'node').substring(0,8);
    box.style.display = '';
  }).catch(() => {
    content.textContent = 'Unavailable';
    box.style.display = '';
  });
}
function copyGitKey() {
  const c = document.getElementById('git-key-content');
  if (c) navigator.clipboard.writeText(c.textContent);
}

// ── Users ─────────────────────────────────────────────────────────────
async function fetchUsers() {
  try {
    const r = await apiFetch('/api/v1/admin/users');
    const tbody = document.getElementById('user-tbody');
    if (!tbody) return;
    if (!r.ok) { tbody.innerHTML = '<tr><td colspan="4" class="text-dim">Unavailable (requires admin auth)</td></tr>'; return; }
    const d = await r.json();
    const users = d.users || d || [];
    if (!users.length) { tbody.innerHTML = '<tr><td colspan="4" class="text-dim">No users configured</td></tr>'; return; }
    tbody.innerHTML = users.map(u =>
      '<tr>' +
      '<td class="mono truncate">' + (u.xpub || u.id || '—').substring(0,20) + '…</td>' +
      '<td>' + (u.name || u.display_name || '—') + '</td>' +
      '<td><span class="badge badge-' + (u.role === 'admin' ? 'admin' : 'standard') + '">' + (u.role || 'user') + '</span></td>' +
      '<td><button class="btn btn-sm btn-danger" onclick="removeUser(\'' + (u.xpub||u.id) + '\')">Remove</button></td>' +
      '</tr>'
    ).join('');
  } catch(e) {
    const tbody = document.getElementById('user-tbody');
    if (tbody) tbody.innerHTML = '<tr><td colspan="4" class="text-dim">Error: ' + e.message + '</td></tr>';
  }
}
async function addUser() {
  const body = { xpub: document.getElementById('new-user-xpub').value.trim(), name: document.getElementById('new-user-name').value.trim(), role: document.getElementById('new-user-role').value };
  if (!body.xpub) { alert('xPub required'); return; }
  const r = await apiFetch('/api/v1/admin/users', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body) });
  if (r.ok) { closeModal('modal-add-user'); fetchUsers(); }
  else { alert('Failed: ' + await r.text()); }
}
async function removeUser(id) {
  if (!confirm('Remove user?')) return;
  const r = await apiFetch('/api/v1/admin/users/' + encodeURIComponent(id), { method:'DELETE' });
  if (r.ok) fetchUsers();
}

// ── Account modal ─────────────────────────────────────────────────────
async function fetchAccount() {
  const el = document.getElementById('account-content');
  if (!el) return;
  try {
    const r = await apiFetch('/api/auth/me');
    if (!r.ok) { el.innerHTML = '<div class="text-dim text-sm">Not authenticated</div>'; return; }
    const d = await r.json();
    const row = (k, v) => '<div style="padding:4px 0;border-bottom:1px solid var(--border);display:flex;gap:8px"><span style="color:var(--text-dim);width:110px;flex-shrink:0;font-size:12px">' + k + '</span><span class="mono truncate">' + (v||'—') + '</span></div>';
    el.innerHTML = row('xPub', (d.xpub||'').substring(0,24)+'…') + row('Peer ID', d.peer_id) + row('Role', d.role) + row('Name', d.name);
  } catch(e) {
    el.innerHTML = '<div class="text-dim text-sm">Unavailable</div>';
  }
}
function signOut() {
  if (!confirm('Sign out?')) return;
  apiFetch('/api/auth/logout', { method:'POST' }).then(() => { window.location.href = '/login'; });
}

// ── Settings ───────────────────────────────────────────────────────────
async function fetchSettings() {
  try {
    const r = await apiFetch('/api/v1/admin/settings');
    const el = document.getElementById('registry-settings');
    if (!el) return;
    if (!r.ok) { el.innerHTML = '<div class="text-dim text-sm">Unavailable</div>'; return; }
    const d = await r.json();
    el.innerHTML = '<pre class="event-log" style="max-height:200px;white-space:pre-wrap">' + JSON.stringify(d, null, 2) + '</pre>';
  } catch(e) {
    setEl('registry-settings', '<div class="text-dim text-sm">Error: ' + e.message + '</div>');
  }
}
async function fetchBlocklist() {
  try {
    const r = await apiFetch('/api/v1/admin/blocklist');
    const el = document.getElementById('blocklist-content');
    if (!el) return;
    if (!r.ok) { el.innerHTML = '<div class="text-dim text-sm">Unavailable</div>'; return; }
    const d = await r.json();
    const items = d.blocked || d || [];
    if (!items.length) { el.innerHTML = '<div class="text-dim text-sm">No blocked peers</div>'; return; }
    el.innerHTML = items.map(pid => '<div class="row row-between" style="padding:5px 0;border-bottom:1px solid var(--border)"><span class="mono text-sm">' + pid + '</span><button class="btn btn-sm" onclick="unblockPeer(\'' + pid + '\')">Unblock</button></div>').join('');
  } catch(e) { setEl('blocklist-content','<div class="text-dim text-sm">Error: ' + e.message + '</div>'); }
}
async function blockPeer() {
  const pid = document.getElementById('block-input').value.trim();
  if (!pid) return;
  const r = await apiFetch('/api/v1/admin/blocklist', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({ peer_id: pid }) });
  if (r.ok) { document.getElementById('block-input').value = ''; fetchBlocklist(); }
  else { alert('Failed: ' + await r.text()); }
}
async function unblockPeer(pid) {
  const r = await apiFetch('/api/v1/admin/blocklist/' + encodeURIComponent(pid), { method:'DELETE' });
  if (r.ok) fetchBlocklist();
}

// ── Event log ──────────────────────────────────────────────────────────
function logEvent(level, msg) {
  const el = document.getElementById('event-log');
  if (!el) return;
  const ts = new Date().toLocaleTimeString();
  const cls = { ok:'ev-ok', err:'ev-err', info:'ev-info' }[level] || '';
  el.innerHTML += '<div><span class="ev-ts">' + ts + '</span> <span class="' + cls + '">' + msg + '</span></div>';
  el.scrollTop = el.scrollHeight;
}
function logEventDetail(level, msg) {
  logEvent(level, msg);
  const el = document.getElementById('event-detail-log');
  if (!el) return;
  const ts = new Date().toLocaleTimeString();
  const cls = { ok:'ev-ok', err:'ev-err', info:'ev-info' }[level] || '';
  el.innerHTML += '<div><span class="ev-ts">' + ts + '</span> <span class="' + cls + '">' + msg + '</span></div>';
  el.scrollTop = el.scrollHeight;
}
function clearLog() {
  const el = document.getElementById('event-log');
  if (el) el.innerHTML = '';
}
function clearEventDetail() {
  const el = document.getElementById('event-detail-log');
  if (el) el.innerHTML = '';
}

// ── Modals ─────────────────────────────────────────────────────────────
function openModal(id) {
  const el = document.getElementById(id);
  if (el) el.classList.add('open');
  if (id === 'modal-account') fetchAccount();
}
function closeModal(id) {
  const el = document.getElementById(id);
  if (el) el.classList.remove('open');
}
document.addEventListener('click', e => {
  if (e.target.classList.contains('modal-overlay')) closeModal(e.target.id);
});
document.addEventListener('keydown', e => {
  if (e.key === 'Escape') document.querySelectorAll('.modal-overlay.open').forEach(m => m.classList.remove('open'));
});

// ── Helpers ─────────────────────────────────────────────────────────────
function setEl(id, html) { const e = document.getElementById(id); if (e) e.innerHTML = html; }
function fmtDuration(s) {
  if (typeof s === 'string') return s;
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  return h + 'h ' + m + 'm';
}

// ── Init ───────────────────────────────────────────────────────────────
setMode(S.mode);
if (S.serverUrl) document.getElementById('connect-url').value = S.serverUrl;

// Load initial page from hash
(function() {
  const hash = window.location.hash.replace('#', '') || 'status';
  const btn = document.querySelector('[data-page="' + hash + '"]');
  nav(hash, btn);
})();

window.addEventListener('hashchange', () => {
  const hash = window.location.hash.replace('#', '') || 'status';
  const btn = document.querySelector('[data-page="' + hash + '"]');
  nav(hash, btn);
});

// Polling
setInterval(() => { fetchPeerGraph(); fetchRelayStatus(); }, 30000);
setInterval(fetchNodeInfo, 60000);

logEvent('info', 'SDN Admin UI loaded');
</script>
</body>
</html>
{{end}}
`
