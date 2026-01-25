// Package server provides the HTTP server with setup, admin, and API endpoints.
package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"

	"github.com/spacedatanetwork/sdn-server/internal/admin"
	"github.com/spacedatanetwork/sdn-server/internal/audit"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/setup"
)

var log = logging.Logger("sdn-server")

// Server represents the HTTP server with admin and setup functionality.
type Server struct {
	config      *config.Config
	setupMgr    *setup.Manager
	keyMgr      *keys.Manager
	adminMgr    *admin.Manager
	auditLog    *audit.Logger
	httpServer  *http.Server
	mux         *http.ServeMux
	setupToken  string
	mu          sync.RWMutex
}

// NewServer creates a new HTTP server.
func NewServer(cfg *config.Config) (*Server, error) {
	// Determine base data path
	basePath := cfg.Setup.DataPath
	if basePath == "" {
		basePath = filepath.Dir(cfg.Storage.Path)
	}

	// Initialize setup manager
	setupMgr, err := setup.NewManager(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create setup manager: %w", err)
	}

	// Initialize key manager
	keyMgr, err := keys.NewManager(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create key manager: %w", err)
	}

	// Initialize admin manager
	adminMgr, err := admin.NewManager(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create admin manager: %w", err)
	}

	// Initialize audit logger
	auditLog, err := audit.NewLogger(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create audit logger: %w", err)
	}

	s := &Server{
		config:   cfg,
		setupMgr: setupMgr,
		keyMgr:   keyMgr,
		adminMgr: adminMgr,
		auditLog: auditLog,
		mux:      http.NewServeMux(),
	}

	// Setup HTTP routes
	s.setupRoutes()

	return s, nil
}

// setupRoutes configures all HTTP routes.
func (s *Server) setupRoutes() {
	// Setup routes (only active during first-time setup)
	s.mux.HandleFunc("/setup", s.handleSetup)
	s.mux.HandleFunc("/api/setup", s.handleSetupAPI)

	// Login routes
	s.mux.HandleFunc("/login", s.handleLogin)
	s.mux.HandleFunc("/api/login", s.handleLoginAPI)
	s.mux.HandleFunc("/api/logout", s.handleLogoutAPI)

	// Admin routes (require authentication)
	s.mux.HandleFunc("/admin", s.requireAuth(s.handleAdmin))
	s.mux.HandleFunc("/admin/", s.requireAuth(s.handleAdmin))
	s.mux.HandleFunc("/api/admin/", s.requireAuth(s.handleAdminAPI))

	// Key backup/recovery routes (require authentication)
	s.mux.HandleFunc("/api/keys/backup", s.requireAuth(s.handleKeyBackup))
	s.mux.HandleFunc("/api/keys/restore", s.requireAuth(s.handleKeyRestore))

	// Audit log routes (require authentication)
	s.mux.HandleFunc("/api/audit", s.requireAuth(s.handleAuditAPI))

	// Health check
	s.mux.HandleFunc("/health", s.handleHealth)

	// Root redirect
	s.mux.HandleFunc("/", s.handleRoot)
}

// Start starts the HTTP server.
func (s *Server) Start(ctx context.Context) error {
	// Check if setup is required
	if s.setupMgr.IsSetupRequired() {
		// Generate setup token
		token, err := s.setupMgr.StartSetupMode()
		if err != nil {
			return fmt.Errorf("failed to start setup mode: %w", err)
		}
		s.mu.Lock()
		s.setupToken = token
		s.mu.Unlock()

		// Print setup banner
		setup.PrintSetupBanner(token, s.config.Admin.ListenAddr)

		// Log setup start
		s.auditLog.LogSetupStart("")
	} else {
		// Load existing identity
		if s.keyMgr.HasIdentity() {
			_, err := s.keyMgr.LoadIdentity()
			if err != nil {
				log.Warnf("Failed to load identity: %v", err)
			} else {
				log.Infof("Server identity loaded: %s", s.keyMgr.PublicKeyFingerprint())
				s.auditLog.LogServerStart(s.keyMgr.PublicKeyFingerprint())
			}
		}
	}

	// Create HTTP server
	s.httpServer = &http.Server{
		Addr:    s.config.Admin.ListenAddr,
		Handler: s.mux,
	}

	// Start listening
	go func() {
		log.Infof("HTTP server listening on %s", s.config.Admin.ListenAddr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Errorf("HTTP server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop(ctx context.Context) error {
	s.auditLog.LogServerStop()

	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			log.Warnf("HTTP server shutdown error: %v", err)
		}
	}

	if s.adminMgr != nil {
		s.adminMgr.Close()
	}

	if s.auditLog != nil {
		s.auditLog.Close()
	}

	return nil
}

// IsSetupRequired returns true if first-time setup is needed.
func (s *Server) IsSetupRequired() bool {
	return s.setupMgr.IsSetupRequired()
}

// handleRoot redirects to appropriate page.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	if s.setupMgr.IsSetupRequired() {
		http.Redirect(w, r, "/setup", http.StatusTemporaryRedirect)
		return
	}

	http.Redirect(w, r, "/admin", http.StatusTemporaryRedirect)
}

