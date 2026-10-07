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
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multibase"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
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

// siteLabel is the node name a request arrived under: exactly one label below
// spacedatanetwork.org.
func siteLabel(r *http.Request) (string, bool) {
	host := strings.ToLower(strings.TrimSpace(r.Host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	label, ok := strings.CutSuffix(strings.TrimSuffix(host, "."), "."+publicSiteZone)
	if !ok || label == "" || strings.Contains(label, ".") {
		return "", false
	}
	return label, true
}

// isNodeSiteRequest reports whether a request arrived under a node name.
func isNodeSiteRequest(r *http.Request) bool {
	_, ok := siteLabel(r)
	return ok
}

// isHomepagePath reports the homepage's own paths on any address.
func isHomepagePath(path string) bool {
	return path == "/home" || path == "/home/" || path == "/home/index.html"
}

// serveHomepage writes the served homepage (servedUI) under its
// build-generated CSP.
func serveHomepage(w http.ResponseWriter, r *http.Request) {
	ui := servedUI.current()
	if len(ui.homepage) == 0 || ui.homepageCSP == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-SDN-UI", ui.label)
	w.Header().Set("Content-Security-Policy", ui.homepageCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	// no-transform keeps a CDN in front of the gateway from rewriting the
	// page (Cloudflare would inject its analytics script, which the policy
	// above refuses).
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(ui.homepage)
	}
}

// The homepage document. The same rules hold in the dashboard's editor
// (spaceaware-ui lib/homepage/document.js); this copy is the one that counts.
const (
	homepageFile        = "homepage.json"
	homepageVersion     = 2
	homepageMaxBytes    = 64 << 10
	homepageMaxBlocks   = 24
	homepageMaxTitle    = 120
	homepageMaxSubtitle = 240
	homepageMaxBody     = 4000
	homepageMaxLinks    = 12
	homepageMaxLabel    = 80
	homepageMaxURL      = 400
	homepageMaxModels   = 12
	homepageMaxCaption  = 240
	homepageMaxCID      = 100
)

// homepageItem is one entry of a links block (label, url) or of a models
// block (name, cid, source_url, caption); each block keeps only its own
// fields.
type homepageItem struct {
	Label     string `json:"label,omitempty"`
	URL       string `json:"url,omitempty"`
	Name      string `json:"name,omitempty"`
	CID       string `json:"cid,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
	Caption   string `json:"caption,omitempty"`
}

type homepageBlock struct {
	Type     string         `json:"type"`
	Title    string         `json:"title,omitempty"`
	Subtitle string         `json:"subtitle,omitempty"`
	Body     string         `json:"body,omitempty"`
	Items    []homepageItem `json:"items,omitempty"`
}

type homepageDocument struct {
	Version int             `json:"version"`
	Blocks  []homepageBlock `json:"blocks"`
}

// defaultHomepage is what a node with no saved document shows: its name
// (the page falls back to the card's name), its card, its address book.
func defaultHomepage() homepageDocument {
	return homepageDocument{Version: homepageVersion, Blocks: []homepageBlock{{Type: "heading"}, {Type: "card"}, {Type: "contacts"}}}
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

// safeSourceURL admits the http(s) address a model was published at.
func safeSourceURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// homepageModelCID is a model's CID in canonical form, or false when the text
// is not one CID.
func homepageModelCID(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if text == "" || len(text) > homepageMaxCID {
		return "", false
	}
	c, err := cid.Decode(text)
	if err != nil {
		return "", false
	}
	return c.String(), true
}

// normalizeHomepage keeps known blocks within their limits, links with a
// label and a safe address, models with a CID, at most one address book, and
// exactly one card: a document without one gets it at the end, and the card
// survives the block limit.
func normalizeHomepage(doc homepageDocument) homepageDocument {
	out := homepageDocument{Version: homepageVersion}
	card := false
	contacts := false
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
				block.Items = append(block.Items, homepageItem{Label: label, URL: link})
			}
			out.Blocks = append(out.Blocks, block)
		case "models":
			block := homepageBlock{Type: b.Type, Title: clipRunes(b.Title, homepageMaxTitle)}
			seen := map[string]bool{}
			for _, m := range b.Items {
				if len(block.Items) == homepageMaxModels {
					break
				}
				id, ok := homepageModelCID(m.CID)
				if !ok || seen[id] {
					continue
				}
				seen[id] = true
				source := strings.TrimSpace(m.SourceURL)
				if utf8.RuneCountInString(source) > homepageMaxURL || !safeSourceURL(source) {
					source = ""
				}
				block.Items = append(block.Items, homepageItem{
					Name: clipRunes(m.Name, homepageMaxTitle), CID: id,
					SourceURL: source, Caption: clipRunes(m.Caption, homepageMaxCaption),
				})
			}
			out.Blocks = append(out.Blocks, block)
		case "contacts":
			if !contacts {
				contacts = true
				out.Blocks = append(out.Blocks, homepageBlock{Type: b.Type, Title: clipRunes(b.Title, homepageMaxTitle)})
			}
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

// THE MODELS AN ENTITY CLAIMS (owner 2026-10-06): a homepage shows them from
// the node itself, so a model published somewhere else is imported first. The
// node pins the bytes and serves them same-origin at /ipfs/<cid>, which every
// gateway forwards; the address it came from stays in the document as
// provenance.
const (
	homepageModelMaxBytes     = 64 << 20
	homepageModelFetchTimeout = 2 * time.Minute
)

// glbMagic opens every glTF binary, followed by its version (2) as a little-
// endian uint32.
var glbMagic = []byte{'g', 'l', 'T', 'F', 2, 0, 0, 0}

// handleHomepageModels imports one model: a GLB request body, {"url"} the node
// downloads over https, or {"cid"} the node fetches over IPFS. Only a glTF
// binary within homepageModelMaxBytes is pinned. The answer is
// {"cid", "bytes", "source_url"}. The caller gates it to the operator.
func handleHomepageModels(apiURL func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		api := strings.TrimSpace(apiURL())
		if api == "" {
			http.Error(w, "This node has no IPFS repository to keep models in.", http.StatusServiceUnavailable)
			return
		}
		tmp, err := os.CreateTemp("", "homepage-model-*.glb")
		if err != nil {
			http.Error(w, "could not stage the model", http.StatusInternalServerError)
			return
		}
		path := tmp.Name()
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(r.Context(), homepageModelFetchTimeout)
		defer cancel()

		var source string
		media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch media {
		case "application/json":
			var req struct {
				URL string `json:"url"`
				CID string `json:"cid"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
				tmp.Close()
				http.Error(w, "send {\"url\"} or {\"cid\"}", http.StatusBadRequest)
				return
			}
			switch {
			case strings.TrimSpace(req.CID) != "":
				tmp.Close()
				id, ok := homepageModelCID(req.CID)
				if !ok {
					http.Error(w, "That is not an IPFS CID.", http.StatusBadRequest)
					return
				}
				if err := storage.FetchIPFSBlockByCIDToFile(ctx, api, id, path); err != nil {
					log.Debugf("homepage model %s: %v", id, err)
					http.Error(w, "The node could not fetch that CID over IPFS.", http.StatusBadGateway)
					return
				}
			case strings.TrimSpace(req.URL) != "":
				source = strings.TrimSpace(req.URL)
				err := downloadHomepageModel(ctx, source, tmp)
				tmp.Close()
				if err != nil {
					log.Debugf("homepage model %s: %v", source, err)
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
			default:
				tmp.Close()
				http.Error(w, "send {\"url\"} or {\"cid\"}", http.StatusBadRequest)
				return
			}
		case "model/gltf-binary", "application/octet-stream":
			n, err := io.Copy(tmp, io.LimitReader(r.Body, homepageModelMaxBytes+1))
			tmp.Close()
			if err != nil {
				http.Error(w, "could not read the model", http.StatusBadRequest)
				return
			}
			if n > homepageModelMaxBytes {
				http.Error(w, "The model is larger than 64 MiB.", http.StatusRequestEntityTooLarge)
				return
			}
		default:
			tmp.Close()
			http.Error(w, "send a .glb file or {\"url\"} / {\"cid\"}", http.StatusUnsupportedMediaType)
			return
		}

		size, err := checkHomepageModel(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		id, err := storage.PinAssetGLB(ctx, api, path)
		if err != nil {
			log.Warnf("homepage model pin: %v", err)
			http.Error(w, "The node could not pin the model.", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"cid": id, "bytes": size, "source_url": source})
	}
}

// modelProblem is a reason an import failed, worded for the operator.
type modelProblem string

func (p modelProblem) Error() string { return string(p) }

// checkHomepageModel admits a glTF binary within the size limit.
func checkHomepageModel(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, modelProblem("The node could not read the model.")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, modelProblem("The node could not read the model.")
	}
	if info.Size() > homepageModelMaxBytes {
		return 0, modelProblem("The model is larger than 64 MiB.")
	}
	head := make([]byte, len(glbMagic))
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, glbMagic) {
		return 0, modelProblem("That is not a glTF binary (.glb) model.")
	}
	return info.Size(), nil
}

