package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

const storefrontUploadMaxBytes int64 = 64 << 20

// StorefrontDatasetSelection identifies one stored standard/source/batch lane.
// It is node-local request data, not an SDS record projection.
type StorefrontDatasetSelection struct {
	SchemaName string `json:"schema_name"`
	ProviderID string `json:"provider_id,omitempty"`
	SourceName string `json:"source_name,omitempty"`
	BatchID    string `json:"batch_id,omitempty"`
}

// StorefrontUploadReference identifies bytes already pinned through the node.
type StorefrontUploadReference struct {
	CID        string `json:"cid"`
	SHA256     string `json:"sha256"`
	ByteLength int64  `json:"byte_length"`
	FileName   string `json:"file_name"`
	MediaType  string `json:"media_type,omitempty"`
}

// StorefrontPublicationOptions records the operator's publication choices.
// The backend validates supported networks and applies the dataset selection.
type StorefrontPublicationOptions struct {
	AnnounceTo    []string `json:"announce_to"`
	PinRecords    bool     `json:"pin_records"`
	PinManifest   bool     `json:"pin_manifest"`
	RetentionDays uint32   `json:"retention_days"`
}

// StorefrontPublishCommand wraps the SDS listing draft with node-local source
// and publication choices. Listing remains raw so this API layer does not own
// or duplicate the SDS field contract implemented by internal/storefront.
type StorefrontPublishCommand struct {
	Listing     json.RawMessage              `json:"listing"`
	Dataset     *StorefrontDatasetSelection  `json:"dataset,omitempty"`
	Upload      *StorefrontUploadReference   `json:"upload,omitempty"`
	Publication StorefrontPublicationOptions `json:"publication"`
}

type StorefrontUpload struct {
	FileName  string
	MediaType string
	Data      []byte
}

// StorefrontListingBackend is the narrow seam between HTTP and the existing
// storefront service. Results are concrete JSON-ready projections owned by the
// backend, which already owns listing kinds, signatures, and index tables.
type StorefrontListingBackend interface {
	PublishableInventory(context.Context) (any, error)
	OwnListings(context.Context) (any, error)
	PublishListing(context.Context, StorefrontPublishCommand) (any, error)
	WithdrawListing(context.Context, string) (any, error)
	PinUpload(context.Context, StorefrontUpload) (any, error)
}

// RegisterStorefrontListingRoutes mounts the node-operator listing workflow.
// protect must apply the node's existing Admin authentication wrapper.
func RegisterStorefrontListingRoutes(mux *http.ServeMux, backend StorefrontListingBackend, protect func(http.HandlerFunc) http.HandlerFunc) {
	if protect == nil {
		protect = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}
	h := &storefrontListingHTTP{backend: backend}
	mux.HandleFunc("/api/v1/storefront/inventory", protect(h.inventory))
	mux.HandleFunc("/api/v1/storefront/listings/own", protect(h.ownListings))
	mux.HandleFunc("/api/v1/storefront/listings/publish", protect(h.publish))
	mux.HandleFunc("/api/v1/storefront/listings/", protect(h.listingAction))
	mux.HandleFunc("/api/v1/storefront/uploads", protect(h.upload))
}

type storefrontListingHTTP struct {
	backend StorefrontListingBackend
}

