package sealed

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func (f *fixture) sendFrom(raw []byte, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, Route, bytes.NewReader(raw))
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) echoed(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		return ""
	}
	env, err := Open(rec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	body, err := env.Decrypt(f.replyPriv)
	if err != nil {
		t.Fatal(err)
	}
	return string(body.Body)
}

// TestSEC001RootKeyIsAdminOnlyFromTheBox: a root signer over /api/rpc is
// Admin only from loopback with no proxy hop, as root sign-in is.
func TestSEC001RootKeyIsAdminOnlyFromTheBox(t *testing.T) {
	f := newFixture(t)
	rootPub, rootPriv, _ := ed25519.GenerateKey(rand.Reader)
	f.h.RootKeys = []ed25519.PublicKey{rootPub}
	f.h.RootAccount = "xpub-node-root"

	for _, c := range []struct {
		remote string
		hdr    map[string]string
	}{
		{"203.0.113.7:443", nil},
		{"172.70.114.9:51000", map[string]string{"CF-Connecting-IP": "127.0.0.1"}},
		{"127.0.0.1:40000", map[string]string{"X-Forwarded-For": "203.0.113.7"}},
	} {
		raw := f.request(t, rootPriv, "PUT", "/api/echo", "x", f.now)
		if got := f.echoed(t, f.sendFrom(raw, c.remote, c.hdr)); strings.Contains(got, "xpub-node-root") {
			t.Fatalf("root signer admitted from %s %v: %q", c.remote, c.hdr, got)
		}
	}
	raw := f.request(t, rootPriv, "PUT", "/api/echo", "x", f.now)
	if got := f.echoed(t, f.sendFrom(raw, "127.0.0.1:40000", nil)); !strings.Contains(got, "xpub-node-root") {
		t.Fatalf("root signer from loopback not admitted: %q", got)
	}
	// An ordinary admin still works from anywhere.
	raw = f.request(t, f.admin, "PUT", "/api/echo", "x", f.now)
	if got := f.echoed(t, f.sendFrom(raw, "203.0.113.7:443", nil)); !strings.Contains(got, "xpub-admin") {
		t.Fatalf("ordinary admin refused: %q", got)
	}
}