// downloadHomepageModel fetches a model the operator named by address, over
// https only, from public addresses only (checked on the resolved address, so
// a name cannot be pointed back inside the network), within the size limit.
func downloadHomepageModel(ctx context.Context, raw string, out io.Writer) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return modelProblem("Paste an https address of a .glb file.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return modelProblem("Paste an https address of a .glb file.")
	}
	req.Header.Set("Accept", "model/gltf-binary, application/octet-stream;q=0.9, */*;q=0.1")
	resp, err := homepageModelClient().Do(req)
	if err != nil {
		return modelProblem("The node could not download that address.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return modelProblem(fmt.Sprintf("That address answered %d.", resp.StatusCode))
	}
	if resp.ContentLength > homepageModelMaxBytes {
		return modelProblem("The model is larger than 64 MiB.")
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, homepageModelMaxBytes+1))
	if err != nil {
		return modelProblem("The download did not finish.")
	}
	if n > homepageModelMaxBytes {
		return modelProblem("The model is larger than 64 MiB.")
	}
	return nil
}

func homepageModelClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 15 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !publicUnicast(ip) {
				return fmt.Errorf("refusing %s: not a public address", host)
			}
			return nil
		},
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("redirect leaves https")
			}
			return nil
		},
	}
}

// publicUnicast reports an address on the public internet: not loopback,
// private, link-local, multicast, unspecified or carrier-grade NAT.
func publicUnicast(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64 {
		return false
	}
	return ip.IsGlobalUnicast()
}
