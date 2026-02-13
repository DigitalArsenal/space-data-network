package wasiplugin

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const maxRequestBodySize = 16 * 1024 // 16KB — plenty for key exchange packets

// Handler provides HTTP handlers for the OrbPro key broker binary protocol.
type Handler struct {
	runtime *Runtime
}

// NewHandler creates an HTTP handler backed by the given WASI plugin runtime.
func NewHandler(rt *Runtime) *Handler {
	return &Handler{runtime: rt}
}

// HandlePublicKey serves GET requests for the server's P-256 public key and
// allowed-domain metadata. Response is JSON matching the OrbPro client expectation.
func (h *Handler) HandlePublicKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	pubKey, err := h.runtime.GetPublicKey(ctx)
	if err != nil {
		log.Errorf("GetPublicKey failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	metadata, err := h.runtime.GetMetadata(ctx)
	if err != nil {
		log.Errorf("GetMetadata failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	domains := parseBinaryDomains(metadata)

	resp := map[string]interface{}{
		"publicKey": hex.EncodeToString(pubKey),
		"keyKind":   2, // P-256 uncompressed
		"domains":   domains,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	json.NewEncoder(w).Encode(resp)
}

// HandleKeyExchange serves POST requests for the binary P-256 ECDH key
// exchange. Request and response bodies are opaque binary packets defined
// by the OrbPro protection runtime protocol.
func (h *Handler) HandleKeyExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	response, status, err := h.runtime.HandleRequest(ctx, body, r.Host)
	if err != nil {
		log.Errorf("HandleRequest failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if status != 0 {
		log.Debugf("key exchange returned protocol status %d", status)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	if response != nil {
		w.Write(response)
	}
}

// HandleUI serves the plugin's embedded admin UI page. This is rendered
// inside an iframe on the Plugins page in the SDN web client.
func (h *Handler) HandleUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	pubKey, err := h.runtime.GetPublicKey(ctx)
	if err != nil {
		log.Errorf("GetPublicKey failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	metadata, err := h.runtime.GetMetadata(ctx)
	if err != nil {
		log.Errorf("GetMetadata failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	domains := parseBinaryDomains(metadata)
	pubKeyHex := hex.EncodeToString(pubKey)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")

	domainsHTML := ""
	for _, d := range domains {
		domainsHTML += "<li>" + d + "</li>"
	}

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>OrbPro Key Broker</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; padding: 24px; color: #1e293b; line-height: 1.6; }
  h2 { font-size: 1.25rem; margin-bottom: 16px; }
  .card { background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 8px; padding: 16px; margin-bottom: 16px; }
  .card h3 { font-size: 0.875rem; color: #64748b; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 8px; }
  .mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 0.8rem; word-break: break-all; color: #0f172a; }
  ul { margin: 0; padding-left: 20px; }
  li { margin-bottom: 4px; }
  .status { display: inline-flex; align-items: center; gap: 6px; font-size: 0.875rem; font-weight: 500; }
  .status-dot { width: 10px; height: 10px; border-radius: 50%%; background: #34d399; }
</style>
</head>
<body>
  <h2>OrbPro Key Broker</h2>
  <p class="status"><span class="status-dot"></span> Running</p>

  <div class="card" style="margin-top: 16px;">
    <h3>Server Public Key (P-256)</h3>
    <p class="mono">%s</p>
  </div>

  <div class="card">
    <h3>Allowed Domains</h3>
    <ul>%s</ul>
  </div>

  <div class="card">
    <h3>Endpoints</h3>
    <ul>
      <li><code>GET /orbpro-key-broker/v1/orbpro/public-key</code></li>
      <li><code>POST /orbpro-key-broker/v1/orbpro/key</code></li>
    </ul>
  </div>
</body>
</html>`, pubKeyHex, domainsHTML)
}

// parseBinaryDomains decodes the plugin_get_metadata binary format:
// domainCount(4 LE) + [domainLen(2 LE) + domain(N)]...
func parseBinaryDomains(metadata []byte) []string {
	if len(metadata) < 4 {
		return nil
	}
	count := binary.LittleEndian.Uint32(metadata[:4])
	offset := 4
	domains := make([]string, 0, count)
	for i := uint32(0); i < count && offset+2 <= len(metadata); i++ {
		dlen := int(binary.LittleEndian.Uint16(metadata[offset : offset+2]))
		offset += 2
		if offset+dlen > len(metadata) {
			break
		}
		domains = append(domains, string(metadata[offset:offset+dlen]))
		offset += dlen
	}
	return domains
}
