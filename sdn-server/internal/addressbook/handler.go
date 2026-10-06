package addressbook

// The book over HTTP. The public route is the only one a visitor reaches, and
// it never holds a private entry; the caller gates the operator routes.
//
//	GET    /api/v1/address-book                 public entries          (anyone)
//	GET    /api/v1/address-book/entries         every entry + default   (operator)
//	POST   /api/v1/address-book/entries         sign a card into it     (operator)
//	PATCH  /api/v1/address-book/entries/{id}    change its visibility   (operator)
//	DELETE /api/v1/address-book/entries/{id}    revoke it               (operator)
//	GET    /api/v1/address-book/settings        the default visibility  (operator)
//	PUT    /api/v1/address-book/settings
//
// POST takes {"peer_id"} (a card this node holds for that node), {"vcard"}, or
// {"qr"} (a PNG or JPEG of a QR code, base64 or a data: URL), and optionally
// {"visibility"}.

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/vcard"
)

const (
	PublicPath   = "/api/v1/address-book"
	OperatorPath = "/api/v1/address-book/"

	maxRequestBytes = 12 << 20
	maxQRSide       = 8192
	maxQRPixels     = 40 << 20
)

// Handler serves a Book. ResolvePeer returns the card this node holds for a
// peer ID, or nil; HeldProfiles lists every card it holds for other nodes.
type Handler struct {
	Book         *Book
	ResolvePeer  func(peerID string) []byte
	HeldProfiles func() [][]byte
}

type attestationJSON struct {
	Standard      string `json:"standard"`
	Signer        string `json:"signer"`
	Key           string `json:"key"`
	Algorithm     string `json:"algorithm"`
	Signature     string `json:"signature"`
	ProfileSHA256 string `json:"profile_sha256"`
	Record        string `json:"record"` // the size-prefixed $ABA record, base64
}

type entryJSON struct {
	ID          string          `json:"id"`
	PeerID      string          `json:"peer_id,omitempty"`
	VCard       string          `json:"vcard"`
	AttestedAt  string          `json:"attested_at"`
	UpdatedAt   string          `json:"updated_at"`
	Visibility  string          `json:"visibility"`
	Attestation attestationJSON `json:"attestation"`
}

func toJSON(e Entry) entryJSON {
	return entryJSON{
		ID:         e.EntryID,
		PeerID:     e.PeerID,
		VCard:      e.VCard,
		AttestedAt: stamp(e.CreatedAt),
		UpdatedAt:  stamp(e.UpdatedAt),
		Visibility: e.Visibility.String(),
		Attestation: attestationJSON{
			Standard:      "$ABA",
			Signer:        e.NodePeerID,
			Key:           hex.EncodeToString(e.PublicKey),
			Algorithm:     e.Algorithm,
			Signature:     hex.EncodeToString(e.Signature),
			ProfileSHA256: hex.EncodeToString(e.ProfileSHA256),
			Record:        base64.StdEncoding.EncodeToString(e.Frame()),
		},
	}
}

func stamp(ms uint64) string {
	return time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339Nano)
}

func listJSON(entries []Entry) []entryJSON {
	out := make([]entryJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, toJSON(e))
	}
	return out
}

// ServePublic answers the public route with the public entries only.
func (h *Handler) ServePublic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		problem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET.")
		return
	}
	entries, err := h.Book.Entries(false)
	if err != nil {
		problem(w, http.StatusInternalServerError, "unavailable", "The address book is unavailable.")
		return
	}
	reply(w, r, http.StatusOK, map[string]any{"entries": listJSON(entries)})
}

// ServeHTTP answers the operator routes.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, OperatorPath)
	switch {
	case rest == "entries" && r.Method == http.MethodGet:
		h.list(w, r)
	case rest == "entries" && r.Method == http.MethodPost:
		h.add(w, r)
	case strings.HasPrefix(rest, "entries/") && r.Method == http.MethodPatch:
		h.change(w, r, strings.TrimPrefix(rest, "entries/"))
	case strings.HasPrefix(rest, "entries/") && r.Method == http.MethodDelete:
		h.remove(w, r, strings.TrimPrefix(rest, "entries/"))
	case rest == "settings" && r.Method == http.MethodGet:
		h.settings(w, r)
	case rest == "settings" && r.Method == http.MethodPut:
		h.setSettings(w, r)
	case rest == "entries" || strings.HasPrefix(rest, "entries/") || rest == "settings":
		problem(w, http.StatusMethodNotAllowed, "method_not_allowed", "That method is not allowed here.")
	default:
		problem(w, http.StatusNotFound, "not_found", "Not found.")
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	entries, err := h.Book.Entries(true)
	if err != nil {
		problem(w, http.StatusInternalServerError, "unavailable", "The address book is unavailable.")
		return
	}
	v, err := h.Book.DefaultVisibility()
	if err != nil {
		problem(w, http.StatusInternalServerError, "unavailable", "The address book settings are unavailable.")
		return
	}
	reply(w, r, http.StatusOK, map[string]any{"entries": listJSON(entries), "default_visibility": v.String()})
}

type addRequest struct {
	PeerID     string `json:"peer_id"`
	VCard      string `json:"vcard"`
	QR         string `json:"qr"`
	Visibility string `json:"visibility"`
}

