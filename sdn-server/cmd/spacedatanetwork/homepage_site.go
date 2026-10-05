package main

// THE NODE'S PUBLIC HOMEPAGE (owner 2026-10-05).
//
// Every detected SDN node is registered under spacedatanetwork.org as
// https://<peer-label>.spacedatanetwork.org. A request arriving under that name
// gets the node's homepage at "/", its public APIs answer behind it, and the
// page always carries the node's EPM card. Every other address (127.0.0.1, an
// operator's own domain) keeps the dashboard at "/"; /home/ shows the
// homepage on any address.
//
// The page is one embedded single-file app (sdn-js/dashboard/build-dashboard.mjs
// builds it as the `home` app). What the operator puts on it is a small JSON
// document kept beside the node's other operator settings and served at
// /api/v1/homepage; the card is not part of the document, so no document can
// remove it.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multibase"
)

//go:embed embedded/homepage.html
var homepageHTML []byte

// homepageCSPRaw is the page's policy, emitted by build-dashboard.mjs next to
// the html so its inline-script hash always matches the shipped bytes.
//
//go:embed embedded/homepage.csp
var homepageCSPRaw string

// publicSiteZone is the zone the network registers node names under.
const publicSiteZone = "spacedatanetwork.org"

// nodeSiteURL is a node's public address: its full peer ID as a lowercase
// base36 CIDv1 libp2p-key, one DNS label (the label docs/peer-id-cdn.md and the
// cloudflare-registrar module derive).
func nodeSiteURL(id peer.ID) string {
	if id == "" {
		return ""
	}
	label, err := peer.ToCid(id).StringOfBase(multibase.Base36)
	if err != nil {
		return ""
	}
	return "https://" + label + "." + publicSiteZone
}

// isNodeSiteRequest reports whether a request arrived under a node name in the
// zone: exactly one label below spacedatanetwork.org.
func isNodeSiteRequest(r *http.Request) bool {
	host := strings.ToLower(strings.TrimSpace(r.Host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	label, ok := strings.CutSuffix(strings.TrimSuffix(host, "."), "."+publicSiteZone)
	return ok && label != "" && !strings.Contains(label, ".")
}

// isHomepagePath reports the homepage's own paths on any address.
func isHomepagePath(path string) bool {
	return path == "/home" || path == "/home/" || path == "/home/index.html"
}

// serveHomepage writes the embedded homepage under its build-generated CSP.
func serveHomepage(w http.ResponseWriter, r *http.Request) {
	if len(homepageHTML) == 0 || strings.TrimSpace(homepageCSPRaw) == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", strings.TrimSpace(homepageCSPRaw))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(homepageHTML)
	}
}

// The homepage document. The same rules hold in the dashboard's editor
// (spaceaware-ui lib/homepage/document.js); this copy is the one that counts.
const (
	homepageFile        = "homepage.json"
	homepageMaxBytes    = 64 << 10
	homepageMaxBlocks   = 24
	homepageMaxTitle    = 120
	homepageMaxSubtitle = 240
	homepageMaxBody     = 4000
	homepageMaxLinks    = 12
	homepageMaxLabel    = 80
	homepageMaxURL      = 400
)

type homepageLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type homepageBlock struct {
	Type     string         `json:"type"`
	Title    string         `json:"title,omitempty"`
	Subtitle string         `json:"subtitle,omitempty"`
	Body     string         `json:"body,omitempty"`
	Items    []homepageLink `json:"items,omitempty"`
}

type homepageDocument struct {
	Version int             `json:"version"`
	Blocks  []homepageBlock `json:"blocks"`
}

// defaultHomepage is what a node with no saved document shows: its name
// (the page falls back to the card's name), then its card.
func defaultHomepage() homepageDocument {
	return homepageDocument{Version: 1, Blocks: []homepageBlock{{Type: "heading"}, {Type: "card"}}}
}

func clipRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// safeHomepageURL admits http(s) and mailto only, so a link can never run
// script on the page.
func safeHomepageURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "https", "http":
		return u.Host != ""
	case "mailto":
		return u.Opaque != "" || u.Path != ""
	}
	return false
}