// handleSetup serves the setup page.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.setupMgr.IsSetupRequired() {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	handler := setup.NewHandler(s.setupMgr, s.keyMgr, s.adminMgr, s.auditLog)
	handler.HandleSetupPage(w, r)
}

// handleSetupAPI handles setup completion.
func (s *Server) handleSetupAPI(w http.ResponseWriter, r *http.Request) {
	if !s.setupMgr.IsSetupRequired() {
		http.Error(w, "Setup already complete", http.StatusBadRequest)
		return
	}

	handler := setup.NewHandler(s.setupMgr, s.keyMgr, s.adminMgr, s.auditLog)
	handler.HandleSetupAPI(w, r)
}

// handleLogin serves the login page.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.setupMgr.IsSetupRequired() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	// Serve login page HTML
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(loginPageHTML))
}

// handleLoginAPI handles login requests.
func (s *Server) handleLoginAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	rememberMe := r.FormValue("remember_me") == "true"
	totpCode := r.FormValue("totp_code")

	clientIP := getClientIP(r)
	userAgent := r.UserAgent()

	var token string
	var err error

	if totpCode != "" {
		token, err = s.adminMgr.AuthenticateWithTOTP(username, password, totpCode, clientIP, userAgent, rememberMe)
	} else {
		token, err = s.adminMgr.Authenticate(username, password, clientIP, userAgent, rememberMe)
	}

	if err != nil {
		// Log failed login attempt
		s.auditLog.LogAdminLogin(0, username, clientIP, false)

		status := http.StatusUnauthorized
		msg := "Invalid credentials"

		if err == admin.ErrTOTPRequired {
			status = http.StatusPreconditionRequired
			msg = "TOTP required"
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"success":false,"error":"%s"}`, msg)
		return
	}

	// Log successful login
	session, _ := s.adminMgr.ValidateSession(token)
	if session != nil {
		s.auditLog.LogAdminLogin(session.AdminID, username, clientIP, true)
	}

	// Set session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "sdn_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(24 * time.Hour / time.Second),
	})

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"success":true,"redirect":"/admin"}`)
}

// handleLogoutAPI handles logout requests.
func (s *Server) handleLogoutAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get session cookie
	cookie, err := r.Cookie("sdn_session")
	if err == nil && cookie.Value != "" {
		s.adminMgr.RevokeSession(cookie.Value)
	}

	// Clear cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "sdn_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"success":true}`)
}

// requireAuth is a middleware that checks for valid session.
func (s *Server) requireAuth(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Skip auth if setup is required
		if s.setupMgr.IsSetupRequired() {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}

		// Skip auth if not required by config
		if !s.config.Admin.RequireAuth {
			handler(w, r)
			return
		}

		// Check session cookie
		cookie, err := r.Cookie("sdn_session")
		if err != nil || cookie.Value == "" {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return
		}

		// Validate session
		session, err := s.adminMgr.ValidateSession(cookie.Value)
		if err != nil {
			// Clear invalid cookie
			http.SetCookie(w, &http.Cookie{
				Name:   "sdn_session",
				Value:  "",
				Path:   "/",
				MaxAge: -1,
			})

			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, "Session expired", http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return
		}

		// Store session in context
		ctx := context.WithValue(r.Context(), "session", session)
		handler(w, r.WithContext(ctx))
	}
}

// handleAdmin serves the admin dashboard.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(adminPageHTML))
}

// handleAdminAPI handles admin API requests.
func (s *Server) handleAdminAPI(w http.ResponseWriter, r *http.Request) {
	// Parse sub-path
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/")

	switch {
	case path == "password" && r.Method == http.MethodPost:
		s.handlePasswordChange(w, r)
	case path == "sessions":
		s.handleSessions(w, r)
	case path == "profile":
		s.handleProfile(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handlePasswordChange handles password change requests.
func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value("session").(*admin.Session)
	clientIP := getClientIP(r)

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	oldPassword := r.FormValue("old_password")
	newPassword := r.FormValue("new_password")

	if err := s.adminMgr.ChangePassword(session.AdminID, oldPassword, newPassword); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"success":false,"error":"%s"}`, err.Error())
		return
	}

	s.auditLog.LogPasswordChange(session.AdminID, clientIP)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"success":true}`)
}

