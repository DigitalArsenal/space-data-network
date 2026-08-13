package auth

// The self-service profile contract (owner directive 2026-07-30, graph task
// sdn-account-modal-hdwallet-ui / sdn-operator-self-service-profile).
//
// These tests pin the three things that make a self-service write safe, so a
// later refactor cannot quietly widen it: it writes only the CALLER'S row, it
// writes only SELF-DESCRIBING fields, and it refuses config-managed rows
// instead of pretending to write them.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// profileFixture builds a handler with one DATABASE-backed operator at the
// given tier plus a live session for them.
func profileFixture(t *testing.T, tier peers.TrustLevel, configEntries []config.UserEntry) (*Handler, string, string) {
	t.Helper()

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), configEntries)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { _ = userStore.Close() })

	const xpub = "xpub-operator-self-service"
	if len(configEntries) == 0 {
		if err := userStore.AddUser(xpub, "Before", tier, ""); err != nil {
			t.Fatalf("AddUser: %v", err)
		}
	}

	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { _ = closer() })

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	target := xpub
	if len(configEntries) > 0 {
		target = configEntries[0].XPub
	}
	token, err := sessions.CreateSession(target, tier, "127.0.0.1", "test-agent", 24*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	return NewHandler(userStore, sessions, 24*time.Hour, "", ""), token, target
}

func patchMe(t *testing.T, h *Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/auth/me", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	h.handleMe(rec, req)
	return rec
}

