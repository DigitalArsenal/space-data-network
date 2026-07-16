package sdnapi

// Node EPM / vCard / QR export API — a SEPARATE handler from the read-only
// NewHandler surface (like the credentials and runs handlers). It owns three
// exact routes under /sdn/v1/node/ and is mounted on the same loopback listener
// by the kubo plugin. Every route is GET. The signed node $EPM is resolved per
// request through the injected accessor (built from the node's libp2p identity;
// see sdn/nodeepm), so this handler holds no key material of its own.
//
//	GET /sdn/v1/node/epm?format=json|fb   node EPM as JSON (default) or size-
//	                                      prefixed FlatBuffer (application/octet-stream)
//	GET /sdn/v1/node/vcard                text/vcard attachment (peer id + keys)
//	GET /sdn/v1/node/qr[?size=NNN]        image/png QR of the signed EPM

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/ipfs/kubo/sdn/nodeepm"
)

// NodeEPMDeps is the live source the export API reads.
type NodeEPMDeps struct {
	// EPM returns the node's signed, size-prefixed $EPM FlatBuffer built from its
	// libp2p identity, or an error when the identity is unavailable/unsupported.
	// A nil EPM (or a nil return) makes every route report 503.
	EPM func() ([]byte, error)
}

type nodeEPMHandler struct {
	deps NodeEPMDeps
}

// NewNodeEPMHandler builds the node EPM export API. The returned handler owns
// the three /sdn/v1/node/{epm,vcard,qr} routes; mount them on the plugin's
// loopback listener.
func NewNodeEPMHandler(deps NodeEPMDeps) http.Handler {
	h := &nodeEPMHandler{deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sdn/v1/node/epm", h.epm)
	mux.HandleFunc("GET /sdn/v1/node/vcard", h.vcard)
	mux.HandleFunc("GET /sdn/v1/node/qr", h.qr)
	return mux
}

// epmBytes resolves the signed node EPM, or returns a written HTTP error and
// false when it is unavailable.
func (h *nodeEPMHandler) epmBytes(w http.ResponseWriter) ([]byte, bool) {
	if h.deps.EPM == nil {
		writeErr(w, http.StatusServiceUnavailable, "node EPM unavailable")
		return nil, false
	}
	b, err := h.deps.EPM()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "node EPM unavailable: "+err.Error())
		return nil, false
	}
	if len(b) == 0 {
		writeErr(w, http.StatusServiceUnavailable, "node EPM unavailable")
		return nil, false
	}
	return b, true
}

func (h *nodeEPMHandler) epm(w http.ResponseWriter, r *http.Request) {
	b, ok := h.epmBytes(w)
	if !ok {
		return
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "fb", "flatbuffer", "bin", "binary":
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", `attachment; filename="sdn-node.epm"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	default:
		js, err := nodeepm.EPMToJSON(b)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, js)
	}
}

func (h *nodeEPMHandler) vcard(w http.ResponseWriter, _ *http.Request) {
	b, ok := h.epmBytes(w)
	if !ok {
		return
	}
	card, err := nodeepm.EPMToVCard(b)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="sdn-node.vcf"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(card))
}

func (h *nodeEPMHandler) qr(w http.ResponseWriter, r *http.Request) {
	b, ok := h.epmBytes(w)
	if !ok {
		return
	}
	size := 0
	if s := strings.TrimSpace(r.URL.Query().Get("size")); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			size = n
		}
	}
	png, err := nodeepm.EPMToQR(b, size)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}
