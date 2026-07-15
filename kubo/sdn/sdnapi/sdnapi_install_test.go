package sdnapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ipfs/kubo/sdn/sdnapi"
)

// fakeInstaller is a ModuleInstaller test double: it records the last call and
// returns a canned view or one of the sdnapi sentinel errors, so the HTTP route
// can be exercised for every status-code path without a live runtime.
type fakeInstaller struct {
	gotHash   string
	gotGrants []sdnapi.CapabilityGrant
	view      sdnapi.InstalledModuleView
	err       error
}

func (f *fakeInstaller) AdminInstall(_ context.Context, contentHash string, grants []sdnapi.CapabilityGrant) (sdnapi.InstalledModuleView, error) {
	f.gotHash = contentHash
	f.gotGrants = grants
	return f.view, f.err
}

const validHash = "b6efbb2519790000000000000000000000000000000000000000000000000000"

func installHandler(inst sdnapi.ModuleInstaller) http.Handler {
	return sdnapi.NewHandler(sdnapi.Deps{
		Installer: func() sdnapi.ModuleInstaller { return inst },
	})
}

func postInstall(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sdn/v1/admin/modules/install", strings.NewReader(body)))
	return rec
}

// TestAdminInstallRouteSuccess: a valid content hash + capability grant returns
// 200 and the installed-module view, and the route forwards the grants.
func TestAdminInstallRouteSuccess(t *testing.T) {
	fake := &fakeInstaller{view: sdnapi.InstalledModuleView{
		ID: "com.orbpro.celestrak-supgp", ContentHash: validHash,
		Name: "CelesTrak SupGP", Enabled: true, Timers: []string{"pull"},
	}}
	h := installHandler(fake)

	body := fmt.Sprintf(`{"content_hash":%q,"approved_by":"operator","capabilities":["http","storage_ingest","wallet_sign","pubsub"]}`, validHash)
	rec := postInstall(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got sdnapi.InstalledModuleView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if got.ID != "com.orbpro.celestrak-supgp" || len(got.Timers) != 1 || got.Timers[0] != "pull" {
		t.Fatalf("unexpected view: %+v", got)
	}
	if fake.gotHash != validHash {
		t.Fatalf("forwarded hash = %q, want %q", fake.gotHash, validHash)
	}
	if len(fake.gotGrants) != 4 || fake.gotGrants[0].Capability != "http" || fake.gotGrants[0].ApprovedBy != "operator" {
		t.Fatalf("forwarded grants unexpected: %+v", fake.gotGrants)
	}
}

// TestAdminInstallRouteDenied: a fail-closed capability denial maps to 403.
func TestAdminInstallRouteDenied(t *testing.T) {
	fake := &fakeInstaller{err: fmt.Errorf("%w: needs http", sdnapi.ErrInstallDenied)}
	rec := postInstall(t, installHandler(fake), fmt.Sprintf(`{"content_hash":%q,"capabilities":["storage_ingest"]}`, validHash))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminInstallRouteNotFound: a well-formed hash with no resident block maps
// to 404.
func TestAdminInstallRouteNotFound(t *testing.T) {
	fake := &fakeInstaller{err: fmt.Errorf("%w: no block", sdnapi.ErrModuleNotFound)}
	rec := postInstall(t, installHandler(fake), fmt.Sprintf(`{"content_hash":%q}`, validHash))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminInstallRouteMalformedHash: a bad content hash is a 400 and never
// reaches the installer.
func TestAdminInstallRouteMalformedHash(t *testing.T) {
	fake := &fakeInstaller{}
	for _, body := range []string{
		`{"content_hash":"short"}`,
		`{"content_hash":"zzzz` + strings.Repeat("z", 60) + `"}`,
		`not json`,
		`[1,2,3]`,
	} {
		rec := postInstall(t, installHandler(fake), body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, want 400", body, rec.Code)
		}
	}
	if fake.gotHash != "" {
		t.Fatalf("malformed request must not reach the installer, got hash %q", fake.gotHash)
	}
}

// TestAdminInstallRouteUnavailable: with no Installer dep the route is 503.
func TestAdminInstallRouteUnavailable(t *testing.T) {
	h := sdnapi.NewHandler(sdnapi.Deps{}) // no Installer
	rec := postInstall(t, h, fmt.Sprintf(`{"content_hash":%q}`, validHash))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminInstallRouteMethodGuard: only POST is accepted on the install route.
func TestAdminInstallRouteMethodGuard(t *testing.T) {
	h := installHandler(&fakeInstaller{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sdn/v1/admin/modules/install", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on install route status = %d, want 405", rec.Code)
	}
}