func (h *storefrontListingHTTP) inventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		storefrontMethodNotAllowed(w, http.MethodGet)
		return
	}
	if h.backend == nil {
		storefrontWriteError(w, http.StatusServiceUnavailable, "storefront unavailable")
		return
	}
	result, err := h.backend.PublishableInventory(r.Context())
	if err != nil {
		storefrontWriteError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *storefrontListingHTTP) ownListings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		storefrontMethodNotAllowed(w, http.MethodGet)
		return
	}
	if h.backend == nil {
		storefrontWriteError(w, http.StatusServiceUnavailable, "storefront unavailable")
		return
	}
	result, err := h.backend.OwnListings(r.Context())
	if err != nil {
		storefrontWriteError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *storefrontListingHTTP) publish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		storefrontMethodNotAllowed(w, http.MethodPost)
		return
	}
	if h.backend == nil {
		storefrontWriteError(w, http.StatusServiceUnavailable, "storefront unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		storefrontWriteError(w, http.StatusBadRequest, "invalid publication request")
		return
	}
	command, err := decodeStorefrontPublishCommand(raw)
	if err != nil {
		storefrontWriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.backend.PublishListing(r.Context(), command)
	if err != nil {
		storefrontWriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func decodeStorefrontPublishCommand(raw []byte) (StorefrontPublishCommand, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return StorefrontPublishCommand{}, errors.New("publication request is required")
	}
	var envelope StorefrontPublishCommand
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return StorefrontPublishCommand{}, errors.New("invalid publication request")
	}
	if len(envelope.Listing) != 0 && string(envelope.Listing) != "null" {
		return envelope, nil
	}
	// Preserve the existing raw SDS-draft request shape for operator tools that
	// already post directly to this route.
	envelope.Listing = append(json.RawMessage(nil), raw...)
	return envelope, nil
}

func (h *storefrontListingHTTP) listingAction(w http.ResponseWriter, r *http.Request) {
	if h.backend == nil {
		storefrontWriteError(w, http.StatusServiceUnavailable, "storefront unavailable")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/storefront/listings/"), "/")
	listingID := rest
	if strings.HasSuffix(rest, "/withdraw") {
		listingID = strings.TrimSuffix(rest, "/withdraw")
		if r.Method != http.MethodPost {
			storefrontMethodNotAllowed(w, http.MethodPost)
			return
		}
	} else if r.Method != http.MethodDelete {
		storefrontMethodNotAllowed(w, http.MethodDelete)
		return
	}
	listingID = strings.TrimSpace(listingID)
	if listingID == "" || strings.Contains(listingID, "/") {
		storefrontWriteError(w, http.StatusBadRequest, "listing ID is required")
		return
	}
	result, err := h.backend.WithdrawListing(r.Context(), listingID)
	if err != nil {
		storefrontWriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *storefrontListingHTTP) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		storefrontMethodNotAllowed(w, http.MethodPost)
		return
	}
	if h.backend == nil {
		storefrontWriteError(w, http.StatusServiceUnavailable, "storefront unavailable")
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		storefrontWriteError(w, http.StatusBadRequest, "a file upload is required")
		return
	}
	var upload StorefrontUpload
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			storefrontWriteError(w, http.StatusBadRequest, "invalid file upload")
			return
		}
		if part.FormName() != "file" || upload.Data != nil {
			_ = part.Close()
			storefrontWriteError(w, http.StatusBadRequest, "the upload must contain one file")
			return
		}
		upload.FileName = filepath.Base(strings.TrimSpace(part.FileName()))
		upload.MediaType = strings.TrimSpace(part.Header.Get("Content-Type"))
		upload.Data, err = io.ReadAll(io.LimitReader(part, storefrontUploadMaxBytes+1))
		_ = part.Close()
		if err != nil {
			storefrontWriteError(w, http.StatusBadRequest, "invalid file upload")
			return
		}
	}
	if upload.FileName == "" || len(upload.Data) == 0 {
		storefrontWriteError(w, http.StatusBadRequest, "the upload must contain one non-empty file")
		return
	}
	if int64(len(upload.Data)) > storefrontUploadMaxBytes {
		storefrontWriteError(w, http.StatusRequestEntityTooLarge, "file exceeds the 64 MiB upload limit")
		return
	}
	result, err := h.backend.PinUpload(r.Context(), upload)
	if err != nil {
		storefrontWriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func storefrontMethodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	storefrontWriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func storefrontWriteError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{
		"code":    http.StatusText(status),
		"message": message,
	})
}