// normalizeHomepage keeps known blocks within their limits, links with a
// label and a safe address, and exactly one card: a document without one gets
// it at the end, and the card survives the block limit.
func normalizeHomepage(doc homepageDocument) homepageDocument {
	out := homepageDocument{Version: 1}
	card := false
	for _, b := range doc.Blocks {
		switch b.Type {
		case "heading":
			out.Blocks = append(out.Blocks, homepageBlock{Type: b.Type, Title: clipRunes(b.Title, homepageMaxTitle), Subtitle: clipRunes(b.Subtitle, homepageMaxSubtitle)})
		case "text":
			out.Blocks = append(out.Blocks, homepageBlock{Type: b.Type, Title: clipRunes(b.Title, homepageMaxTitle), Body: clipRunes(b.Body, homepageMaxBody)})
		case "links":
			block := homepageBlock{Type: b.Type, Title: clipRunes(b.Title, homepageMaxTitle)}
			for _, l := range b.Items {
				if len(block.Items) == homepageMaxLinks {
					break
				}
				label := clipRunes(strings.TrimSpace(l.Label), homepageMaxLabel)
				link := strings.TrimSpace(l.URL)
				if label == "" || utf8.RuneCountInString(link) > homepageMaxURL || !safeHomepageURL(link) {
					continue
				}
				block.Items = append(block.Items, homepageLink{Label: label, URL: link})
			}
			out.Blocks = append(out.Blocks, block)
		case "card":
			if !card {
				card = true
				out.Blocks = append(out.Blocks, homepageBlock{Type: b.Type})
			}
		}
	}
	if !card {
		out.Blocks = append(out.Blocks, homepageBlock{Type: "card"})
	}
	for len(out.Blocks) > homepageMaxBlocks {
		for i := len(out.Blocks) - 1; i >= 0; i-- {
			if out.Blocks[i].Type != "card" {
				out.Blocks = append(out.Blocks[:i], out.Blocks[i+1:]...)
				break
			}
		}
	}
	return out
}

// homepageStore keeps the document as homepage.json in the node's storage
// directory, beside trust-settings.json.
type homepageStore struct {
	mu   sync.Mutex
	path string
}

func newHomepageStore(dir string) *homepageStore {
	return &homepageStore{path: filepath.Join(dir, homepageFile)}
}

// load returns the saved document, or the default when there is none.
func (s *homepageStore) load() (homepageDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultHomepage(), nil
	}
	if err != nil {
		return homepageDocument{}, err
	}
	var doc homepageDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return homepageDocument{}, fmt.Errorf("%s: %w", s.path, err)
	}
	return normalizeHomepage(doc), nil
}

// save normalizes and writes the document atomically.
func (s *homepageStore) save(doc homepageDocument) (homepageDocument, error) {
	doc = normalizeHomepage(doc)
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return homepageDocument{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp, err := os.CreateTemp(filepath.Dir(s.path), homepageFile+".*")
	if err != nil {
		return homepageDocument{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return homepageDocument{}, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return homepageDocument{}, err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return homepageDocument{}, err
	}
	return doc, nil
}

// handleHomepageDocument serves GET (anyone) and PUT (the caller gates writes)
// on /api/v1/homepage: {"document": …, "public_url": "https://<label>.spacedatanetwork.org"}.
func handleHomepageDocument(store *homepageStore, publicURL func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var doc homepageDocument
		var err error
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			doc, err = store.load()
		case http.MethodPut:
			body, readErr := io.ReadAll(io.LimitReader(r.Body, homepageMaxBytes+1))
			if readErr != nil {
				http.Error(w, "read body", http.StatusBadRequest)
				return
			}
			if len(body) > homepageMaxBytes {
				http.Error(w, "homepage document too large", http.StatusRequestEntityTooLarge)
				return
			}
			var in homepageDocument
			if err := json.Unmarshal(body, &in); err != nil {
				http.Error(w, "homepage document is not valid JSON", http.StatusBadRequest)
				return
			}
			doc, err = store.save(in)
		default:
			w.Header().Set("Allow", "GET, HEAD, PUT")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			log.Warnf("homepage document: %v", err)
			http.Error(w, "homepage document unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"document": doc, "public_url": publicURL()})
	}
}
