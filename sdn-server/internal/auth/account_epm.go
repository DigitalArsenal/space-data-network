package auth

// ACCOUNT EPMs — the server half (owner directive 2026-08-28):
//
//	"for the 'profile fields' it needs to be in the UI for the EPM for this
//	 particular key, and we need to be able to store it on the server and pin
//	 it. All SDN nodes need to be able to pin all the EPMs that are created by
//	 accounts tied to them"
//
// GET  /api/auth/epm -> size-prefixed $EPM, or a JSON display projection when
//                      the caller does not request application/x-flatbuffers.
// PUT  /api/auth/epm <- size-prefixed $EPM profile proposal.
//
// The node builds and signs the record — see internal/epm/account_epm.go for
// why the account cannot sign one itself — then stores it in the EPM.fbs lane,
// pins it, and persists the account→epm_cid binding in the operator row.
//
// # The connectors-only boundary
//
// This file knows nothing about FlatSQL, Kubo, pin ledgers or CIDs beyond
// carrying a string. It declares two PORTS — AccountEPMBuilder (satisfied by
// *epm.Service) and AccountEPMStore (bound in main.go to the node's record +
// pin lanes) — and calls them. A node that has bound neither refuses honestly
// with 501 instead of pretending to have stored an identity.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
)

// maxAccountEPMProfileBytes bounds the proposed FlatBuffer record.
const maxAccountEPMProfileBytes = 6 << 20

// AccountEPMBuilder builds a signed $EPM record for one account subject.
// *epm.Service satisfies it: the node lane's builder, different subject.
type AccountEPMBuilder interface {
	BuildAccountEPM(profile *epm.Profile, subject epm.AccountSubject) ([]byte, error)
}

// AccountEPMStore is the record + pin lane. Store returns the CID the record
// was filed under; Pinned answers whether the record is still both retained by
// the store AND covered by a pin entry, which is the question the fleet-law
// reconciler asks on every pass.
type AccountEPMStore interface {
	StoreAccountEPM(ctx context.Context, sourceName string, epmBytes []byte) (string, error)
	AccountEPMPinned(ctx context.Context, cid string) (bool, error)
}

// SetAccountEPMServices binds the account-EPM ports. Leaving them unbound is a
// valid deployment: the endpoint then refuses honestly.
func (h *Handler) SetAccountEPMServices(builder AccountEPMBuilder, store AccountEPMStore) {
	h.accountEPMBuilder = builder
	h.accountEPMStore = store
}

// AccountEPMBinding is one durable account→epm_cid binding, as persisted in the
// operator row. It is what the reconciler walks.
type AccountEPMBinding struct {
	XPub             string
	SigningPubKeyHex string
	CID              string
	EPMData          []byte
	PhotoDataURL     string
	UpdatedAt        time.Time
}

