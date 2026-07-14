package credstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultSpaceTrackLoginURL matches internal/ingest's defaultSpaceTrackLoginURL.
const DefaultSpaceTrackLoginURL = "https://www.space-track.org/ajaxauth/login"

// probeCooldown is the minimum interval between credential probes.
//
// The ingest lane has its own rate limiter, but the operator's "Save & verify"
// button is user-repeatable in a way a scheduled fetch is not: a frustrated
// operator clicking Save ten times must not turn into ten Space-Track logins.
// This cooldown is the fetch policy's "never retry-hammer" rule made mechanical.
// It is deliberately generous — verification is a rare, deliberate act.
const probeCooldown = 60 * time.Second

// ErrProbeCooldown is returned when a probe is attempted inside the cooldown.
var ErrProbeCooldown = errors.New("verification was attempted too recently; the credential was saved but not re-verified")

// SpaceTrackVerifier performs a single authenticated login against Space-Track
// to confirm a credential works.
//
// FETCH POLICY
//
//	one lightweight login, no data pull, no retry, cooldown-limited.
//
// It hits only /ajaxauth/login: no query, no catalog, nothing downloaded.
type SpaceTrackVerifier struct {
	LoginURL string
	Client   *http.Client

	mu        sync.Mutex
	lastProbe time.Time
}

// NewSpaceTrackVerifier builds a verifier against the real Space-Track login.
func NewSpaceTrackVerifier() *SpaceTrackVerifier {
	return &SpaceTrackVerifier{
		LoginURL: DefaultSpaceTrackLoginURL,
		Client:   &http.Client{Timeout: 20 * time.Second},
	}
}

// Verify posts the credential to Space-Track's login endpoint exactly once.
//
// Space-Track answers a FAILED login with HTTP 200 and a body such as
// {"Login":"Failed"} — so a status check alone would happily report a wrong
// password as valid. Success therefore requires BOTH:
//
//   - HTTP 200, and
//   - a session cookie actually set by the server, and
//   - a body that does not carry a login-failure marker.
//
// The credential is never logged and never appears in any returned error.
func (v *SpaceTrackVerifier) Verify(ctx context.Context, username, secret string) error {
	if strings.TrimSpace(username) == "" || secret == "" {
		return errors.New("username and secret are required")
	}

	v.mu.Lock()
	if !v.lastProbe.IsZero() && time.Since(v.lastProbe) < probeCooldown {
		v.mu.Unlock()
		return ErrProbeCooldown
	}
	v.lastProbe = time.Now()
	v.mu.Unlock()

	loginURL := v.LoginURL
	if loginURL == "" {
		loginURL = DefaultSpaceTrackLoginURL
	}
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	// A private cookie jar per probe: the session this login mints is discarded
	// immediately. Nothing is cached, nothing is reused, nothing is fetched.
	jar, err := cookiejar.New(nil)
	if err != nil {
		return errors.New("verification unavailable")
	}
	probeClient := &http.Client{
		Jar:       jar,
		Timeout:   client.Timeout,
		Transport: client.Transport,
	}

	form := url.Values{}
	form.Set("identity", username)
	form.Set("password", secret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("verification unavailable")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := probeClient.Do(req)
	if err != nil {
		// NOTE: err may embed the request URL but never the form body.
		return fmt.Errorf("could not reach Space-Track: %w", redactURLError(err))
	}
	defer resp.Body.Close()

	// Read a bounded prefix: enough to spot the failure marker, never enough to
	// constitute a data pull.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errors.New("Space-Track rejected the credential")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return errors.New("Space-Track rate-limited the verification attempt; the credential was saved but not verified")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Space-Track returned HTTP %d", resp.StatusCode)
	}

	// HTTP 200 does NOT mean the login succeeded.
	if looksLikeLoginFailure(string(body)) {
		return errors.New("Space-Track rejected the credential")
	}
	if u, perr := url.Parse(loginURL); perr == nil && len(jar.Cookies(u)) == 0 {
		// 200, no failure marker, but no session cookie: treat as failure
		// rather than optimistically reporting success.
		return errors.New("Space-Track did not establish a session for this credential")
	}
	return nil
}

// looksLikeLoginFailure spots Space-Track's HTTP-200 failure bodies.
func looksLikeLoginFailure(body string) bool {
	b := strings.ToLower(body)
	for _, marker := range []string{
		`"login":"failed"`,
		`"login": "failed"`,
		"login failed",
		"failed to login",
		"invalid credentials",
		"unauthorized",
	} {
		if strings.Contains(b, marker) {
			return true
		}
	}
	return false
}

// redactURLError strips any query/userinfo from a transport error before it is
// surfaced to an operator, so a URL-embedded credential can never ride out in an
// error string. (We never build such a URL, but errors are the classic leak.)
func redactURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("%s %s: %v", uerr.Op, redactURLString(uerr.URL), uerr.Err)
	}
	return err
}

func redactURLString(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "space-track"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