// handleSessions handles session management requests.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value("session").(*admin.Session)

	if r.Method == http.MethodGet {
		// List active sessions
		sessions, err := s.adminMgr.ListActiveSessions(session.AdminID)
		if err != nil {
			http.Error(w, "Failed to list sessions", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"sessions":[`)
		for i, sess := range sessions {
			if i > 0 {
				fmt.Fprintf(w, ",")
			}
			isCurrent := sess.Token == session.Token
			fmt.Fprintf(w, `{"id":"%s","ip":"%s","user_agent":"%s","created_at":"%s","current":%t}`,
				admin.HashToken(sess.Token), sess.IPAddress, sess.UserAgent,
				sess.CreatedAt.Format(time.RFC3339), isCurrent)
		}
		fmt.Fprintf(w, `]}`)
		return
	}

	if r.Method == http.MethodDelete {
		// Revoke all other sessions
		s.adminMgr.RevokeAllSessions(session.AdminID)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success":true}`)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// handleProfile returns the admin profile.
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value("session").(*admin.Session)

	adm, err := s.adminMgr.GetAdmin(session.AdminID)
	if err != nil {
		http.Error(w, "Admin not found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"username":"%s","totp_enabled":%t,"created_at":"%s"}`,
		adm.Username, adm.TOTPEnabled, adm.CreatedAt.Format(time.RFC3339))
}

// handleKeyBackup handles key backup requests.
func (s *Server) handleKeyBackup(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value("session").(*admin.Session)
	clientIP := getClientIP(r)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	password := r.FormValue("password")
	if password == "" {
		http.Error(w, "Password required", http.StatusBadRequest)
		return
	}

	backup, err := s.keyMgr.ExportEncrypted(password)
	if err != nil {
		http.Error(w, "Failed to export backup", http.StatusInternalServerError)
		return
	}

	s.auditLog.LogKeyBackup(session.AdminID, clientIP)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=sdn-key-backup.json")
	w.Write([]byte(backup))
}

// handleKeyRestore handles key restore requests.
func (s *Server) handleKeyRestore(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value("session").(*admin.Session)
	clientIP := getClientIP(r)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	backup := r.FormValue("backup")
	password := r.FormValue("password")

	if err := s.keyMgr.ImportEncrypted(backup, password); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"success":false,"error":"%s"}`, err.Error())
		return
	}

	s.auditLog.LogKeyRestore(session.AdminID, clientIP, s.keyMgr.PublicKeyFingerprint())

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"success":true,"fingerprint":"%s"}`, s.keyMgr.PublicKeyFingerprint())
}

// handleAuditAPI handles audit log API requests.
func (s *Server) handleAuditAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse query params
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}

	eventType := r.URL.Query().Get("type")
	severity := r.URL.Query().Get("severity")

	opts := audit.QueryOptions{
		EventType: eventType,
		Severity:  severity,
		Limit:     limit,
	}

	entries, err := s.auditLog.Query(opts)
	if err != nil {
		http.Error(w, "Failed to query audit log", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"entries":[`)
	for i, entry := range entries {
		if i > 0 {
			fmt.Fprintf(w, ",")
		}
		fmt.Fprintf(w, `{"id":%d,"timestamp":"%s","type":"%s","severity":"%s","description":"%s"}`,
			entry.ID, entry.Timestamp.Format(time.RFC3339), entry.EventType, entry.Severity, entry.Description)
	}
	fmt.Fprintf(w, `]}`)
}

// handleHealth returns server health status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","setup_required":%t}`, s.setupMgr.IsSetupRequired())
}

// getClientIP extracts the client IP from the request.
func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}

// KeyManager returns the key manager.
func (s *Server) KeyManager() *keys.Manager {
	return s.keyMgr
}

// AuditLogger returns the audit logger.
func (s *Server) AuditLogger() *audit.Logger {
	return s.auditLog
}

