package api

import (
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/compute"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

const (
	// MaxComputeRequestBytes bounds incoming $PIV compute payloads.
	MaxComputeRequestBytes int64 = 64 << 20

	// ComputeFallbackHeader tells clients to execute locally in browser WASM.
	ComputeFallbackHeader = "X-SDN-Compute-Fallback"
	// ComputeModuleHeader selects the target plugin ID out-of-band.
	ComputeModuleHeader = "X-SDN-Module-Id"
	// ComputeResponseModuleHeader echoes the executing plugin ID.
	ComputeResponseModuleHeader = "X-SDN-Module-ID"
	// ComputeWasmSHA256Header echoes the executing WASM SHA-256 hex digest.
	ComputeWasmSHA256Header = "X-SDN-Wasm-Sha256"

	flatbuffersBareContentType = "application/x-flatbuffers"
)

// ComputeHandler serves POST /api/v1/compute/invoke and admin entitlement updates
// over FlatBuffers-only frames.
type ComputeHandler struct {
	service     *compute.Service
	resolveAuth func() *auth.Handler
	rl          *rateLimiter
}

// NewComputeHandler creates a FlatBuffers-only compute offload HTTP handler.
func NewComputeHandler(service *compute.Service, resolveAuth func() *auth.Handler) *ComputeHandler {
	return &ComputeHandler{
		service:     service,
		resolveAuth: resolveAuth,
		rl:          newRateLimiter(),
	}
}

// RegisterRoutes mounts the compute offload endpoints on mux.
func (h *ComputeHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/compute/invoke", h.handleInvoke)
	mux.HandleFunc("/api/v1/admin/entitlements", h.handleAdminEntitlements)
}

func (h *ComputeHandler) authHandler() *auth.Handler {
	if h == nil || h.resolveAuth == nil {
		return nil
	}
	return h.resolveAuth()
}

func (h *ComputeHandler) handleInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "compute invoke requires POST with one $PIV FlatBuffer", 0)
		return
	}
	if h.rl != nil && !h.rl.Allow(w, r) {
		return
	}
	if h == nil || h.service == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "compute_unavailable", "compute service unavailable on this node", 0)
		return
	}

	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != StreamContentType && mediaType != flatbuffersBareContentType {
		WriteErrorFrame(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"compute invoke requires "+StreamContentType+" or "+flatbuffersBareContentType, 0)
		return
	}

	ah := h.authHandler()
	if ah == nil {
		w.Header().Set(ComputeFallbackHeader, license.DefaultComputeFallbackSignal)
		WriteErrorFrame(w, http.StatusPaymentRequired, "subscription_required", "authentication and active subscription required; fallback=browser-local", 0)
		return
	}

	session, err := ah.SessionFromRequestOrSigned(r)
	if err != nil || session == nil || strings.TrimSpace(session.XPub) == "" {
		w.Header().Set(ComputeFallbackHeader, license.DefaultComputeFallbackSignal)
		WriteErrorFrame(w, http.StatusPaymentRequired, "subscription_required", "authentication and active subscription required; fallback=browser-local", 0)
		return
	}

	ent, authErr := h.service.AuthorizeXPub(session.XPub)
	if authErr != nil {
		if authErr.FallbackSignal != "" {
			w.Header().Set(ComputeFallbackHeader, authErr.FallbackSignal)
		}
		WriteErrorFrame(w, authErr.HTTPStatus, authErr.Code, authErr.Message, 0)
		return
	}

	if r.Body == nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "request body is required", 0)
		return
	}
	defer r.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(r.Body, MaxComputeRequestBytes+1))
	if readErr != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "failed to read $PIV request body", 0)
		return
	}
	if int64(len(body)) > MaxComputeRequestBytes {
		WriteErrorFrame(w, http.StatusRequestEntityTooLarge, "too_large", "$PIV request exceeds maximum size", 0)
		return
	}

	moduleHint := strings.TrimSpace(r.Header.Get(ComputeModuleHeader))
	flusher, _ := w.(http.Flusher)
	headerWritten := false

	meta, invokeErr := h.service.Invoke(r.Context(), ent, moduleHint, body, func(frameMeta compute.InvokeMetadata, frame modulert.InvokeOutputFrame) error {
		if !headerWritten {
			hdr := w.Header()
			hdr.Set("Content-Type", StreamContentType)
			hdr.Set(StreamFormatHeader, StreamFormat)
			hdr.Set("Cache-Control", "no-store")
			if frameMeta.ModuleID != "" {
				hdr.Set(ComputeResponseModuleHeader, frameMeta.ModuleID)
			} else if moduleHint != "" {
				hdr.Set(ComputeResponseModuleHeader, moduleHint)
			}
			if frameMeta.WasmSHA256 != "" {
				hdr.Set(ComputeWasmSHA256Header, frameMeta.WasmSHA256)
			}
			w.WriteHeader(http.StatusOK)
			headerWritten = true
		}
		if _, werr := w.Write(compute.WriteSizePrefixedFrame(frame.Payload)); werr != nil {
			return werr
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})

	if invokeErr != nil {
		if !headerWritten {
			if invokeErr.FallbackSignal != "" {
				w.Header().Set(ComputeFallbackHeader, invokeErr.FallbackSignal)
			}
			WriteErrorFrame(w, invokeErr.HTTPStatus, invokeErr.Code, invokeErr.Message, 0)
			return
		}
		// Stream already started; emit a trailing $QRP error frame so the client
		// observes the failure cleanly on the stream boundary.
		_, _ = w.Write(syncwire.ErrorFrame(QRPStatusError, invokeErr.Code, invokeErr.Message, invokeErr.HTTPStatus, 0))
		if flusher != nil {
			flusher.Flush()
		}
		return
	}

	if !headerWritten {
		hdr := w.Header()
		hdr.Set("Content-Type", StreamContentType)
		hdr.Set(StreamFormatHeader, StreamFormat)
		hdr.Set("Cache-Control", "no-store")
		if meta != nil {
			if meta.ModuleID != "" {
				hdr.Set(ComputeResponseModuleHeader, meta.ModuleID)
			}
			if meta.WasmSHA256 != "" {
				hdr.Set(ComputeWasmSHA256Header, meta.WasmSHA256)
			}
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (h *ComputeHandler) handleAdminEntitlements(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "entitlement upsert requires POST with one $ENT FlatBuffer", 0)
		return
	}
	if ah := h.authHandler(); ah != nil {
		if !ah.RequireTrust(w, r, peers.Admin) {
			return
		}
	}
	if h == nil || h.service == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "entitlements_unavailable", "entitlement service unavailable on this node", 0)
		return
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != StreamContentType && mediaType != flatbuffersBareContentType {
		WriteErrorFrame(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"entitlement upsert requires "+StreamContentType+" or "+flatbuffersBareContentType, 0)
		return
	}
	if r.Body == nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "request body is required", 0)
		return
	}
	defer r.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestFrameBytes+1))
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "failed to read $ENT payload", 0)
		return
	}
	if int64(len(raw)) > MaxRequestFrameBytes {
		WriteErrorFrame(w, http.StatusRequestEntityTooLarge, "too_large", "$ENT payload exceeds maximum size", 0)
		return
	}

	ent, upsertErr := h.service.UpsertEntitlementRecord(raw)
	if upsertErr != nil {
		WriteErrorFrame(w, upsertErr.HTTPStatus, upsertErr.Code, upsertErr.Message, 0)
		return
	}

	writeTablePage(w, qrpTable{
		Status:  QRPStatusOk,
		Columns: []qrpCol{{Name: "plan", Type: colString}, {Name: "status", Type: colString}},
		Rows:    [][]qrpVal{{textVal(ent.Plan), textVal(ent.Status)}},
		AsOfMs:  time.Now().UnixMilli(),
	})
}
