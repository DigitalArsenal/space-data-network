package addressbook

// The book over HTTP. Every entry is public (owner 2026-10-07: "all should be
// visible"); the caller gates the operator routes.
//
//	GET    /api/v1/address-book                 every entry             (anyone)
//	GET    /api/v1/address-book/entries         every entry             (operator)
//	POST   /api/v1/address-book/entries         sign a card into it     (operator)
//	DELETE /api/v1/address-book/entries/{id}    revoke it               (operator)
//
// POST takes {"peer_id"} (a card this node holds for that node), {"vcard"}, or
// {"qr"} (a PNG or JPEG of a QR code, base64 or a data: URL).

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
	Attestation attestationJSON `json:"attestation"`
}

func toJSON(e Entry) entryJSON {
	return entryJSON{
		ID:         e.EntryID,
		PeerID:     e.PeerID,
		VCard:      e.VCard,
		AttestedAt: stamp(e.CreatedAt),
		UpdatedAt:  stamp(e.UpdatedAt),
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

// ServePublic answers the public route with every entry.
func (h *Handler) ServePublic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		problem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET.")
		return
	}
	entries, err := h.Book.Entries()
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
	case strings.HasPrefix(rest, "entries/") && r.Method == http.MethodDelete:
		h.remove(w, r, strings.TrimPrefix(rest, "entries/"))
	case rest == "entries" || strings.HasPrefix(rest, "entries/"):
		problem(w, http.StatusMethodNotAllowed, "method_not_allowed", "That method is not allowed here.")
	default:
		problem(w, http.StatusNotFound, "not_found", "Not found.")
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	entries, err := h.Book.Entries()
	if err != nil {
		problem(w, http.StatusInternalServerError, "unavailable", "The address book is unavailable.")
		return
	}
	reply(w, r, http.StatusOK, map[string]any{"entries": listJSON(entries)})
}

type addRequest struct {
	PeerID string `json:"peer_id"`
	VCard  string `json:"vcard"`
	QR     string `json:"qr"`
}

func (h *Handler) add(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if !decode(w, r, &req) {
		return
	}
	profile, status, message := h.profile(req)
	if message != "" {
		problem(w, status, "bad_card", message)
		return
	}
	entry, created, err := h.Book.Add(profile)
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
	if strings.TrimSpace(req.PeerID) != "" {
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
	}
	return ReadCard(req.VCard, req.QR, h.HeldProfiles)
}

// ReadCard reads a contact card into the profile it carries, size-prefixed:
// a vCard's text, or else a picture of a QR code. On refusal the status and
// message say why.
func ReadCard(text, qr string, held func() [][]byte) ([]byte, int, string) {
	card, status, refusal := CardText(text, qr)
	if status != 0 {
		return nil, status, refusal
	}
	refusal = "That is not a vCard this node can read."
	if strings.TrimSpace(text) == "" {
		refusal = "That QR code does not hold a contact card."
	}
	return CardProfile(card, held, refusal)
}

// CardText is the vCard a request carries: its text, or else the card held
// by a picture of a QR code (PNG or JPEG, base64 or a data URL). The node's
// contacts read cards through it and CardProfile too.
func CardText(text, qr string) (string, int, string) {
	if strings.TrimSpace(text) != "" {
		return text, 0, ""
	}
	data := strings.TrimSpace(qr)
	if i := strings.Index(data, ","); strings.HasPrefix(data, "data:") && i > 0 {
		data = data[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", http.StatusBadRequest, "The QR image is not base64."
	}
	// A small file can declare a vast picture; measure before decoding.
	size, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", http.StatusUnprocessableEntity, "The QR image must be a PNG or a JPEG."
	}
	if size.Width > maxQRSide || size.Height > maxQRSide || size.Width*size.Height > maxQRPixels {
		return "", http.StatusUnprocessableEntity, "The QR image is too large. Crop it to the code."
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", http.StatusUnprocessableEntity, "The QR image must be a PNG or a JPEG."
	}
	card, err := vcard.QRImageToVCard(img)
	if err != nil {
		return "", http.StatusUnprocessableEntity, "No QR code could be read in that image."
	}
	return card, 0, ""
}

// CardProfile is the EPM for a vCard, size-prefixed. A compact card whose
// sign key signed a profile `held` returns, as a node's QR code is, enters
// as that record.
func CardProfile(text string, held func() [][]byte, refusal string) ([]byte, int, string) {
	if key := vcard.SignKeyFromVCard(text); len(key) > 0 && held != nil {
		for _, profile := range held() {
			if record, err := sizePrefixedEPM(profile); err == nil && epm.VerifyEPMSignatureBindingKey(record, key) == nil {
				return record, 0, ""
			}
		}
	}
	profile, err := vcard.VCardToEPM(text)
	if err != nil || len(profile) == 0 {
		return nil, http.StatusUnprocessableEntity, refusal
	}
	if profile, err = sizePrefixedEPM(profile); err != nil {
		return nil, http.StatusUnprocessableEntity, refusal
	}
	return profile, 0, ""
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