// A marginal operator — the tier that previously had NO write at all — edits
// their own profile. This is the whole point of the endpoint.
func TestPatchMe_BelowAdminOperatorEditsOwnProfile(t *testing.T) {
	t.Parallel()

	h, token, xpub := profileFixture(t, peers.Marginal, nil)

	rec := patchMe(t, h, token, `{"name":"Ada Lovelace","organization":"Analytical Engines"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/auth/me = %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	var got meResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "Ada Lovelace" {
		t.Errorf("name = %q, want %q", got.Name, "Ada Lovelace")
	}
	if got.Organization != "Analytical Engines" {
		t.Errorf("organization = %q, want %q", got.Organization, "Analytical Engines")
	}
	if got.TrustLevel != peers.Marginal {
		t.Errorf("trust level = %v, want marginal — a profile write must never move a tier", got.TrustLevel)
	}
	if !got.Editable {
		t.Error("editable = false for a database row that just accepted a write")
	}

	// The store, not just the reply.
	user, err := h.userStore.GetUser(xpub)
	if err != nil || user == nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.Name != "Ada Lovelace" {
		t.Errorf("stored name = %q — the reply agreed but the row did not change", user.Name)
	}
}

// §4/§13.1: the extended public key never crosses this boundary, on any method.
func TestPatchMe_NeverReturnsXPub(t *testing.T) {
	t.Parallel()

	h, token, xpub := profileFixture(t, peers.Standard, nil)
	rec := patchMe(t, h, token, `{"name":"Grace"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), xpub) {
		t.Fatalf("PATCH /api/auth/me leaked the caller's xpub: %s", rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	getRec := httptest.NewRecorder()
	h.handleMe(getRec, req)
	if strings.Contains(getRec.Body.String(), xpub) {
		t.Fatalf("GET /api/auth/me leaked the caller's xpub: %s", getRec.Body.String())
	}
}

// Privilege cannot be self-granted: an unknown field is REFUSED, not ignored.
// A silent 200 here would be the worst outcome — the caller believes the write
// landed and the node believes it refused.
func TestPatchMe_RefusesPrivilegedFields(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`{"trust_level":"admin"}`,
		`{"name":"fine","trust_level":"admin"}`,
		`{"xpub":"xpub-somebody-else","name":"fine"}`,
		`{"signing_pubkey_hex":"00"}`,
		`{"connection_count":9999}`,
	} {
		h, token, xpub := profileFixture(t, peers.Standard, nil)
		rec := patchMe(t, h, token, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400", body, rec.Code)
		}
		user, err := h.userStore.GetUser(xpub)
		if err != nil || user == nil {
			t.Fatalf("GetUser: %v", err)
		}
		if user.TrustLevel != peers.Standard {
			t.Errorf("PATCH %s moved the tier to %v", body, user.TrustLevel)
		}
		if user.Name != "Before" {
			t.Errorf("PATCH %s applied a partial write (name = %q)", body, user.Name)
		}
	}
}

// An empty body changes nothing and says so.
func TestPatchMe_RejectsEmptyUpdate(t *testing.T) {
	t.Parallel()

	h, token, _ := profileFixture(t, peers.Standard, nil)
	if rec := patchMe(t, h, token, `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH {} = %d, want 400", rec.Code)
	}
}

// A config-managed row refuses rather than accepting a write the next GET
// would silently undo (applyConfigOverrides re-imposes the file's name).
func TestPatchMe_ConfigManagedRowRefuses(t *testing.T) {
	t.Parallel()

	h, token, _ := profileFixture(t, peers.Admin, []config.UserEntry{{
		XPub:       "xpub-from-config",
		Name:       "Configured Admin",
		TrustLevel: "admin",
	}})

	rec := patchMe(t, h, token, `{"name":"Renamed"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("PATCH on a config row = %d, want 409; body %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	getRec := httptest.NewRecorder()
	h.handleMe(getRec, req)
	var got meResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Editable {
		t.Error("editable = true for a config row the node just refused to write")
	}
	if got.Name != "Configured Admin" {
		t.Errorf("name = %q, want the config value", got.Name)
	}
}

func TestMe_RejectsOtherMethods(t *testing.T) {
	t.Parallel()

	h, token, _ := profileFixture(t, peers.Admin, nil)
	req := httptest.NewRequest(http.MethodDelete, "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	h.handleMe(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /api/auth/me = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, PATCH" {
		t.Errorf("Allow = %q, want %q", allow, "GET, PATCH")
	}
}

// An unauthenticated PATCH never reaches the store.
func TestPatchMe_RequiresSession(t *testing.T) {
	t.Parallel()

	h, _, _ := profileFixture(t, peers.Admin, nil)
	req := httptest.NewRequest(http.MethodPatch, "/api/auth/me", strings.NewReader(`{"name":"nobody"}`))
	rec := httptest.NewRecorder()
	h.handleMe(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous PATCH = %d, want 401", rec.Code)
	}
}

// The Admin registry write claimed to carry a name for months and dropped it.
func TestPutUser_ActuallyWritesTheName(t *testing.T) {
	t.Parallel()

	h, token, xpub := profileFixture(t, peers.Admin, nil)

	body := `{"xpub":"` + xpub + `","name":"Renamed By Admin","trust_level":"standard"}`
	req := httptest.NewRequest(http.MethodPut, "/api/auth/users/"+xpub, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	h.handleUserByXPub(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	user, err := h.userStore.GetUser(xpub)
	if err != nil || user == nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.Name != "Renamed By Admin" {
		t.Fatalf("stored name = %q — PUT reported success and discarded the name", user.Name)
	}
	if user.TrustLevel != peers.Standard {
		t.Errorf("trust = %v, want standard", user.TrustLevel)
	}
}

// A valid session whose xpub has no registry row is AUTHENTICATED — the root
// ceremony mints exactly such sessions (me_session_tier_test.go pins the GET).
// But there is no row to write, so the write refuses with a non-auth status:
// a 401 here would bounce a proven session into the sign-in flow.
func TestPatchMe_RowlessSessionIsNotAnAuthFailure(t *testing.T) {
	t.Parallel()

	h := newMeTierHandler(t, nil)
	token, err := h.sessions.CreateSession("xpub-rowless-patch", peers.Admin, "127.0.0.1", "test", time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rec := patchMe(t, h, token, `{"name":"Ghost"}`)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("rowless PATCH = 401 — row absence is not an auth failure; the session already proved itself")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rowless PATCH = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
}

// ---- profile photo -------------------------------------------------------

type stubPhotoStore struct {
	cid  string
	err  error
	seen []byte
	typ  string
}

func (s *stubPhotoStore) PinProfilePhoto(_ context.Context, data []byte, contentType string) (string, error) {
	s.seen = append([]byte(nil), data...)
	s.typ = contentType
	return s.cid, s.err
}

// A 1x1 PNG — real bytes, because the handler sniffs them.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89,
}

func postPhoto(t *testing.T, h *Handler, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/me/photo", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "image/png")
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	h.handleMePhoto(rec, req)
	return rec
}

func TestMePhoto_StoresObjectAndReferencesItFromTheVCard(t *testing.T) {
	t.Parallel()

	h, token, _ := profileFixture(t, peers.Marginal, nil)
	store := &stubPhotoStore{cid: "bafkreiprofilephoto"}
	h.SetProfilePhotoStore(store)

	rec := postPhoto(t, h, token, onePixelPNG)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST photo = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if store.typ != "image/png" {
		t.Errorf("stored content type = %q, want image/png", store.typ)
	}

	var got meResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.PhotoCID != "bafkreiprofilephoto" {
		t.Errorf("photo_cid = %q", got.PhotoCID)
	}
	if got.PhotoPath != "/ipfs/bafkreiprofilephoto" {
		t.Errorf("photo_path = %q, want a same-origin path", got.PhotoPath)
	}
	if strings.Contains(got.VCardData, "http://") || strings.Contains(got.VCardData, "https://") {
		t.Errorf("vCard points at an external origin: %s", got.VCardData)
	}
	if !strings.Contains(got.VCardData, "PHOTO;MEDIATYPE=image/png;VALUE=uri:/ipfs/bafkreiprofilephoto") {
		t.Errorf("vCard PHOTO not written as a served reference: %s", got.VCardData)
	}
}

// A second upload replaces the reference rather than stacking PHOTO lines.
func TestMePhoto_ReplacesExistingReference(t *testing.T) {
	t.Parallel()

	h, token, _ := profileFixture(t, peers.Standard, nil)
	h.SetProfilePhotoStore(&stubPhotoStore{cid: "bafkreifirst"})
	if rec := postPhoto(t, h, token, onePixelPNG); rec.Code != http.StatusOK {
		t.Fatalf("first upload = %d", rec.Code)
	}
	h.SetProfilePhotoStore(&stubPhotoStore{cid: "bafkreisecond"})
	rec := postPhoto(t, h, token, onePixelPNG)
	if rec.Code != http.StatusOK {
		t.Fatalf("second upload = %d", rec.Code)
	}

	var got meResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Count(got.VCardData, "PHOTO") != 1 {
		t.Errorf("expected exactly one PHOTO property, got: %s", got.VCardData)
	}
	if got.PhotoCID != "bafkreisecond" {
		t.Errorf("photo_cid = %q, want the replacement", got.PhotoCID)
	}
}

// Bytes that are not an image are refused even when the header lies.
func TestMePhoto_RefusesNonImageBytes(t *testing.T) {
	t.Parallel()

	h, token, _ := profileFixture(t, peers.Standard, nil)
	h.SetProfilePhotoStore(&stubPhotoStore{cid: "bafkreinever"})

	rec := postPhoto(t, h, token, []byte("#!/bin/sh\nrm -rf /\n"))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("script upload = %d, want 415; body %s", rec.Code, rec.Body.String())
	}
}

// No object store bound: refuse honestly, never silently drop the picture.
func TestMePhoto_FailsClosedWithoutAStore(t *testing.T) {
	t.Parallel()

	h, token, _ := profileFixture(t, peers.Standard, nil)
	rec := postPhoto(t, h, token, onePixelPNG)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("photo upload with no store = %d, want 501", rec.Code)
	}
}

// A storage failure never leaves the vCard pointing at an object that is not there.
func TestMePhoto_StorageFailureLeavesVCardUntouched(t *testing.T) {
	t.Parallel()

	h, token, xpub := profileFixture(t, peers.Standard, nil)
	h.SetProfilePhotoStore(&stubPhotoStore{err: errors.New("kubo is down")})

	if rec := postPhoto(t, h, token, onePixelPNG); rec.Code != http.StatusBadGateway {
		t.Fatalf("failed pin = %d, want 502", rec.Code)
	}
	user, err := h.userStore.GetUser(xpub)
	if err != nil || user == nil {
		t.Fatalf("GetUser: %v", err)
	}
	if strings.Contains(user.VCardData, "PHOTO") {
		t.Fatalf("vCard gained a PHOTO for an object that was never stored: %s", user.VCardData)
	}
}

func TestVCardPhotoReference_IgnoresEmbeddedAndExternal(t *testing.T) {
	t.Parallel()

	for name, card := range map[string]string{
		"embedded blob": "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:X\r\nPHOTO;ENCODING=b;MEDIATYPE=image/png:iVBORw0KGgo=\r\nEND:VCARD",
		"gateway url":   "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:X\r\nPHOTO;VALUE=uri:https://ipfs.io/ipfs/bafkrei\r\nEND:VCARD",
		"no photo":      "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:X\r\nEND:VCARD",
	} {
		if path, cid := vCardPhotoReference(card); path != "" || cid != "" {
			t.Errorf("%s: advertised %q/%q, want nothing — this node does not serve it", name, path, cid)
		}
	}
}

// A rowless session posting a photo: authenticated, but there is no vCard to
// hang the reference on. 404, never 401 — the session already proved itself.
func TestMePhoto_RowlessSessionIsNotAnAuthFailure(t *testing.T) {
	t.Parallel()

	h := newMeTierHandler(t, nil)
	h.SetProfilePhotoStore(&stubPhotoStore{cid: "bafkreirowless"})
	token, err := h.sessions.CreateSession("xpub-rowless-photo", peers.Admin, "127.0.0.1", "test", time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rec := postPhoto(t, h, token, onePixelPNG)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("rowless photo POST = %d, want 404 (no registry row to attach to, NOT an auth failure); body %s", rec.Code, rec.Body.String())
	}
}