// SaveAccountEPM persists the binding: the signed record bytes, the CID the
// node pinned them under, and the photo data URL (which is profile state, not
// wire state — the EPM schema has no PHOTO field).
//
// This is the durability half of the owner directive. It survives restarts
// because it is a column write in the same operator row the account signs in
// against, not a cache.
func (s *UserStore) SaveAccountEPM(xpub string, epmBytes []byte, cid, photoDataURL string) error {
	trimmed := strings.TrimSpace(xpub)
	if trimmed == "" {
		return fmt.Errorf("account EPM binding requires an xpub")
	}
	if len(epmBytes) == 0 {
		return fmt.Errorf("account EPM binding requires record bytes")
	}
	if strings.TrimSpace(cid) == "" {
		return fmt.Errorf("account EPM binding requires a CID")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(
		`UPDATE users SET epm_data = ?, epm_cid = ?, epm_updated_at = ?, epm_photo_data_url = ? WHERE xpub = ?`,
		epmBytes, strings.TrimSpace(cid), time.Now().Unix(), strings.TrimSpace(photoDataURL), trimmed,
	)
	if err != nil {
		return fmt.Errorf("failed to persist account EPM binding: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// AccountEPM reads back one account's binding. ok is false when the account has
// never published one.
func (s *UserStore) AccountEPM(xpub string) (AccountEPMBinding, bool, error) {
	trimmed := strings.TrimSpace(xpub)
	if trimmed == "" {
		return AccountEPMBinding{}, false, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var binding AccountEPMBinding
	var cid, signing, photo sql.NullString
	var updatedAt sql.NullInt64
	err := s.db.QueryRow(
		`SELECT xpub, signing_pubkey_hex, epm_cid, epm_data, epm_updated_at, epm_photo_data_url
		 FROM users WHERE xpub = ?`, trimmed,
	).Scan(&binding.XPub, &signing, &cid, &binding.EPMData, &updatedAt, &photo)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountEPMBinding{}, false, nil
	}
	if err != nil {
		return AccountEPMBinding{}, false, fmt.Errorf("read account EPM binding: %w", err)
	}
	binding.SigningPubKeyHex = signing.String
	binding.CID = strings.TrimSpace(cid.String)
	binding.PhotoDataURL = photo.String
	if updatedAt.Valid && updatedAt.Int64 > 0 {
		binding.UpdatedAt = time.Unix(updatedAt.Int64, 0).UTC()
	}
	if binding.CID == "" || len(binding.EPMData) == 0 {
		return AccountEPMBinding{}, false, nil
	}
	return binding, true, nil
}

// ListAccountEPMBindings returns every persisted account→epm_cid binding. This
// is the reconciler's work list: every EPM created by an account tied to THIS
// node.
func (s *UserStore) ListAccountEPMBindings() ([]AccountEPMBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT xpub, signing_pubkey_hex, epm_cid, epm_data, epm_updated_at, epm_photo_data_url
		 FROM users WHERE epm_cid IS NOT NULL AND epm_cid != ''`,
	)
	if err != nil {
		return nil, fmt.Errorf("list account EPM bindings: %w", err)
	}
	defer rows.Close()

	var bindings []AccountEPMBinding
	for rows.Next() {
		var binding AccountEPMBinding
		var cid, signing, photo sql.NullString
		var updatedAt sql.NullInt64
		if err := rows.Scan(&binding.XPub, &signing, &cid, &binding.EPMData, &updatedAt, &photo); err != nil {
			return nil, fmt.Errorf("scan account EPM binding: %w", err)
		}
		binding.SigningPubKeyHex = signing.String
		binding.CID = strings.TrimSpace(cid.String)
		binding.PhotoDataURL = photo.String
		if updatedAt.Valid && updatedAt.Int64 > 0 {
			binding.UpdatedAt = time.Unix(updatedAt.Int64, 0).UTC()
		}
		if binding.CID == "" || len(binding.EPMData) == 0 {
			continue
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

// handleAccountEPM serves GET and PUT /api/auth/epm. Self-scoped by
// construction, exactly like PATCH /api/auth/me and POST /api/auth/me/photo:
// the row read and written is session.XPub, and the request body carries no
// identifier to tamper with. An account can only ever publish ITS OWN identity
// because there is no way to name another one.
func (h *Handler) handleAccountEPM(w http.ResponseWriter, r *http.Request) {
	session, err := h.sessionFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "not authenticated"})
		return
	}
	session = h.maybeRefreshSessionCookie(w, r, session)

	switch r.Method {
	case http.MethodGet:
		h.serveAccountEPM(w, r, session)
	case http.MethodPut:
		h.storeAccountEPM(w, r, session)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Code: "method_not_allowed", Message: "GET or PUT only"})
	}
}

func (h *Handler) serveAccountEPM(w http.ResponseWriter, r *http.Request, session *Session) {
	binding, ok, err := h.userStore.AccountEPM(session.XPub)
	if err != nil {
		log.Warnf("account EPM read failed for %s: %v", XPubFingerprint(session.XPub), err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: "the stored identity could not be read"})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Code: "not_found", Message: "this account has not published an identity yet"})
		return
	}
	if strings.Contains(r.Header.Get("Accept"), epm.EPMContentType) {
		w.Header().Set("Content-Type", epm.EPMContentType)
		w.Header().Set("X-SDN-EPM-CID", binding.CID)
		w.Header().Set("Last-Modified", binding.UpdatedAt.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(binding.EPMData)
		return
	}
	projection := epm.AccountEPMJSON(binding.EPMData, binding.PhotoDataURL)
	if projection == nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: "the stored identity is not a readable EPM record"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"cid":        binding.CID,
		"epm":        projection,
		"updated_at": binding.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) storeAccountEPM(w http.ResponseWriter, r *http.Request, session *Session) {
	builder, store := h.accountEPMBuilder, h.accountEPMStore
	if builder == nil || store == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{
			Code:    "account_epm_unavailable",
			Message: "this node has no identity record lane bound, so it cannot store an account identity",
		})
		return
	}

	// A missing row is not an auth failure — the session already proved itself
	// — but an account EPM is written INTO the row and is ABOUT the key bound
	// to it. Without a row there is neither a subject nor a place to keep it.
	user, err := h.userStore.GetUser(session.XPub)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Code:    "no_account",
			Message: "this session has no account row on this node",
		})
		return
	}
	if strings.TrimSpace(user.SigningPubKeyHex) == "" {
		writeJSON(w, http.StatusForbidden, errorResponse{
			Code:    "no_signing_key",
			Message: "this account has no bound signing key, so it has no identity to publish",
		})
		return
	}
	mediaType, _, contentTypeErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentTypeErr != nil || mediaType != epm.EPMContentType {
		writeJSON(w, http.StatusUnsupportedMediaType, errorResponse{
			Code:    "flatbuffer_required",
			Message: "JSON profiles are not accepted. Send a size-prefixed EPM FlatBuffer.",
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxAccountEPMProfileBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: "could not read the request body"})
		return
	}
	if len(body) > maxAccountEPMProfileBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{
			Code:    "profile_too_large",
			Message: fmt.Sprintf("a profile must be %d bytes or smaller", maxAccountEPMProfileBytes),
		})
		return
	}
	profile, err := epm.DecodeProfileEPM(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_epm", Message: err.Error()})
		return
	}

	epmBytes, err := builder.BuildAccountEPM(profile, epm.AccountSubject{SigningPubKeyHex: user.SigningPubKeyHex})
	if err != nil {
		if epm.IsKeyPathValidationError(err) || errors.Is(err, epm.ErrNoAccountSigningKey) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: err.Error()})
			return
		}
		log.Warnf("account EPM build failed for %s: %v", XPubFingerprint(session.XPub), err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: "the identity record could not be built"})
		return
	}
	// The node signed it; verify its own output before anything is stored. A
	// record that cannot be verified is never filed under an account's name.
	if err := epm.VerifyEPMSignature(epmBytes); err != nil {
		log.Warnf("account EPM self-verification failed for %s: %v", XPubFingerprint(session.XPub), err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: "the identity record did not verify"})
		return
	}

	cid, err := store.StoreAccountEPM(r.Context(), AccountEPMSourceName(user.SigningPubKeyHex), epmBytes)
	if err != nil {
		log.Warnf("account EPM store failed for %s: %v", XPubFingerprint(session.XPub), err)
		writeJSON(w, http.StatusBadGateway, errorResponse{Code: "store_failed", Message: "the node could not store the identity record"})
		return
	}
	// PHOTO is not present in the current canonical EPM schema. Keep any
	// previously stored photo rather than erasing it on a FlatBuffer update.
	photoDataURL := ""
	if current, ok, readErr := h.userStore.AccountEPM(session.XPub); readErr == nil && ok {
		photoDataURL = current.PhotoDataURL
	}
	if err := h.userStore.SaveAccountEPM(session.XPub, epmBytes, cid, photoDataURL); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "server_error", Message: err.Error()})
		return
	}
	w.Header().Set("Content-Type", epm.EPMContentType)
	w.Header().Set("X-SDN-EPM-CID", cid)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(epmBytes)
}

// AccountEPMSourceName is the SourceName every account EPM is tagged with: a
// fingerprint of the account's signing key, so the lane is queryable per
// account without putting the raw key in a source tag.
func AccountEPMSourceName(signingPubKeyHex string) string {
	trimmed := strings.ToLower(strings.TrimSpace(signingPubKeyHex))
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 16 {
		return trimmed
	}
	return trimmed[:16]
}