func (h *Handler) add(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if !decode(w, r, &req) {
		return
	}
	var visibility *Visibility
	if req.Visibility != "" {
		v, ok := ParseVisibility(req.Visibility)
		if !ok {
			problem(w, http.StatusBadRequest, "bad_visibility", "Visibility is public or private.")
			return
		}
		visibility = &v
	}
	profile, status, message := h.profile(req)
	if message != "" {
		problem(w, status, "bad_card", message)
		return
	}
	entry, created, err := h.Book.Add(profile, visibility)
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "not_signed", "The card could not be signed into the address book: "+err.Error()+".")
		return
	}
	status = http.StatusOK
	if created {
		status = http.StatusCreated
	}
	reply(w, r, status, map[string]any{"entry": toJSON(entry)})
}

// profile turns a request into the EPM to attest.
func (h *Handler) profile(req addRequest) ([]byte, int, string) {
	given := 0
	for _, s := range []string{req.PeerID, req.VCard, req.QR} {
		if strings.TrimSpace(s) != "" {
			given++
		}
	}
	if given != 1 {
		return nil, http.StatusBadRequest, "Send one of peer_id, vcard or qr."
	}
	switch {
	case strings.TrimSpace(req.PeerID) != "":
		id := strings.TrimSpace(req.PeerID)
		var held []byte
		if h.ResolvePeer != nil {
			held = h.ResolvePeer(id)
		}
		if len(held) == 0 {
			return nil, http.StatusNotFound, "This node holds no card for that node."
		}
		held, err := sizePrefixedEPM(held)
		if err != nil || epm.VerifyEPMSignature(held) != nil {
			return nil, http.StatusUnprocessableEntity, "That node's card is not signed by it."
		}
		if named, _ := epm.PeerIDFromEPM(held); named != id {
			return nil, http.StatusUnprocessableEntity, "That card names another node."
		}
		return held, 0, ""
	case strings.TrimSpace(req.VCard) != "":
		return h.cardProfile(req.VCard, "That is not a vCard this node can read.")
	default:
		data := strings.TrimSpace(req.QR)
		if i := strings.Index(data, ","); strings.HasPrefix(data, "data:") && i > 0 {
			data = data[i+1:]
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, http.StatusBadRequest, "The QR image is not base64."
		}
		// A small file can declare a vast picture; measure before decoding.
		size, _, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			return nil, http.StatusUnprocessableEntity, "The QR image must be a PNG or a JPEG."
		}
		if size.Width > maxQRSide || size.Height > maxQRSide || size.Width*size.Height > maxQRPixels {
			return nil, http.StatusUnprocessableEntity, "The QR image is too large. Crop it to the code."
		}
		img, _, err := image.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, http.StatusUnprocessableEntity, "The QR image must be a PNG or a JPEG."
		}
		text, err := vcard.QRImageToVCard(img)
		if err != nil {
			return nil, http.StatusUnprocessableEntity, "No QR code could be read in that image."
		}
		return h.cardProfile(text, "That QR code does not hold a contact card.")
	}
}

// cardProfile is the EPM for a vCard. A compact card whose sign key signed a
// node record this node holds, as a node's QR code is, enters as that record.
func (h *Handler) cardProfile(text, refusal string) ([]byte, int, string) {
	if key := vcard.SignKeyFromVCard(text); len(key) > 0 && h.HeldProfiles != nil {
		for _, held := range h.HeldProfiles() {
			if record, err := sizePrefixedEPM(held); err == nil && epm.VerifyEPMSignatureBindingKey(record, key) == nil {
				return record, 0, ""
			}
		}
	}
	profile, err := vcard.VCardToEPM(text)
	if err != nil || len(profile) == 0 {
		return nil, http.StatusUnprocessableEntity, refusal
	}
	return profile, 0, ""
}

type changeRequest struct {
	Visibility string `json:"visibility"`
}

func (h *Handler) change(w http.ResponseWriter, r *http.Request, id string) {
	var req changeRequest
	if !decode(w, r, &req) {
		return
	}
	v, ok := ParseVisibility(req.Visibility)
	if !ok {
		problem(w, http.StatusBadRequest, "bad_visibility", "Visibility is public or private.")
		return
	}
	entry, err := h.Book.SetVisibility(id, v)
	if errors.Is(err, ErrNotFound) {
		problem(w, http.StatusNotFound, "not_found", "No such entry.")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "not_signed", "The change could not be signed.")
		return
	}
	reply(w, r, http.StatusOK, map[string]any{"entry": toJSON(entry)})
}

func (h *Handler) remove(w http.ResponseWriter, _ *http.Request, id string) {
	err := h.Book.Remove(id)
	if errors.Is(err, ErrNotFound) {
		problem(w, http.StatusNotFound, "not_found", "No such entry.")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "not_signed", "The removal could not be signed.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	v, err := h.Book.DefaultVisibility()
	if err != nil {
		problem(w, http.StatusInternalServerError, "unavailable", "The address book settings are unavailable.")
		return
	}
	reply(w, r, http.StatusOK, settings{DefaultVisibility: v.String()})
}

func (h *Handler) setSettings(w http.ResponseWriter, r *http.Request) {
	var req settings
	if !decode(w, r, &req) {
		return
	}
	v, ok := ParseVisibility(req.DefaultVisibility)
	if !ok {
		problem(w, http.StatusBadRequest, "bad_visibility", "Visibility is public or private.")
		return
	}
	if err := h.Book.SetDefaultVisibility(v); err != nil {
		problem(w, http.StatusInternalServerError, "unavailable", "The address book settings could not be saved.")
		return
	}
	reply(w, r, http.StatusOK, settings{DefaultVisibility: v.String()})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(v); err != nil {
		problem(w, http.StatusBadRequest, "bad_request", "The request body must be JSON.")
		return false
	}
	return true
}

func reply(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func problem(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}
