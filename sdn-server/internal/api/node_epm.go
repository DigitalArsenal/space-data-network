package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
)

const maxNodeEPMBytes = 6 << 20

// NodeEPMService is the node identity wire lane. *epm.Service satisfies it.
type NodeEPMService interface {
	GetNodeEPM() []byte
	GetNodeEPMCID() (string, error)
	UpdateProfileFromEPM([]byte) error
}

// NodeEPMCommit stores, pins, and publishes the publisher-signed record after
// the service has rebuilt it. The executable supplies the node-specific lane.
type NodeEPMCommit func(context.Context, []byte) error

// NewNodeEPMHandler serves the raw size-prefixed $EPM contract for
// GET/PUT /api/node/epm. JSON is a derived read projection on the separate
// /api/node/epm/json route and is never accepted here.
func NewNodeEPMHandler(service NodeEPMService, commit NodeEPMCommit) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			nodeEPMError(w, http.StatusServiceUnavailable, "epm_unavailable", "The node identity is unavailable.")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeNodeEPM(w, service)
		case http.MethodPut:
			storeNodeEPM(w, r, service, commit)
		default:
			w.Header().Set("Allow", "GET, PUT")
			nodeEPMError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET or PUT.")
		}
	})
}

func storeNodeEPM(w http.ResponseWriter, r *http.Request, service NodeEPMService, commit NodeEPMCommit) {
	if commit == nil {
		nodeEPMError(w, http.StatusNotImplemented, "epm_store_unavailable", "This node cannot store and publish its identity.")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != epm.EPMContentType {
		nodeEPMError(w, http.StatusUnsupportedMediaType, "flatbuffer_required", "JSON profiles are not accepted. Send a size-prefixed EPM FlatBuffer.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxNodeEPMBytes+1))
	if err != nil {
		nodeEPMError(w, http.StatusBadRequest, "invalid_request", "The EPM record could not be read.")
		return
	}
	if len(body) > maxNodeEPMBytes {
		nodeEPMError(w, http.StatusRequestEntityTooLarge, "epm_too_large", fmt.Sprintf("The EPM record must be %d bytes or smaller.", maxNodeEPMBytes))
		return
	}
	if err := service.UpdateProfileFromEPM(body); err != nil {
		status := http.StatusInternalServerError
		code := "server_error"
		if errors.Is(err, epm.ErrInvalidProfileEPM) || epm.IsKeyPathValidationError(err) {
			status = http.StatusBadRequest
			code = "invalid_epm"
		}
		nodeEPMError(w, status, code, err.Error())
		return
	}
	stored := service.GetNodeEPM()
	if len(stored) == 0 {
		nodeEPMError(w, http.StatusInternalServerError, "server_error", "The signed node identity is unavailable.")
		return
	}
	if err := commit(r.Context(), stored); err != nil {
		nodeEPMError(w, http.StatusBadGateway, "store_failed", "The node could not store and publish its identity.")
		return
	}
	writeNodeEPM(w, service)
}

func writeNodeEPM(w http.ResponseWriter, service NodeEPMService) {
	data := service.GetNodeEPM()
	if len(data) == 0 {
		nodeEPMError(w, http.StatusNotFound, "not_found", "No node identity is available.")
		return
	}
	if cid, err := service.GetNodeEPMCID(); err == nil && cid != "" {
		w.Header().Set("X-SDN-EPM-CID", cid)
	}
	w.Header().Set("Content-Type", epm.EPMContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func nodeEPMError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
