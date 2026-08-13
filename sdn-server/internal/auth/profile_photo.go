package auth

// Profile photos — the operator's own picture, stored as an IPFS object and
// referenced from their vCard (owner directive 2026-07-30).
//
// # Why a REFERENCE and not an embedded blob
//
// The node's OWN card embeds its photo as base64 (epm.vcardPhotoLine, ENCODING=b)
// because that card must survive being handed to a stranger with nothing else.
// An operator photo is different: it is served by the node the operator signs in
// to, it can be large, and it is re-read on every account-screen load. Embedding
// it would put a megabyte of base64 into the operator registry — which is listed
// wholesale by every Admin and projected into the trust matrix — for a picture.
// So the object goes to IPFS and the card carries a URI.
//
// # Why the URI is a PATH and never a gateway URL
//
// PHOTO is written as `/ipfs/<cid>`, which THIS node serves. A public gateway
// URL (https://ipfs.io/ipfs/...) would make every viewer of this card fetch
// third-party bytes — breaking the node-UI rule that no surface loads
// external-origin bytes, and telling a gateway operator who is looking at whom.
// The CID is the portable part; the origin is the viewer's own node.
//
// # The connectors-only boundary
//
// This file contains NO knowledge of Kubo, IPFS RPC, or any storage protocol.
// It declares a PORT (ProfilePhotoStore) and calls it. main.go binds that port
// to the node's existing IPFS lane. A node that has not bound it fails CLOSED —
// the endpoint answers 501 and says so, rather than pretending to store a photo.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxProfilePhotoBytes bounds an uploaded picture. Camera captures and phone
// photos land far below this; anything above it is not a profile picture.
const maxProfilePhotoBytes = 4 << 20

// ProfilePhotoStore is the port this package needs to keep a picture: hand it
// bytes, receive the content identifier of the stored object. Everything about
// HOW the object is stored belongs to the implementation, not to auth.
type ProfilePhotoStore interface {
	PinProfilePhoto(ctx context.Context, data []byte, contentType string) (string, error)
}

// SetProfilePhotoStore binds the storage port. Leaving it unbound is a valid
// deployment: the endpoint then refuses honestly instead of failing silently.
func (h *Handler) SetProfilePhotoStore(store ProfilePhotoStore) {
	h.photoStore = store
}

// allowedProfilePhotoTypes is the closed set of formats a browser can both
// capture (canvas.toBlob) and render. It is a whitelist, not a blacklist: the
// bytes are sniffed and must land in here, so a renamed executable cannot be
// stored as somebody's face.
var allowedProfilePhotoTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
}

// handleMePhoto stores the caller's OWN profile picture and points their vCard
// at it. Self-scoped by construction, exactly like PATCH /api/auth/me: the row
// written is session.XPub and the request carries no identifier to tamper with.
func (h *Handler) handleMePhoto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session, err := h.sessionFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "not authenticated"})
		return
	}
	session = h.maybeRefreshSessionCookie(w, r, session)

	// A missing row is NOT an auth failure — the session already proved itself
	// (root-ceremony sessions have no row; me_session_tier_test.go pins this
	// for GET). But a photo reference is written INTO the row's vCard, so
	// without a row there is nowhere to keep it.
	user, err := h.userStore.GetUser(session.XPub)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Code: "no_profile_row", Message: "this session has no operator registry row to attach a photo to"})
		return
	}
	if user.Source == "config" {
		writeJSON(w, http.StatusConflict, errorResponse{
			Code:    "config_managed",
			Message: "this entry comes from the node config file and cannot be edited through the API",
		})
		return
	}

	store := h.photoStore
	if store == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{
			Code:    "photo_storage_unavailable",
			Message: "this node has no object storage bound, so it cannot keep a profile photo",
		})
		return
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, maxProfilePhotoBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "could not read the uploaded image"})
		return
	}
	if len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "no image was uploaded"})
		return
	}
	if len(data) > maxProfilePhotoBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{
			Code:    "image_too_large",
			Message: fmt.Sprintf("a profile photo must be %d bytes or smaller", maxProfilePhotoBytes),
		})
		return
	}

	// Sniff the BYTES. The Content-Type header is the uploader's claim about
	// their own file; it is not evidence, and this object becomes a URL this
	// node serves to other people.
	contentType := detectProfilePhotoType(data)
	if contentType == "" {
		writeJSON(w, http.StatusUnsupportedMediaType, errorResponse{
			Code:    "unsupported_image",
			Message: "a profile photo must be a PNG, JPEG, WebP or GIF image",
		})
		return
	}

	cid, err := store.PinProfilePhoto(r.Context(), data, contentType)
	if err != nil {
		log.Warnf("profile photo storage failed for %s: %v", XPubFingerprint(session.XPub), err)
		writeJSON(w, http.StatusBadGateway, errorResponse{
			Code:    "photo_storage_failed",
			Message: "the node could not store the image",
		})
		return
	}
	cid = strings.TrimSpace(cid)
	if cid == "" {
		writeJSON(w, http.StatusBadGateway, errorResponse{
			Code:    "photo_storage_failed",
			Message: "object storage returned no content identifier",
		})
		return
	}

	updated := setVCardPhotoReference(user.VCardData, cid, contentType, user.Name)
	if err := h.userStore.UpdateProfile(session.XPub, ProfileUpdate{VCardData: &updated}); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "update_failed", Message: err.Error()})
		return
	}

	refreshed, err := h.userStore.GetUser(session.XPub)
	if err != nil || refreshed == nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: "profile could not be re-read"})
		return
	}
	writeJSON(w, http.StatusOK, h.meBody(session, refreshed))
}

