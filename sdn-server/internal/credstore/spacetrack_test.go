package credstore

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const probeSecret = "PROBE-PLAINTEXT-CANARY-5521"

func newProbeVerifier(srv *httptest.Server) *SpaceTrackVerifier {
	return &SpaceTrackVerifier{
		LoginURL: srv.URL + "/ajaxauth/login",
		Client:   srv.Client(),
	}
}

// A successful login: HTTP 200, a session cookie, no failure marker.
func TestVerifySuccess(t *testing.T) {
	var gotIdentity, gotPassword, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotPath = r.URL.Path
		gotIdentity = r.Form.Get("identity")
		gotPassword = r.Form.Get("password")
		http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "session-token"})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`""`))
	}))
	defer srv.Close()

	v := newProbeVerifier(srv)
	if err := v.Verify(context.Background(), "operator@example.com", probeSecret); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// The probe hits ONLY the login endpoint — no data pull.
	if gotPath != "/ajaxauth/login" {
		t.Errorf("probe hit %q, want /ajaxauth/login", gotPath)
	}
	// It uses Space-Track's own field names.
	if gotIdentity != "operator@example.com" || gotPassword != probeSecret {
		t.Error("the probe did not submit identity/password as Space-Track expects")
	}
}

// THE TRAP: Space-Track answers a FAILED login with HTTP 200 and a failure body.
// A status-code-only check would report a wrong password as "verified".
func TestVerifyRejectsHTTP200LoginFailure(t *testing.T) {
	for name, body := range map[string]string{
		"json marker":   `{"Login":"Failed"}`,
		"spaced marker": `{"Login": "Failed"}`,
		"prose marker":  `Failed to login`,
		"invalid creds": `invalid credentials`,
		"unauthorized":  `Unauthorized`,
		"login failed":  `login failed`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 200 OK, and even a cookie — but the body says it failed.
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "x"})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))

		v := newProbeVerifier(srv)
		err := v.Verify(context.Background(), "operator@example.com", "wrong-password")
		srv.Close()

		if err == nil {
			t.Errorf("%s: SECURITY: a failed login (HTTP 200 + %q) was reported as VERIFIED", name, body)
			continue
		}
		if strings.Contains(err.Error(), "wrong-password") {
			t.Errorf("%s: SECURITY: the error leaked the submitted secret", name)
		}
	}
}

// HTTP 200 with no session cookie is not a successful login either.
func TestVerifyRequiresSessionCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`""`))
	}))
	defer srv.Close()

	v := newProbeVerifier(srv)
	if err := v.Verify(context.Background(), "operator@example.com", probeSecret); err == nil {
		t.Fatal("SECURITY: a login that established no session was reported as verified")
	}
}

func TestVerifyRejectsErrorStatuses(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		v := newProbeVerifier(srv)
		err := v.Verify(context.Background(), "operator@example.com", probeSecret)
		srv.Close()

		if err == nil {
			t.Errorf("HTTP %d was reported as verified", code)
			continue
		}
		if strings.Contains(err.Error(), probeSecret) {
			t.Errorf("HTTP %d: SECURITY: the error leaked the secret", code)
		}
	}
}

// FETCH POLICY: exactly one request per probe, and a cooldown that makes a
// repeatedly-clicked "Save & verify" button incapable of hammering Space-Track.
func TestProbeIsSingleRequestAndCooldownLimited(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "s"})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	v := newProbeVerifier(srv)
	if err := v.Verify(context.Background(), "operator@example.com", probeSecret); err != nil {
		t.Fatalf("first probe: %v", err)
	}
	if requests != 1 {
		t.Errorf("a single probe made %d requests, want exactly 1 (no retry, no data pull)", requests)
	}

	// An immediate second probe must be refused by the cooldown, NOT sent.
	err := v.Verify(context.Background(), "operator@example.com", probeSecret)
	if !errors.Is(err, ErrProbeCooldown) {
		t.Errorf("second probe: err = %v, want ErrProbeCooldown", err)
	}
	if requests != 1 {
		t.Errorf("the cooldown let a second request through: %d requests total", requests)
	}

	// Once the cooldown lapses, a probe is allowed again.
	v.mu.Lock()
	v.lastProbe = time.Now().Add(-2 * probeCooldown)
	v.mu.Unlock()
	if err := v.Verify(context.Background(), "operator@example.com", probeSecret); err != nil {
		t.Fatalf("probe after cooldown: %v", err)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
}

func TestVerifyRequiresCredentials(t *testing.T) {
	v := NewSpaceTrackVerifier()
	if err := v.Verify(context.Background(), "", "secret"); err == nil {
		t.Error("empty username should be rejected without any network call")
	}
	if err := v.Verify(context.Background(), "user", ""); err == nil {
		t.Error("empty secret should be rejected without any network call")
	}
}

// A transport error must not carry the credential out in its message.
func TestTransportErrorDoesNotLeakSecret(t *testing.T) {
	v := &SpaceTrackVerifier{
		LoginURL: "http://127.0.0.1:1/ajaxauth/login", // nothing listening
		Client:   &http.Client{Timeout: 2 * time.Second},
	}
	err := v.Verify(context.Background(), "operator@example.com", probeSecret)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), probeSecret) {
		t.Fatal("SECURITY: the transport error leaked the secret")
	}
}

func TestLooksLikeLoginFailure(t *testing.T) {
	for body, want := range map[string]bool{
		`{"Login":"Failed"}`:  true,
		`{"Login": "Failed"}`: true,
		`LOGIN FAILED`:        true,
		`""`:                  false,
		`[]`:                  false,
		`{"session":"ok"}`:    false,
	} {
		if got := looksLikeLoginFailure(body); got != want {
			t.Errorf("looksLikeLoginFailure(%q) = %v, want %v", body, got, want)
		}
	}
}
