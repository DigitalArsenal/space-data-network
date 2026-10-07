package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
)

// maxNodePhotoRequestBytes bounds the JSON body: the photo as base64 (4/3 of
// epm.MaxNodePhotoBytes) plus its data URL header and the JSON around it.
const maxNodePhotoRequestBytes = epm.MaxNodePhotoBytes*4/3 + 4096

// NodePhotoService sets the node's photo. *epm.Service satisfies it.
type NodePhotoService interface {
	SetNodePhoto(string) error
	GetNodeEPM() []byte
}

type nodePhotoRequest struct {
	PhotoDataURL string `json:"photo_data_url"`
}

// NewNodePhotoHandler serves PUT /api/node/epm/photo, the node's photo (owner
// 2026-10-07: an uploaded photo "did NOT replace the image and did not survive
// the refresh"). The $EPM schema has no photo field, so PUT /api/node/epm
// cannot carry one and keeps the stored photo by design; this route sets it.
// The body is {"photo_data_url": "data:image/jpeg;base64,…"}; an empty value
// removes the photo. The re-signed record is committed like any profile edit,
// and the photo appears on the node's card.
func NewNodePhotoHandler(service NodePhotoService, commit NodeEPMCommit) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			nodeEPMError(w, http.StatusServiceUnavailable, "epm_unavailable", "The node identity is unavailable.")
			return
		}
		if r.Method != http.MethodPut {
			w.Header().Set("Allow", "PUT")
			nodeEPMError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use PUT.")
			return
		}
		if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
			nodeEPMError(w, http.StatusUnsupportedMediaType, "json_required", `Send {"photo_data_url": "data:image/jpeg;base64,…"} as application/json.`)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxNodePhotoRequestBytes+1))
		if err != nil {
			nodeEPMError(w, http.StatusBadRequest, "invalid_request", "The photo could not be read.")
			return
		}
		if len(body) > maxNodePhotoRequestBytes {
			nodeEPMError(w, http.StatusRequestEntityTooLarge, "photo_too_large",
				fmt.Sprintf("The photo must be %d bytes or smaller.", epm.MaxNodePhotoBytes))
			return
		}
		var request nodePhotoRequest
		if err := json.Unmarshal(body, &request); err != nil {
			nodeEPMError(w, http.StatusBadRequest, "invalid_request", `Send {"photo_data_url": "…"}.`)
			return
		}
		if err := service.SetNodePhoto(request.PhotoDataURL); err != nil {
			if errors.Is(err, epm.ErrInvalidNodePhoto) || epm.IsKeyPathValidationError(err) {
				nodeEPMError(w, http.StatusBadRequest, "invalid_photo", err.Error())
				return
			}
			nodeEPMError(w, http.StatusInternalServerError, "server_error", err.Error())
			return
		}
		if commit != nil {
			if err := commit(r.Context(), service.GetNodeEPM()); err != nil {
				nodeEPMError(w, http.StatusBadGateway, "store_failed", "The node could not store and publish its identity.")
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]bool{"photo": request.PhotoDataURL != ""})
	})
}
