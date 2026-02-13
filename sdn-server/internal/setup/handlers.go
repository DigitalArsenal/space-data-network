// Package setup provides first-time server setup security for the SDN server.
package setup

import (
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"strings"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/spacedatanetwork/sdn-server/internal/admin"
	"github.com/spacedatanetwork/sdn-server/internal/audit"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

// Handler handles HTTP requests for the setup process.
type Handler struct {
	setupMgr *Manager
	keyMgr   *keys.Manager
	adminMgr *admin.Manager
	auditLog *audit.Logger
}

// NewHandler creates a new setup handler.
func NewHandler(setupMgr *Manager, keyMgr *keys.Manager, adminMgr *admin.Manager, auditLog *audit.Logger) *Handler {
	return &Handler{
		setupMgr: setupMgr,
		keyMgr:   keyMgr,
		adminMgr: adminMgr,
		auditLog: auditLog,
	}
}

// SetupPageData contains data for the setup page template.
type SetupPageData struct {
	SetupComplete  bool
	TokenExpired   bool
	RemainingTime  string
	ErrorMessage   string
	SuccessMessage string
}

// SetupRequest represents a setup completion request.
type SetupRequest struct {
	Token      string `json:"token"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	ServerName string `json:"server_name"`
}

// SetupResponse represents the response after setup completion.
type SetupResponse struct {
	Success             bool   `json:"success"`
	Error               string `json:"error,omitempty"`
	SigningPublicKey    string `json:"signing_public_key,omitempty"`
	EncryptionPublicKey string `json:"encryption_public_key,omitempty"`
	Fingerprint         string `json:"fingerprint,omitempty"`
}

// HandleSetupPage serves the setup page.
func (h *Handler) HandleSetupPage(w http.ResponseWriter, r *http.Request) {
	// Only allow GET and POST
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if setup is already complete
	if h.setupMgr.IsSetupComplete() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	data := SetupPageData{
		SetupComplete: h.setupMgr.IsSetupComplete(),
		TokenExpired:  h.setupMgr.RemainingTime() == 0,
	}

	if !data.TokenExpired {
		remaining := h.setupMgr.RemainingTime()
		minutes := int(remaining.Minutes())
		seconds := int(remaining.Seconds()) % 60
		data.RemainingTime = formatTime(minutes, seconds)
	}

	// Render template
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := setupTemplate.Execute(w, data); err != nil {
		log.Errorf("Failed to render setup template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// HandleSetupAPI handles the setup completion API endpoint.
func (h *Handler) HandleSetupAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if setup is already complete
	if h.setupMgr.IsSetupComplete() {
		sendJSONResponse(w, SetupResponse{
			Success: false,
			Error:   "Setup already complete",
		}, http.StatusBadRequest)
		return
	}

	// Parse request
	var req SetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSONResponse(w, SetupResponse{
			Success: false,
			Error:   "Invalid request body",
		}, http.StatusBadRequest)
		return
	}

	// Validate input
	if req.Token == "" {
		sendJSONResponse(w, SetupResponse{
			Success: false,
			Error:   "Token is required",
		}, http.StatusBadRequest)
		return
	}
	if req.Username == "" || len(req.Username) < 3 {
		sendJSONResponse(w, SetupResponse{
			Success: false,
			Error:   "Username must be at least 3 characters",
		}, http.StatusBadRequest)
		return
	}
	if req.Password == "" || len(req.Password) < 8 {
		sendJSONResponse(w, SetupResponse{
			Success: false,
			Error:   "Password must be at least 8 characters",
		}, http.StatusBadRequest)
		return
	}

	// Get client IP
	clientIP := getClientIP(r)

	// Verify token
	if err := h.setupMgr.VerifyToken(req.Token); err != nil {
		errMsg := "Invalid token"
		if err == ErrSetupTokenExpired {
			errMsg = "Token has expired. Please restart the server to get a new token."
		}
		sendJSONResponse(w, SetupResponse{
			Success: false,
			Error:   errMsg,
		}, http.StatusUnauthorized)
		return
	}

	// Generate server identity keys
	identity, err := h.keyMgr.GenerateIdentity()
	if err != nil {
		log.Errorf("Failed to generate identity: %v", err)
		sendJSONResponse(w, SetupResponse{
			Success: false,
			Error:   "Failed to generate server identity",
		}, http.StatusInternalServerError)
		return
	}

	// Create admin account
	if err := h.adminMgr.CreateAdmin(req.Username, req.Password); err != nil {
		log.Errorf("Failed to create admin: %v", err)
		sendJSONResponse(w, SetupResponse{
			Success: false,
			Error:   "Failed to create admin account",
		}, http.StatusInternalServerError)
		return
	}

	// Generate EPM for server identity
	if req.ServerName == "" {
		req.ServerName = "Space Data Network Server"
	}
	epmData := buildServerEPM(req.ServerName, identity)
	_ = epmData // EPM is generated but storage implementation can be added later

	// Mark setup as complete
	if err := h.setupMgr.CompleteSetup(); err != nil {
		log.Errorf("Failed to complete setup: %v", err)
		sendJSONResponse(w, SetupResponse{
			Success: false,
			Error:   "Failed to complete setup",
		}, http.StatusInternalServerError)
		return
	}

	// Log the setup completion
	fingerprint := h.keyMgr.PublicKeyFingerprint()
	if h.auditLog != nil {
		h.auditLog.LogSetupComplete(1, clientIP, fingerprint)
	}

	// Return success response with public keys
	signingKey, encryptionKey := h.keyMgr.ExportPublicKeys()
	sendJSONResponse(w, SetupResponse{
		Success:             true,
		SigningPublicKey:    signingKey,
		EncryptionPublicKey: encryptionKey,
		Fingerprint:         fingerprint,
	}, http.StatusOK)

	log.Infof("Setup completed successfully. Server fingerprint: %s", fingerprint)
}

// sendJSONResponse sends a JSON response.
func sendJSONResponse(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// getClientIP extracts the client IP from the request.
func getClientIP(r *http.Request) string {
	remoteHost, _, _ := net.SplitHostPort(r.RemoteAddr)
	if remoteHost == "" {
		remoteHost = r.RemoteAddr
	}

	remoteIP := net.ParseIP(remoteHost)
	isTrustedProxy := remoteIP != nil && remoteIP.IsLoopback()

	if isTrustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if len(parts) > 0 {
				return strings.TrimSpace(parts[0])
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	return remoteHost
}

// formatTime formats minutes and seconds as "Xm Ys".
func formatTime(minutes, seconds int) string {
	if minutes > 0 {
		return strings.TrimSpace(strings.Replace(strings.Replace("%dm %ds", "%d", string(rune('0'+minutes%10)), 1), "%d", string(rune('0'+seconds%10)), 1))
	}
	return strings.Replace("%ds", "%d", string(rune('0'+seconds%10)), 1)
}

// buildServerEPM creates an EPM FlatBuffer for the server identity.
func buildServerEPM(serverName string, identity *keys.Identity) []byte {
	builder := flatbuffers.NewBuilder(1024)

	// Create string offsets
	dnOffset := builder.CreateString(serverName)
	legalNameOffset := builder.CreateString(serverName)

	// Create signing key
	signingKeyOffset := builder.CreateString(hex.EncodeToString(identity.SigningKey.PublicKey))
	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, signingKeyOffset)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	signingCryptoKey := EPM.CryptoKeyEnd(builder)

	// Create encryption key
	encryptionKeyOffset := builder.CreateString(hex.EncodeToString(identity.EncryptionKey.PublicKey))
	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, encryptionKeyOffset)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeEncryption)
	encryptionCryptoKey := EPM.CryptoKeyEnd(builder)

	// Create keys vector
	EPM.EPMStartKEYSVector(builder, 2)
	builder.PrependUOffsetT(encryptionCryptoKey)
	builder.PrependUOffsetT(signingCryptoKey)
	keysVectorOffset := builder.EndVector(2)

	// Build EPM
	EPM.EPMStart(builder)
	EPM.EPMAddDN(builder, dnOffset)
	EPM.EPMAddLEGAL_NAME(builder, legalNameOffset)
	EPM.EPMAddKEYS(builder, keysVectorOffset)
	epm := EPM.EPMEnd(builder)

	EPM.FinishSizePrefixedEPMBuffer(builder, epm)

	result := make([]byte, len(builder.FinishedBytes()))
	copy(result, builder.FinishedBytes())
	return result
}

// Setup page HTML template
var setupTemplate = template.Must(template.New("setup").Parse(`
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SDN Server Setup</title>
    <style>
        :root {
            --bg-color: #0a0a0f;
            --card-bg: #12121a;
            --border-color: #2a2a3a;
            --text-primary: #e0e0e0;
            --text-secondary: #8a8a9a;
            --accent-color: #4a9eff;
            --error-color: #ff4a4a;
            --success-color: #4aff4a;
        }
        * {
            box-sizing: border-box;
            margin: 0;
            padding: 0;
        }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, sans-serif;
            background: var(--bg-color);
            color: var(--text-primary);
            min-height: 100vh;
            display: flex;
            justify-content: center;
            align-items: center;
            padding: 20px;
        }
        .container {
            width: 100%;
            max-width: 480px;
        }
        .card {
            background: var(--card-bg);
            border: 1px solid var(--border-color);
            border-radius: 12px;
            padding: 32px;
        }
        .logo {
            text-align: center;
            margin-bottom: 24px;
        }
        .logo svg {
            width: 64px;
            height: 64px;
        }
        h1 {
            text-align: center;
            font-size: 24px;
            margin-bottom: 8px;
        }
        .subtitle {
            text-align: center;
            color: var(--text-secondary);
            margin-bottom: 24px;
        }
        .timer {
            text-align: center;
            padding: 12px;
            background: rgba(74, 158, 255, 0.1);
            border-radius: 8px;
            margin-bottom: 24px;
            font-family: monospace;
        }
        .timer.expired {
            background: rgba(255, 74, 74, 0.1);
            color: var(--error-color);
        }
        .form-group {
            margin-bottom: 20px;
        }
        label {
            display: block;
            margin-bottom: 8px;
            color: var(--text-secondary);
            font-size: 14px;
        }
        input {
            width: 100%;
            padding: 12px 16px;
            background: var(--bg-color);
            border: 1px solid var(--border-color);
            border-radius: 8px;
            color: var(--text-primary);
            font-size: 16px;
            transition: border-color 0.2s;
        }
        input:focus {
            outline: none;
            border-color: var(--accent-color);
        }
        input::placeholder {
            color: var(--text-secondary);
        }
        .token-input {
            font-family: monospace;
            text-transform: uppercase;
        }
        button {
            width: 100%;
            padding: 14px;
            background: var(--accent-color);
            border: none;
            border-radius: 8px;
            color: white;
            font-size: 16px;
            font-weight: 600;
            cursor: pointer;
            transition: opacity 0.2s;
        }
        button:hover {
            opacity: 0.9;
        }
        button:disabled {
            opacity: 0.5;
            cursor: not-allowed;
        }
        .message {
            padding: 12px;
            border-radius: 8px;
            margin-bottom: 16px;
            text-align: center;
        }
        .message.error {
            background: rgba(255, 74, 74, 0.1);
            color: var(--error-color);
        }
        .message.success {
            background: rgba(74, 255, 74, 0.1);
            color: var(--success-color);
        }
        .result {
            margin-top: 24px;
            padding: 16px;
            background: var(--bg-color);
            border-radius: 8px;
        }
        .result h3 {
            margin-bottom: 12px;
            font-size: 16px;
        }
        .key-display {
            font-family: monospace;
            font-size: 12px;
            word-break: break-all;
            padding: 8px;
            background: rgba(74, 158, 255, 0.1);
            border-radius: 4px;
            margin-bottom: 8px;
        }
        .fingerprint {
            color: var(--accent-color);
            font-family: monospace;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="card">
            <div class="logo">
                <svg viewBox="0 0 64 64" fill="none" xmlns="http://www.w3.org/2000/svg">
                    <circle cx="32" cy="32" r="28" stroke="#4a9eff" stroke-width="2"/>
                    <ellipse cx="32" cy="32" rx="28" ry="10" stroke="#4a9eff" stroke-width="1.5"/>
                    <ellipse cx="32" cy="32" rx="28" ry="10" stroke="#4a9eff" stroke-width="1.5" transform="rotate(60 32 32)"/>
                    <ellipse cx="32" cy="32" rx="28" ry="10" stroke="#4a9eff" stroke-width="1.5" transform="rotate(-60 32 32)"/>
                    <circle cx="32" cy="32" r="4" fill="#4a9eff"/>
                </svg>
            </div>
            <h1>Space Data Network</h1>
            <p class="subtitle">First-Time Server Setup</p>

            {{if .SetupComplete}}
            <div class="message success">Setup already complete. Redirecting to login...</div>
            {{else if .TokenExpired}}
            <div class="timer expired">Token Expired - Restart server for new token</div>
            {{else}}
            <div class="timer" id="timer">Token expires in: {{.RemainingTime}}</div>
            {{end}}

            <div id="error-msg" class="message error" style="display: none;"></div>
            <div id="success-msg" class="message success" style="display: none;"></div>

            <form id="setup-form" {{if or .SetupComplete .TokenExpired}}style="display:none;"{{end}}>
                <div class="form-group">
                    <label for="token">Setup Token</label>
                    <input type="text" id="token" name="token" class="token-input"
                           placeholder="SETUP-XXXX-XXXX-..." required>
                </div>
                <div class="form-group">
                    <label for="username">Admin Username</label>
                    <input type="text" id="username" name="username"
                           placeholder="admin" minlength="3" required>
                </div>
                <div class="form-group">
                    <label for="password">Admin Password</label>
                    <input type="password" id="password" name="password"
                           placeholder="At least 8 characters" minlength="8" required>
                </div>
                <div class="form-group">
                    <label for="server_name">Server Name (optional)</label>
                    <input type="text" id="server_name" name="server_name"
                           placeholder="Space Data Network Server">
                </div>
                <button type="submit" id="submit-btn">Complete Setup</button>
            </form>

            <div id="result" class="result" style="display: none;">
                <h3>Setup Complete</h3>
                <p>Your server identity fingerprint:</p>
                <p class="fingerprint" id="fingerprint"></p>
                <p style="margin-top: 16px; color: var(--text-secondary); font-size: 14px;">
                    Save this fingerprint - it uniquely identifies your server.
                </p>
                <button onclick="window.location.href='/login'" style="margin-top: 16px;">
                    Go to Login
                </button>
            </div>
        </div>
    </div>

    <script>
        const form = document.getElementById('setup-form');
        const errorMsg = document.getElementById('error-msg');
        const successMsg = document.getElementById('success-msg');
        const result = document.getElementById('result');
        const submitBtn = document.getElementById('submit-btn');

        form.addEventListener('submit', async (e) => {
            e.preventDefault();

            errorMsg.style.display = 'none';
            successMsg.style.display = 'none';
            submitBtn.disabled = true;
            submitBtn.textContent = 'Setting up...';

            const data = {
                token: document.getElementById('token').value,
                username: document.getElementById('username').value,
                password: document.getElementById('password').value,
                server_name: document.getElementById('server_name').value
            };

            try {
                const response = await fetch('/api/setup', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(data)
                });

                const json = await response.json();

                if (json.success) {
                    form.style.display = 'none';
                    document.getElementById('timer').style.display = 'none';
                    document.getElementById('fingerprint').textContent = json.fingerprint;
                    result.style.display = 'block';
                } else {
                    errorMsg.textContent = json.error;
                    errorMsg.style.display = 'block';
                    submitBtn.disabled = false;
                    submitBtn.textContent = 'Complete Setup';
                }
            } catch (err) {
                errorMsg.textContent = 'Connection error. Please try again.';
                errorMsg.style.display = 'block';
                submitBtn.disabled = false;
                submitBtn.textContent = 'Complete Setup';
            }
        });

        // Auto-format token input
        document.getElementById('token').addEventListener('input', (e) => {
            e.target.value = e.target.value.toUpperCase();
        });
    </script>
</body>
</html>
`))
