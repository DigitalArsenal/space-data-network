// Package peers provides trusted peer registry and management for the SDN.
package peers

import (
	"html/template"
	"net/http"
)

// AdminUI provides the admin web interface for peer management.
type AdminUI struct {
	apiHandler *APIHandler
	templates  *template.Template
	mux        *http.ServeMux
}

// NewAdminUI creates a new admin UI handler.
func NewAdminUI(registry *Registry, gater *TrustedConnectionGater) (*AdminUI, error) {
	// Use inline template (embedded templates can be added later)
	tmpl := template.Must(template.New("admin").Parse(adminTemplate))

	ui := &AdminUI{
		apiHandler: NewAPIHandler(registry, gater),
		templates:  tmpl,
		mux:        http.NewServeMux(),
	}

	ui.setupRoutes()
	return ui, nil
}

// ServeHTTP implements http.Handler.
func (ui *AdminUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ui.mux.ServeHTTP(w, r)
}

func (ui *AdminUI) setupRoutes() {
	// API routes
	ui.mux.Handle("/api/", ui.apiHandler)

	// Admin UI routes
	ui.mux.HandleFunc("/admin", ui.handleAdmin)
	ui.mux.HandleFunc("/admin/", ui.handleAdmin)
}

func (ui *AdminUI) handleAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	ui.templates.ExecuteTemplate(w, "admin", nil)
}

