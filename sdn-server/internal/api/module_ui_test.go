package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testModuleApps struct{ calls int }

func (*testModuleApps) PublishToTopic(context.Context, string, []byte) error { return nil }
func (a *testModuleApps) ModuleApplication(id string) ([]byte, error) {
	a.calls++
	if id != "test.editor" {
		return nil, errors.New("not installed")
	}
	return []byte("APP"), nil
}
func (a *testModuleApps) ModuleApplicationArtifact(id string) ([]byte, error) {
	return a.ModuleApplication(id)
}
func (a *testModuleApps) ModuleApplicationPage(id string) ([]byte, error) {
	return a.ModuleApplication(id)
}

func TestModuleApplicationRoutesRequireAdminAndPinPage(t *testing.T) {
	apps := &testModuleApps{}
	handler := NewCoreAPIHandler("", nil, nil, apps, nil, nil, nil, auth.NewHandler(nil, nil, time.Hour, "", ""), nil)
	mux := http.NewServeMux()
	// Register the real runtime subtree first: a module-ID wildcard at this
	// level conflicts with it and prevents the whole daemon from starting.
	mux.HandleFunc("/api/v1/modules/runtime", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("/api/v1/modules/runtime/", func(http.ResponseWriter, *http.Request) {})
	handler.RegisterRoutes(mux)
	hash := sha256.Sum256([]byte("APP"))
	query := "?sha256=" + hex.EncodeToString(hash[:])
	for _, tc := range []struct {
		path, method string
		admin        bool
		want         int
	}{
		{"app", "GET", false, 401}, {"artifact", "GET", false, 401}, {"app/page" + query, "GET", false, 401},
		{"app", "POST", true, 405}, {"app", "GET", true, 200}, {"artifact", "GET", true, 200},
		{"app/page", "GET", true, 409}, {"app/page?sha256=bad", "GET", true, 409}, {"app/page" + query, "GET", true, 200},
	} {
		r := httptest.NewRequest(tc.method, "/api/v1/modules/apps/test.editor/"+tc.path, nil)
		r.RemoteAddr = "203.0.113.9:1234"
		if tc.admin {
			r = r.WithContext(auth.ContextWithSession(r.Context(), &auth.Session{XPub: "admin", TrustLevel: peers.Admin}))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s admin=%v: %d %s", tc.method, tc.path, tc.admin, w.Code, w.Body)
		}
		if tc.want == 200 && strings.HasPrefix(tc.path, "app/page") {
			for _, rule := range []string{"connect-src 'none'", "sandbox allow-scripts allow-forms allow-downloads", "frame-ancestors 'self'"} {
				if !strings.Contains(w.Header().Get("Content-Security-Policy"), rule) {
					t.Errorf("missing CSP %s", rule)
				}
			}
			if w.Header().Get("X-Frame-Options") != "SAMEORIGIN" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("page security/cache headers missing")
			}
		}
	}
	if apps.calls != 5 {
		t.Fatalf("unauthorized module reads: %d", apps.calls)
	}
}