// Login page HTML
const loginPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SDN Login</title>
    <style>
        :root {
            --bg-color: #0a0a0f;
            --card-bg: #12121a;
            --border-color: #2a2a3a;
            --text-primary: #e0e0e0;
            --text-secondary: #8a8a9a;
            --accent-color: #4a9eff;
            --error-color: #ff4a4a;
        }
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            background: var(--bg-color);
            color: var(--text-primary);
            min-height: 100vh;
            display: flex;
            justify-content: center;
            align-items: center;
            padding: 20px;
        }
        .card {
            background: var(--card-bg);
            border: 1px solid var(--border-color);
            border-radius: 12px;
            padding: 32px;
            width: 100%;
            max-width: 400px;
        }
        h1 { text-align: center; margin-bottom: 24px; }
        .form-group { margin-bottom: 20px; }
        label { display: block; margin-bottom: 8px; color: var(--text-secondary); }
        input {
            width: 100%;
            padding: 12px 16px;
            background: var(--bg-color);
            border: 1px solid var(--border-color);
            border-radius: 8px;
            color: var(--text-primary);
            font-size: 16px;
        }
        input:focus { outline: none; border-color: var(--accent-color); }
        button {
            width: 100%;
            padding: 14px;
            background: var(--accent-color);
            border: none;
            border-radius: 8px;
            color: white;
            font-size: 16px;
            cursor: pointer;
        }
        button:hover { opacity: 0.9; }
        .error { color: var(--error-color); text-align: center; margin-bottom: 16px; display: none; }
        .checkbox { display: flex; align-items: center; gap: 8px; }
        .checkbox input { width: auto; }
    </style>
</head>
<body>
    <div class="card">
        <h1>Space Data Network</h1>
        <div id="error" class="error"></div>
        <form id="login-form">
            <div class="form-group">
                <label>Username</label>
                <input type="text" id="username" required>
            </div>
            <div class="form-group">
                <label>Password</label>
                <input type="password" id="password" required>
            </div>
            <div class="form-group" id="totp-group" style="display:none;">
                <label>TOTP Code</label>
                <input type="text" id="totp_code" pattern="[0-9]{6}">
            </div>
            <div class="form-group checkbox">
                <input type="checkbox" id="remember_me">
                <label for="remember_me" style="margin-bottom:0;">Remember me</label>
            </div>
            <button type="submit">Login</button>
        </form>
    </div>
    <script>
        const form = document.getElementById('login-form');
        const error = document.getElementById('error');
        form.addEventListener('submit', async (e) => {
            e.preventDefault();
            error.style.display = 'none';
            const data = new URLSearchParams({
                username: document.getElementById('username').value,
                password: document.getElementById('password').value,
                remember_me: document.getElementById('remember_me').checked,
                totp_code: document.getElementById('totp_code').value
            });
            try {
                const resp = await fetch('/api/login', {
                    method: 'POST',
                    body: data
                });
                const json = await resp.json();
                if (json.success) {
                    window.location.href = json.redirect;
                } else if (resp.status === 428) {
                    document.getElementById('totp-group').style.display = 'block';
                    error.textContent = 'Enter TOTP code';
                    error.style.display = 'block';
                } else {
                    error.textContent = json.error;
                    error.style.display = 'block';
                }
            } catch (e) {
                error.textContent = 'Connection error';
                error.style.display = 'block';
            }
        });
    </script>
</body>
</html>`

// Admin page HTML (minimal placeholder)
const adminPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SDN Admin</title>
    <style>
        :root {
            --bg-color: #0a0a0f;
            --card-bg: #12121a;
            --border-color: #2a2a3a;
            --text-primary: #e0e0e0;
            --text-secondary: #8a8a9a;
            --accent-color: #4a9eff;
        }
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            background: var(--bg-color);
            color: var(--text-primary);
            min-height: 100vh;
            padding: 20px;
        }
        .container { max-width: 1200px; margin: 0 auto; }
        h1 { margin-bottom: 24px; }
        .card {
            background: var(--card-bg);
            border: 1px solid var(--border-color);
            border-radius: 12px;
            padding: 24px;
            margin-bottom: 20px;
        }
        .logout { float: right; background: transparent; border: 1px solid var(--border-color);
                  padding: 8px 16px; border-radius: 6px; color: var(--text-secondary); cursor: pointer; }
        .logout:hover { background: var(--border-color); }
    </style>
</head>
<body>
    <div class="container">
        <button class="logout" onclick="logout()">Logout</button>
        <h1>Space Data Network Admin</h1>
        <div class="card">
            <h2>Server Status</h2>
            <p>Server is running.</p>
        </div>
        <div class="card">
            <h2>Security</h2>
            <p>Session management and key backup are available via the API.</p>
        </div>
    </div>
    <script>
        async function logout() {
            await fetch('/api/logout', { method: 'POST' });
            window.location.href = '/login';
        }
    </script>
</body>
</html>`