// Inline admin template for when embedded templates aren't available
const adminTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SDN Admin - Trusted Peer Registry</title>
    <style>
        :root {
            --bg-primary: #0d1117;
            --bg-secondary: #161b22;
            --bg-tertiary: #21262d;
            --text-primary: #c9d1d9;
            --text-secondary: #8b949e;
            --border-color: #30363d;
            --accent-blue: #58a6ff;
            --accent-green: #3fb950;
            --accent-yellow: #d29922;
            --accent-red: #f85149;
            --accent-purple: #a371f7;
        }
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, sans-serif;
            background: var(--bg-primary);
            color: var(--text-primary);
            line-height: 1.6;
        }
        .container {
            max-width: 1400px;
            margin: 0 auto;
            padding: 20px;
        }
        header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding: 20px 0;
            border-bottom: 1px solid var(--border-color);
            margin-bottom: 20px;
        }
        h1 { color: var(--text-primary); font-size: 24px; }
        h2 { color: var(--text-primary); font-size: 18px; margin-bottom: 15px; }
        .stats {
            display: flex;
            gap: 20px;
        }
        .stat-card {
            background: var(--bg-secondary);
            border: 1px solid var(--border-color);
            border-radius: 8px;
            padding: 15px 25px;
            text-align: center;
        }
        .stat-value { font-size: 28px; font-weight: bold; color: var(--accent-blue); }
        .stat-label { font-size: 12px; color: var(--text-secondary); text-transform: uppercase; }
        .main-grid {
            display: grid;
            grid-template-columns: 1fr 300px;
            gap: 20px;
        }
        .panel {
            background: var(--bg-secondary);
            border: 1px solid var(--border-color);
            border-radius: 8px;
            padding: 20px;
        }
        .panel-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 15px;
        }
        table {
            width: 100%;
            border-collapse: collapse;
        }
        th, td {
            padding: 12px;
            text-align: left;
            border-bottom: 1px solid var(--border-color);
        }
        th {
            color: var(--text-secondary);
            font-weight: 500;
            font-size: 12px;
            text-transform: uppercase;
        }
        .peer-id {
            font-family: 'SFMono-Regular', Consolas, monospace;
            font-size: 12px;
            color: var(--accent-blue);
        }
        .trust-badge {
            display: inline-block;
            padding: 4px 12px;
            border-radius: 20px;
            font-size: 11px;
            font-weight: 600;
            text-transform: uppercase;
        }
        .trust-untrusted { background: var(--accent-red); color: white; }
        .trust-limited { background: var(--accent-yellow); color: black; }
        .trust-standard { background: var(--bg-tertiary); color: var(--text-primary); border: 1px solid var(--border-color); }
        .trust-trusted { background: var(--accent-green); color: white; }
        .trust-admin { background: var(--accent-purple); color: white; }
        .btn {
            padding: 8px 16px;
            border: none;
            border-radius: 6px;
            cursor: pointer;
            font-size: 14px;
            transition: all 0.2s;
        }
        .btn-primary { background: var(--accent-blue); color: white; }
        .btn-primary:hover { background: #1f6feb; }
        .btn-danger { background: var(--accent-red); color: white; }
        .btn-danger:hover { background: #da3633; }
        .btn-small { padding: 4px 10px; font-size: 12px; }
        input, select {
            background: var(--bg-tertiary);
            border: 1px solid var(--border-color);
            border-radius: 6px;
            padding: 10px 14px;
            color: var(--text-primary);
            font-size: 14px;
            width: 100%;
            margin-bottom: 10px;
        }
        input:focus, select:focus {
            outline: none;
            border-color: var(--accent-blue);
        }
        label {
            display: block;
            font-size: 12px;
            color: var(--text-secondary);
            margin-bottom: 5px;
        }
        .form-group { margin-bottom: 15px; }
        .toggle-container {
            display: flex;
            align-items: center;
            gap: 10px;
            padding: 10px 0;
        }
        .toggle {
            position: relative;
            width: 48px;
            height: 24px;
        }
        .toggle input {
            opacity: 0;
            width: 0;
            height: 0;
        }
        .toggle-slider {
            position: absolute;
            cursor: pointer;
            top: 0; left: 0; right: 0; bottom: 0;
            background: var(--bg-tertiary);
            border-radius: 24px;
            transition: 0.3s;
        }
        .toggle-slider:before {
            position: absolute;
            content: "";
            height: 18px;
            width: 18px;
            left: 3px;
            bottom: 3px;
            background: white;
            border-radius: 50%;
            transition: 0.3s;
        }
        .toggle input:checked + .toggle-slider {
            background: var(--accent-green);
        }
        .toggle input:checked + .toggle-slider:before {
            transform: translateX(24px);
        }
        .modal {
            display: none;
            position: fixed;
            top: 0; left: 0; right: 0; bottom: 0;
            background: rgba(0,0,0,0.7);
            z-index: 1000;
            justify-content: center;
            align-items: center;
        }
        .modal.active { display: flex; }
        .modal-content {
            background: var(--bg-secondary);
            border: 1px solid var(--border-color);
            border-radius: 12px;
            padding: 25px;
            width: 100%;
            max-width: 500px;
        }
        .modal-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 20px;
        }
        .modal-close {
            background: none;
            border: none;
            color: var(--text-secondary);
            font-size: 24px;
            cursor: pointer;
        }
        .actions { display: flex; gap: 10px; }
        .empty-state {
            text-align: center;
            padding: 40px;
            color: var(--text-secondary);
        }
        .connection-indicator {
            width: 8px;
            height: 8px;
            border-radius: 50%;
            display: inline-block;
            margin-right: 8px;
        }
        .connected { background: var(--accent-green); }
        .disconnected { background: var(--text-secondary); }
        .search-bar {
            display: flex;
            gap: 10px;
            margin-bottom: 15px;
        }
        .search-bar input { margin-bottom: 0; }
        .search-bar select { width: auto; margin-bottom: 0; }
        .tabs {
            display: flex;
            gap: 5px;
            margin-bottom: 20px;
            border-bottom: 1px solid var(--border-color);
            padding-bottom: 5px;
        }
        .tab {
            padding: 10px 20px;
            background: none;
            border: none;
            color: var(--text-secondary);
            cursor: pointer;
            border-radius: 6px 6px 0 0;
        }
        .tab.active {
            color: var(--text-primary);
            background: var(--bg-tertiary);
        }
        .tab-content { display: none; }
        .tab-content.active { display: block; }
    </style>
</head>
<body>
    <div class="container">
        <header>
            <h1>Space Data Network - Trusted Peer Registry</h1>
            <div class="stats">
                <div class="stat-card">
                    <div class="stat-value" id="peerCount">-</div>
                    <div class="stat-label">Total Peers</div>
                </div>
                <div class="stat-card">
                    <div class="stat-value" id="groupCount">-</div>
                    <div class="stat-label">Groups</div>
                </div>
                <div class="stat-card">
                    <div class="stat-value" id="blockedCount">-</div>
                    <div class="stat-label">Blocked</div>
                </div>
            </div>
        </header>

        <div class="tabs">
            <button class="tab active" data-tab="peers">Peers</button>
            <button class="tab" data-tab="groups">Groups</button>
            <button class="tab" data-tab="blocklist">Blocklist</button>
            <button class="tab" data-tab="settings">Settings</button>
        </div>

        <div class="tab-content active" id="peers-tab">
            <div class="panel">
                <div class="panel-header">
                    <h2>Trusted Peers</h2>
                    <div class="actions">
                        <button class="btn btn-primary" onclick="showAddPeerModal()">Add Peer</button>
                        <button class="btn" onclick="exportPeers()">Export</button>
                        <label class="btn" style="cursor:pointer;">
                            Import
                            <input type="file" accept=".json" onchange="importPeers(this)" style="display:none;">
                        </label>
                    </div>
                </div>
                <div class="search-bar">
                    <input type="text" id="peerSearch" placeholder="Search peers..." onkeyup="filterPeers()">
                    <select id="trustFilter" onchange="filterPeers()">
                        <option value="">All Trust Levels</option>
                        <option value="untrusted">Untrusted</option>
                        <option value="limited">Limited</option>
                        <option value="standard">Standard</option>
                        <option value="trusted">Trusted</option>
                        <option value="admin">Admin</option>
                    </select>
                </div>
                <table>
                    <thead>
                        <tr>
                            <th>Status</th>
                            <th>Peer ID</th>
                            <th>Name</th>
                            <th>Organization</th>
                            <th>Trust Level</th>
                            <th>Last Seen</th>
                            <th>Actions</th>
                        </tr>
                    </thead>
                    <tbody id="peersTable">
                        <tr><td colspan="7" class="empty-state">Loading...</td></tr>
                    </tbody>
                </table>
            </div>
        </div>

        <div class="tab-content" id="groups-tab">
            <div class="panel">
                <div class="panel-header">
                    <h2>Peer Groups</h2>
                    <button class="btn btn-primary" onclick="showAddGroupModal()">Add Group</button>
                </div>
                <table>
                    <thead>
                        <tr>
                            <th>Name</th>
                            <th>Description</th>
                            <th>Default Trust</th>
                            <th>Members</th>
                            <th>Actions</th>
                        </tr>
                    </thead>
                    <tbody id="groupsTable">
                        <tr><td colspan="5" class="empty-state">Loading...</td></tr>
                    </tbody>
                </table>
            </div>
        </div>

        <div class="tab-content" id="blocklist-tab">
            <div class="panel">
                <div class="panel-header">
                    <h2>Blocked Peers</h2>
                    <button class="btn btn-primary" onclick="showBlockPeerModal()">Block Peer</button>
                </div>
                <table>
                    <thead>
                        <tr>
                            <th>Peer ID</th>
                            <th>Actions</th>
                        </tr>
                    </thead>
                    <tbody id="blocklistTable">
                        <tr><td colspan="2" class="empty-state">Loading...</td></tr>
                    </tbody>
                </table>
            </div>
        </div>

        <div class="tab-content" id="settings-tab">
            <div class="panel">
                <h2>Settings</h2>
                <div class="toggle-container">
                    <label class="toggle">
                        <input type="checkbox" id="strictMode" onchange="updateStrictMode()">
                        <span class="toggle-slider"></span>
                    </label>
                    <div>
                        <strong>Strict Mode</strong>
                        <p style="color: var(--text-secondary); font-size: 13px;">
                            Only allow connections to/from peers in the trusted registry.
                        </p>
                    </div>
                </div>
            </div>
        </div>
    </div>

    <!-- Add Peer Modal -->
    <div class="modal" id="addPeerModal">
        <div class="modal-content">
            <div class="modal-header">
                <h2>Add Peer</h2>
                <button class="modal-close" onclick="closeModal('addPeerModal')">&times;</button>
            </div>
            <form onsubmit="addPeer(event)">
                <div class="form-group">
                    <label>Peer ID *</label>
                    <input type="text" id="newPeerId" placeholder="12D3KooW..." required>
                </div>
                <div class="form-group">
                    <label>Name</label>
                    <input type="text" id="newPeerName" placeholder="Friendly name">
                </div>
                <div class="form-group">
                    <label>Organization</label>
                    <input type="text" id="newPeerOrg" placeholder="Organization name">
                </div>
                <div class="form-group">
                    <label>Addresses (one per line)</label>
                    <textarea id="newPeerAddrs" placeholder="/ip4/192.168.1.1/tcp/4001" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 6px; padding: 10px; color: var(--text-primary); width: 100%; min-height: 80px; resize: vertical;"></textarea>
                </div>
                <div class="form-group">
                    <label>Trust Level</label>
                    <select id="newPeerTrust">
                        <option value="standard">Standard</option>
                        <option value="limited">Limited</option>
                        <option value="trusted">Trusted</option>
                        <option value="admin">Admin</option>
                    </select>
                </div>
                <div class="form-group">
                    <label>Notes</label>
                    <textarea id="newPeerNotes" placeholder="Optional notes" style="background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 6px; padding: 10px; color: var(--text-primary); width: 100%; min-height: 60px; resize: vertical;"></textarea>
                </div>
                <div class="actions">
                    <button type="submit" class="btn btn-primary">Add Peer</button>
                    <button type="button" class="btn" onclick="closeModal('addPeerModal')">Cancel</button>
                </div>
            </form>
        </div>
    </div>

    <!-- Add Group Modal -->
    <div class="modal" id="addGroupModal">
        <div class="modal-content">
            <div class="modal-header">
                <h2>Add Group</h2>
                <button class="modal-close" onclick="closeModal('addGroupModal')">&times;</button>
            </div>
            <form onsubmit="addGroup(event)">
                <div class="form-group">
                    <label>Group Name *</label>
                    <input type="text" id="newGroupName" placeholder="e.g., satellite-operators" required>
                </div>
                <div class="form-group">
                    <label>Description</label>
                    <input type="text" id="newGroupDesc" placeholder="Description">
                </div>
                <div class="form-group">
                    <label>Default Trust Level</label>
                    <select id="newGroupTrust">
                        <option value="standard">Standard</option>
                        <option value="limited">Limited</option>
                        <option value="trusted">Trusted</option>
                    </select>
                </div>
                <div class="actions">
                    <button type="submit" class="btn btn-primary">Add Group</button>
                    <button type="button" class="btn" onclick="closeModal('addGroupModal')">Cancel</button>
                </div>
            </form>
        </div>
    </div>

    <!-- Block Peer Modal -->
    <div class="modal" id="blockPeerModal">
        <div class="modal-content">
            <div class="modal-header">
                <h2>Block Peer</h2>
                <button class="modal-close" onclick="closeModal('blockPeerModal')">&times;</button>
            </div>
            <form onsubmit="blockPeer(event)">
                <div class="form-group">
                    <label>Peer ID *</label>
                    <input type="text" id="blockPeerId" placeholder="12D3KooW..." required>
                </div>
                <div class="actions">
                    <button type="submit" class="btn btn-danger">Block Peer</button>
                    <button type="button" class="btn" onclick="closeModal('blockPeerModal')">Cancel</button>
                </div>
            </form>
        </div>
    </div>

    <script>
        const API_BASE = '/api';
        let allPeers = [];

        // Tab handling
        document.querySelectorAll('.tab').forEach(tab => {
            tab.addEventListener('click', () => {
                document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
                document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
                tab.classList.add('active');
                document.getElementById(tab.dataset.tab + '-tab').classList.add('active');
            });
        });

        // Modal handling
        function showAddPeerModal() { document.getElementById('addPeerModal').classList.add('active'); }
        function showAddGroupModal() { document.getElementById('addGroupModal').classList.add('active'); }
        function showBlockPeerModal() { document.getElementById('blockPeerModal').classList.add('active'); }
        function closeModal(id) { document.getElementById(id).classList.remove('active'); }

        // API calls
        async function fetchPeers() {
            try {
                const res = await fetch(API_BASE + '/peers');
                allPeers = await res.json();
                renderPeers(allPeers);
                document.getElementById('peerCount').textContent = allPeers.length;
            } catch (e) {
                console.error('Error fetching peers:', e);
            }
        }

        async function fetchGroups() {
            try {
                const res = await fetch(API_BASE + '/groups');
                const groups = await res.json();
                renderGroups(groups);
                document.getElementById('groupCount').textContent = groups.length;
            } catch (e) {
                console.error('Error fetching groups:', e);
            }
        }

        async function fetchBlocklist() {
            try {
                const res = await fetch(API_BASE + '/blocklist');
                const blocked = await res.json();
                renderBlocklist(blocked);
                document.getElementById('blockedCount').textContent = blocked.length;
            } catch (e) {
                console.error('Error fetching blocklist:', e);
            }
        }

        async function fetchSettings() {
            try {
                const res = await fetch(API_BASE + '/settings');
                const settings = await res.json();
                document.getElementById('strictMode').checked = settings.strict_mode;
            } catch (e) {
                console.error('Error fetching settings:', e);
            }
        }

        function renderPeers(peers) {
            const tbody = document.getElementById('peersTable');
            if (peers.length === 0) {
                tbody.innerHTML = '<tr><td colspan="7" class="empty-state">No peers in registry</td></tr>';
                return;
            }
            tbody.innerHTML = peers.map(p => {
                const lastSeen = p.last_seen ? new Date(p.last_seen).toLocaleString() : 'Never';
                const trustClass = 'trust-' + p.trust_level;
                return ` + "`" + `
                    <tr>
                        <td><span class="connection-indicator disconnected"></span></td>
                        <td class="peer-id" title="${p.id}">${p.id.substring(0, 16)}...</td>
                        <td>${p.name || '-'}</td>
                        <td>${p.organization || '-'}</td>
                        <td><span class="trust-badge ${trustClass}">${p.trust_level}</span></td>
                        <td>${lastSeen}</td>
                        <td>
                            <button class="btn btn-small" onclick="editPeerTrust('${p.id}')">Edit</button>
                            <button class="btn btn-small btn-danger" onclick="removePeer('${p.id}')">Remove</button>
                        </td>
                    </tr>
                ` + "`" + `;
            }).join('');
        }

        function renderGroups(groups) {
            const tbody = document.getElementById('groupsTable');
            if (groups.length === 0) {
                tbody.innerHTML = '<tr><td colspan="5" class="empty-state">No groups</td></tr>';
                return;
            }
            tbody.innerHTML = groups.map(g => {
                const memberCount = g.members ? g.members.length : 0;
                const trustClass = 'trust-' + g.default_trust_level;
                return ` + "`" + `
                    <tr>
                        <td>${g.name}</td>
                        <td>${g.description || '-'}</td>
                        <td><span class="trust-badge ${trustClass}">${g.default_trust_level}</span></td>
                        <td>${memberCount}</td>
                        <td>
                            <button class="btn btn-small btn-danger" onclick="removeGroup('${g.name}')">Remove</button>
                        </td>
                    </tr>
                ` + "`" + `;
            }).join('');
        }

        function renderBlocklist(blocked) {
            const tbody = document.getElementById('blocklistTable');
            if (blocked.length === 0) {
                tbody.innerHTML = '<tr><td colspan="2" class="empty-state">No blocked peers</td></tr>';
                return;
            }
            tbody.innerHTML = blocked.map(id => ` + "`" + `
                <tr>
                    <td class="peer-id">${id}</td>
                    <td>
                        <button class="btn btn-small" onclick="unblockPeer('${id}')">Unblock</button>
                    </td>
                </tr>
            ` + "`" + `).join('');
        }

        function filterPeers() {
            const search = document.getElementById('peerSearch').value.toLowerCase();
            const trustFilter = document.getElementById('trustFilter').value;
            const filtered = allPeers.filter(p => {
                const matchesSearch = !search ||
                    p.id.toLowerCase().includes(search) ||
                    (p.name && p.name.toLowerCase().includes(search)) ||
                    (p.organization && p.organization.toLowerCase().includes(search));
                const matchesTrust = !trustFilter || p.trust_level === trustFilter;
                return matchesSearch && matchesTrust;
            });
            renderPeers(filtered);
        }

        async function addPeer(e) {
            e.preventDefault();
            const addrs = document.getElementById('newPeerAddrs').value
                .split('\n')
                .map(a => a.trim())
                .filter(a => a);

            const peer = {
                id: document.getElementById('newPeerId').value,
                name: document.getElementById('newPeerName').value,
                organization: document.getElementById('newPeerOrg').value,
                addrs: addrs,
                trust_level: document.getElementById('newPeerTrust').value,
                notes: document.getElementById('newPeerNotes').value
            };

            try {
                const res = await fetch(API_BASE + '/peers', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(peer)
                });
                if (res.ok) {
                    closeModal('addPeerModal');
                    fetchPeers();
                } else {
                    alert('Error: ' + await res.text());
                }
            } catch (e) {
                alert('Error adding peer: ' + e);
            }
        }

        async function removePeer(id) {
            if (!confirm('Remove this peer from the registry?')) return;
            try {
                await fetch(API_BASE + '/peers/' + encodeURIComponent(id), { method: 'DELETE' });
                fetchPeers();
            } catch (e) {
                alert('Error removing peer: ' + e);
            }
        }

        async function editPeerTrust(id) {
            const newTrust = prompt('Enter new trust level (untrusted, limited, standard, trusted, admin):');
            if (!newTrust) return;
            try {
                const res = await fetch(API_BASE + '/peers/' + encodeURIComponent(id) + '/trust', {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ trust_level: newTrust })
                });
                if (res.ok) {
                    fetchPeers();
                } else {
                    alert('Error: ' + await res.text());
                }
            } catch (e) {
                alert('Error updating trust: ' + e);
            }
        }

        async function addGroup(e) {
            e.preventDefault();
            const group = {
                name: document.getElementById('newGroupName').value,
                description: document.getElementById('newGroupDesc').value,
                default_trust_level: document.getElementById('newGroupTrust').value
            };

            try {
                const res = await fetch(API_BASE + '/groups', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(group)
                });
                if (res.ok) {
                    closeModal('addGroupModal');
                    fetchGroups();
                } else {
                    alert('Error: ' + await res.text());
                }
            } catch (e) {
                alert('Error adding group: ' + e);
            }
        }

        async function removeGroup(name) {
            if (!confirm('Remove this group?')) return;
            try {
                await fetch(API_BASE + '/groups/' + encodeURIComponent(name), { method: 'DELETE' });
                fetchGroups();
            } catch (e) {
                alert('Error removing group: ' + e);
            }
        }

        async function blockPeer(e) {
            e.preventDefault();
            const id = document.getElementById('blockPeerId').value;
            try {
                const res = await fetch(API_BASE + '/blocklist', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ peer_id: id })
                });
                if (res.ok) {
                    closeModal('blockPeerModal');
                    fetchBlocklist();
                } else {
                    alert('Error: ' + await res.text());
                }
            } catch (e) {
                alert('Error blocking peer: ' + e);
            }
        }

        async function unblockPeer(id) {
            try {
                await fetch(API_BASE + '/blocklist/' + encodeURIComponent(id), { method: 'DELETE' });
                fetchBlocklist();
            } catch (e) {
                alert('Error unblocking peer: ' + e);
            }
        }

        async function updateStrictMode() {
            const strictMode = document.getElementById('strictMode').checked;
            try {
                await fetch(API_BASE + '/settings', {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ strict_mode: strictMode })
                });
            } catch (e) {
                alert('Error updating settings: ' + e);
            }
        }

        function exportPeers() {
            window.location.href = API_BASE + '/export';
        }

        function importPeers(input) {
            const file = input.files[0];
            if (!file) return;
            const reader = new FileReader();
            reader.onload = async (e) => {
                try {
                    const res = await fetch(API_BASE + '/import?merge=true', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: e.target.result
                    });
                    if (res.ok) {
                        fetchPeers();
                        fetchGroups();
                        alert('Import successful!');
                    } else {
                        alert('Error: ' + await res.text());
                    }
                } catch (e) {
                    alert('Error importing: ' + e);
                }
            };
            reader.readAsText(file);
        }

        // Initial load
        fetchPeers();
        fetchGroups();
        fetchBlocklist();
        fetchSettings();

        // Refresh every 30 seconds
        setInterval(() => {
            fetchPeers();
            fetchGroups();
            fetchBlocklist();
        }, 30000);
    </script>
</body>
</html>`
