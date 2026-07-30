package pmm

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// Path is the well-known location of the manifest. It sits OUTSIDE the /api/
// prefix on purpose: the daemon's auth wall gates /api/ and /orbpro-key-broker/
// only, so this path is anonymous by construction rather than by allowlist.
const Path = "/.well-known/sdn/modules.pmm"

// ArtifactPrefix is the origin-relative root the manifest's ARTIFACT_PATH values
// point at. Origin-relative on purpose: the bytes must come from the same origin
// whose domain the DNS proof binds.
const ArtifactPrefix = "/modules/"

// Source produces the current manifest. Implementations cache; the handler does
// not, so a rebuild is always observable at the endpoint.
type Source interface {
	Manifest() (*Manifest, []BrowseHint, error)
}

// StaticSource serves one prebuilt manifest.
type StaticSource struct {
	mu       sync.RWMutex
	manifest *Manifest
	browse   []BrowseHint
}

// NewStaticSource returns a source holding the given manifest.
func NewStaticSource(m *Manifest, browse []BrowseHint) *StaticSource {
	return &StaticSource{manifest: m, browse: browse}
}

// Set replaces the served manifest (used after a catalog reload).
func (s *StaticSource) Set(m *Manifest, browse []BrowseHint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifest, s.browse = m, browse
}

// Manifest implements Source.
func (s *StaticSource) Manifest() (*Manifest, []BrowseHint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.manifest == nil {
		return nil, nil, ErrNoSigner
	}
	return s.manifest, s.browse, nil
}

// Handler serves the manifest at Path.
//
// ONE path, content-negotiated (SDS ruling 2026-07-30): the size-prefixed $PMM
// FlatBuffer is the default for a `.pmm` URL; JSON is returned when the client
// asks for it. Both projections carry identical field values and the same
// SIGNED_STATEMENT, so a client that verifies one has verified the other.
func Handler(src Source) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		m, browse, err := src.Manifest()
		if err != nil {
			// Fail closed. An unsigned or unbuilt manifest is never served.
			http.Error(w, "manifest unavailable", http.StatusServiceUnavailable)
			return
		}

		var body []byte
		var contentType string
		if wantsJSON(r.Header.Get("Accept")) {
			body, err = MarshalJSON(m, browse)
			contentType = "application/json"
		} else {
			body, err = MarshalBinary(m)
			contentType = "application/x-flatbuffers; schema=PMM"
		}
		if err != nil {
			http.Error(w, "manifest encode failed", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Anonymous browse from any origin: this record exists to be fetched by
		// client apps at boot, before any session, from pages we do not serve.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		// Revalidate often. EXPIRES_AT bounds trust, but a withdrawn module must
		// not sit in a cache until then.
		w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}

func wantsJSON(accept string) bool {
	a := strings.ToLower(accept)
	if strings.Contains(a, "application/json") {
		return true
	}
	// A bare */* asks for the default, which for a .pmm URL is the FlatBuffer.
	return false
}

// FreshnessCheck reports whether the manifest is still inside its own
// EXPIRES_AT. A verifier must refuse anonymous CORE loading from an expired
// manifest, so the node should rebuild before that happens.
func FreshnessCheck(m *Manifest, now time.Time) bool {
	if m == nil || m.ExpiresAt == "" {
		return false
	}
	exp, err := time.Parse("2006-01-02T15:04:05.000Z", m.ExpiresAt)
	if err != nil {
		return false
	}
	return now.UTC().Before(exp)
}