// detectProfilePhotoType returns the canonical media type of data when it is an
// image this node is willing to serve, and "" otherwise.
func detectProfilePhotoType(data []byte) string {
	sniffed := http.DetectContentType(data)
	if base, _, ok := strings.Cut(sniffed, ";"); ok {
		sniffed = base
	}
	sniffed = strings.ToLower(strings.TrimSpace(sniffed))
	if _, allowed := allowedProfilePhotoTypes[sniffed]; !allowed {
		return ""
	}
	return sniffed
}

// profilePhotoPath is the same-origin path this node serves an object from.
func profilePhotoPath(cid string) string {
	trimmed := strings.TrimSpace(cid)
	if trimmed == "" {
		return ""
	}
	return "/ipfs/" + trimmed
}

// vCardPhotoReference reads back the PHOTO reference this package writes,
// returning the served path and the bare CID. A vCard whose PHOTO is an
// embedded blob (ENCODING=b) or an off-node URL yields nothing: this reports
// what THIS node stores, and refuses to advertise bytes it does not serve.
func vCardPhotoReference(vcard string) (path string, cid string) {
	for _, line := range strings.Split(unfoldVCard(vcard), "\n") {
		line = strings.TrimRight(line, "\r")
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(propertyName(name)), "PHOTO") {
			continue
		}
		value = strings.TrimSpace(value)
		if !strings.HasPrefix(value, "/ipfs/") {
			continue
		}
		bare := strings.TrimPrefix(value, "/ipfs/")
		if bare == "" || strings.ContainsAny(bare, "/?#") {
			continue
		}
		return value, bare
	}
	return "", ""
}

// setVCardPhotoReference replaces (or adds) the PHOTO property of a vCard so it
// points at a stored object. A card that does not exist yet is created minimally
// rather than being invented: FN is the one property vCard 4.0 requires.
func setVCardPhotoReference(vcard, cid, contentType, fullName string) string {
	photo := "PHOTO;MEDIATYPE=" + contentType + ";VALUE=uri:" + profilePhotoPath(cid)

	trimmed := strings.TrimSpace(vcard)
	if trimmed == "" {
		name := strings.TrimSpace(fullName)
		if name == "" {
			name = "unnamed"
		}
		return strings.Join([]string{
			"BEGIN:VCARD",
			"VERSION:4.0",
			"FN:" + name,
			photo,
			"END:VCARD",
		}, "\r\n")
	}

	lines := strings.Split(unfoldVCard(trimmed), "\n")
	out := make([]string, 0, len(lines)+1)
	replaced := false
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if name, _, ok := strings.Cut(line, ":"); ok &&
			strings.EqualFold(strings.TrimSpace(propertyName(name)), "PHOTO") {
			if !replaced {
				out = append(out, photo)
				replaced = true
			}
			continue
		}
		out = append(out, line)
	}
	if !replaced {
		// Insert before END:VCARD so the card stays well-formed.
		inserted := make([]string, 0, len(out)+1)
		for _, line := range out {
			if strings.EqualFold(strings.TrimSpace(line), "END:VCARD") && !replaced {
				inserted = append(inserted, photo)
				replaced = true
			}
			inserted = append(inserted, line)
		}
		out = inserted
		if !replaced {
			out = append(out, photo)
		}
	}
	return strings.Join(out, "\r\n")
}

// propertyName strips vCard parameters, leaving the property name: the part of
// "PHOTO;MEDIATYPE=image/png;VALUE=uri" before the first ';'.
func propertyName(raw string) string {
	name, _, _ := strings.Cut(raw, ";")
	return name
}

// unfoldVCard joins RFC 6350 folded continuation lines (a CRLF followed by one
// space or tab) so each property occupies exactly one line.
func unfoldVCard(vcard string) string {
	normalized := strings.ReplaceAll(vcard, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.ReplaceAll(normalized, "\n ", "")
	normalized = strings.ReplaceAll(normalized, "\n\t", "")
	return normalized
}
