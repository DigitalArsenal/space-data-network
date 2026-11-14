package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testModuleCustomer struct {
	calls int
	fail  bool
}

func (c *testModuleCustomer) PublishToTopic(context.Context, string, []byte) error { return nil }
func (c *testModuleCustomer) ModuleCustomerCatalog() (json.RawMessage, error) {
	return json.RawMessage(`{"testMode":true}`), nil
}
func (c *testModuleCustomer) TestPurchaseModule(context.Context, json.RawMessage) (json.RawMessage, error) {
	c.calls++
	if c.fail {
		return nil, errors.New("provider refused this customer")
	}
	return json.RawMessage(`{"status":"downloaded"}`), nil
}

// With test-mode delivery off, an admin's read has nothing to list (204)
// rather than a 404 the dashboard logs on every page; nothing is delivered.
func TestModuleCustomerReadWithTestModeOffListsNothing(t *testing.T) {
	c := &testModuleCustomer{}
	h := NewCoreAPIHandler("", nil, nil, c, nil, nil, nil, auth.NewHandler(nil, nil, time.Hour, "", ""), nil)
	mux := http.NewServeMux()
	h.registerModuleCustomerRoutes(mux)
	t.Setenv("SDN_STOREFRONT_DEV_PAYMENTS", "")
	r := httptest.NewRequest("GET", "/api/v1/modules/customer", nil)
	r = r.WithContext(auth.ContextWithSession(r.Context(), &auth.Session{XPub: "admin", TrustLevel: peers.Admin}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 || c.calls != 0 {
		t.Fatalf("read with test mode off: %d %q (calls %d)", w.Code, w.Body, c.calls)
	}
}

func TestModuleCustomerRouteRequiresAdminAndExplicitTestMode(t *testing.T) {
	c := &testModuleCustomer{}
	h := NewCoreAPIHandler("", nil, nil, c, nil, nil, nil, auth.NewHandler(nil, nil, time.Hour, "", ""), nil)
	mux := http.NewServeMux()
	h.registerModuleCustomerRoutes(mux)
	for _, tc := range []struct {
		mode  string
		admin bool
		want  int
	}{{"", true, 404}, {"1", false, 401}, {"1", true, 200}} {
		t.Setenv("SDN_STOREFRONT_DEV_PAYMENTS", tc.mode)
		r := httptest.NewRequest("POST", "/api/v1/modules/customer", strings.NewReader(`{}`))
		r.RemoteAddr = "203.0.113.9:1234"
		if tc.admin {
			r = r.WithContext(auth.ContextWithSession(r.Context(), &auth.Session{XPub: "admin", TrustLevel: peers.Admin}))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("mode=%q admin=%v: %d %s", tc.mode, tc.admin, w.Code, w.Body)
		}
	}
	if c.calls != 1 {
		t.Fatalf("unapproved delivery calls: %d", c.calls)
	}
	c.fail = true
	r := httptest.NewRequest("POST", "/api/v1/modules/customer", strings.NewReader(`{}`))
	r = r.WithContext(auth.ContextWithSession(r.Context(), &auth.Session{XPub: "admin", TrustLevel: peers.Admin}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"message":"provider refused this customer"`) {
		t.Fatalf("failure was hidden: %d %s", w.Code, w.Body)
	}
}
