// Package apps is the node's $APP registry: which SDS $APP records this
// runtime knows about, and which one each RUNTIME CLASS opens by default.
//
// # Why this exists (owner ruling 2026-08-04)
//
// Verbatim: "there needs to be a default $APP for the SDN node software
// (server or browser), it's the Dashboard for the server and the
// orbital-console for the browser, with both loaded, and just like in the
// design there's a link to each one in the other."
//
// So a node has TWO default apps, one per runtime class, and each must be
// able to link to the other. A browser client pointed at a node must be able
// to discover BOTH anonymously, before it has an identity or a session.
//
// # What this package is NOT
//
// It holds no app logic. It decodes $APP records, keeps them keyed by ID,
// answers "which app is the default for this runtime class", and hands back
// the record bytes. Everything an app DOES lives in the app — the host is
// connectors only (WASM-not-Go host boundary law).
//
// # Truth ordering
//
// The $APP RECORD is the truth about an app: ID, NAME, VERSION, its UI pages
// and their content hashes all come from the decoded FlatBuffer, never from a
// hand-written table. Which app is DEFAULT for a runtime class is node policy
// — one node may open a different app than another — so it is a registry-level
// binding set by the node's own declaration and overridable in deployed
// config, exactly like the $PMM catalog's tiers and access policies.
package apps

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// RuntimeClass names an SDN runtime that opens a default app.
//
// Lowercase: this is an API-synthesized vocabulary, not an SDS record field
// (JSON-capitalization law). It maps onto the $APP schema's existing
// appRuntimeTarget vocabulary — server ↔ NODE, browser ↔ PAGE — which
// APPModuleRef.RUNTIME_TARGET already uses per member module.
type RuntimeClass string

const (
	// RuntimeServer is the node daemon: the Dashboard is its default app.
	RuntimeServer RuntimeClass = "server"
	// RuntimeBrowser is the browser SDN client: the Orbital Console is its
	// default app.
	RuntimeBrowser RuntimeClass = "browser"
)

// RuntimeClasses is the fixed, ordered set of runtime classes. Ordered so
// every response enumerates them the same way.
var RuntimeClasses = []RuntimeClass{RuntimeServer, RuntimeBrowser}

// Valid reports whether c is a known runtime class.
func (c RuntimeClass) Valid() bool {
	return c == RuntimeServer || c == RuntimeBrowser
}

// ParseRuntimeClass accepts the runtime-class spelling used in config and
// query strings, plus the $APP schema's appRuntimeTarget names (NODE/PAGE) so
// a record-carried value maps without a second vocabulary.
func ParseRuntimeClass(raw string) (RuntimeClass, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "server", "node":
		return RuntimeServer, true
	case "browser", "page":
		return RuntimeBrowser, true
	}
	return "", false
}

// Entry states. Lowercase — synthesized, not record fields.
const (
	// StateInstalled means this node HOLDS the $APP record: every record
	// field in the entry was decoded from those bytes.
	StateInstalled = "installed"
	// StateDeclared means the node advertises the app and where to get it,
	// but holds no record for it. Fields other than ID/NAME/VERSION/url are
	// absent, and no record is served — a declared app is a pointer, never a
	// claim about content the node cannot show.
	StateDeclared = "declared"
)

// UIPage is one $APP UI page as reported by the registry.
//
// Record fields keep their IDL capitalization verbatim (JSON-capitalization
// law). CONTENT is deliberately absent: the page bytes are delivered by the
// app's own URL or by fetching the record, never duplicated into a listing.
type UIPage struct {
	ID            string `json:"ID"`
	Title         string `json:"TITLE,omitempty"`
	Description   string `json:"DESCRIPTION,omitempty"`
	Icon          string `json:"ICON,omitempty"`
	MediaType     string `json:"MEDIA_TYPE,omitempty"`
	Encoding      string `json:"ENCODING,omitempty"`
	ContentSHA256 string `json:"CONTENT_SHA256,omitempty"`
	Entry         bool   `json:"ENTRY"`
	ModuleID      string `json:"MODULE_ID,omitempty"`
	URL           string `json:"URL,omitempty"`
	// ContentBytes is the decoded page size. Synthesized (lowercase): the
	// record carries the content, not its length.
	ContentBytes int `json:"content_bytes,omitempty"`
}

// CrossLink points at the OTHER runtime class's default app — the "link to
// each one in the other" the owner's ruling requires. Synthesized entirely.
type CrossLink struct {
	RuntimeClass RuntimeClass `json:"runtime_class"`
	AppID        string       `json:"app_id,omitempty"`
	Name         string       `json:"name,omitempty"`
	URL          string       `json:"url,omitempty"`
}

// Entry is one registered app.
//
// Two vocabularies on purpose, per the JSON-capitalization law: fields read
// out of the $APP FlatBuffer keep the IDL's UPPER_SNAKE spelling; everything
// the node synthesizes (runtime class, state, URLs, cross-links) is lowercase.
type Entry struct {
	// --- synthesized -----------------------------------------------------
	RuntimeClass RuntimeClass `json:"runtime_class"`
	State        string       `json:"state"`
	// Default is true when this entry is the default app of its runtime class.
	Default bool `json:"default"`
	// URL is where this runtime opens the app. Origin-relative for apps this
	// node serves itself (the dashboard is "/"); absolute for an app served
	// elsewhere.
	URL string `json:"url,omitempty"`
	// RecordURL is where the $APP record bytes can be fetched. Empty for a
	// declared entry — there is nothing to serve.
	RecordURL string `json:"record_url,omitempty"`
	// RecordBytes is the size of the $APP FlatBuffer, so a client can decide
	// whether to fetch it before doing so.
	RecordBytes int `json:"record_bytes,omitempty"`
	// CrossLink is filled in only on the defaults view.
	CrossLink *CrossLink `json:"cross_link,omitempty"`

	// --- $APP record fields (IDL capitalization, verbatim) ---------------
	ID          string   `json:"ID"`
	Name        string   `json:"NAME,omitempty"`
	Version     string   `json:"VERSION,omitempty"`
	Description string   `json:"DESCRIPTION,omitempty"`
	CreatedAt   string   `json:"CREATED_AT,omitempty"`
	UpdatedAt   string   `json:"UPDATED_AT,omitempty"`
	UI          []UIPage `json:"UI,omitempty"`

	// record holds the $APP FlatBuffer for an installed entry. Never
	// serialized — the JSON view describes the record, the record URL serves
	// it.
	record []byte
}

// Declaration describes an app this node advertises but holds no record for.
type Declaration struct {
	ID           string
	Name         string
	Version      string
	Description  string
	RuntimeClass RuntimeClass
	// URL is where the runtime opens the app. Required: an advertised app a
	// client cannot reach is not an advertisement, it is noise.
	URL string
}

// Registry holds the node's known apps and the per-runtime-class defaults.
// Safe for concurrent use: it is read by an anonymous HTTP surface and
// written only during wiring.
type Registry struct {
	mu       sync.RWMutex
	entries  map[string]*Entry
	order    []string
	defaults map[RuntimeClass]string
	// recordURLPrefix is prepended to an installed entry's ID to form its
	// RecordURL. Empty means record bytes are not served, and RecordURL is
	// then reported empty rather than pointing at a route that 404s.
	recordURLPrefix string
}

// New returns an empty registry. recordURLPrefix is the origin-relative
// prefix the record-serving route is mounted at (e.g. "/api/v1/apps/records/");
// pass "" when no such route is mounted.
func New(recordURLPrefix string) *Registry {
	return &Registry{
		entries:         map[string]*Entry{},
		defaults:        map[RuntimeClass]string{},
		recordURLPrefix: strings.TrimSpace(recordURLPrefix),
	}
}

// InstallRecord decodes a size-prefixed $APP FlatBuffer and registers it for
// the given runtime class. The record is the truth: every record field on the
// entry comes from these bytes.
//
// url is where this runtime opens the app. Re-installing the same ID replaces
// the entry (a rebuilt dashboard supersedes the previous one) and leaves the
// default binding untouched.
func (r *Registry) InstallRecord(class RuntimeClass, url string, record []byte) (*Entry, error) {
	if !class.Valid() {
		return nil, fmt.Errorf("apps: unknown runtime class %q", class)
	}
	entry, err := DecodeAPP(record)
	if err != nil {
		return nil, err
	}
	entry.RuntimeClass = class
	entry.State = StateInstalled
	entry.URL = strings.TrimSpace(url)
	entry.RecordBytes = len(record)
	entry.record = record

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recordURLPrefix != "" {
		entry.RecordURL = r.recordURLPrefix + entry.ID
	}
	r.put(entry)
	return entry, nil
}

// Declare registers an app this node advertises without holding its record.
func (r *Registry) Declare(d Declaration) (*Entry, error) {
	if !d.RuntimeClass.Valid() {
		return nil, fmt.Errorf("apps: unknown runtime class %q", d.RuntimeClass)
	}
	id := strings.TrimSpace(d.ID)
	if id == "" {
		return nil, fmt.Errorf("apps: declared app needs an ID")
	}
	url := strings.TrimSpace(d.URL)
	if url == "" {
		return nil, fmt.Errorf("apps: declared app %q needs a url", id)
	}
	entry := &Entry{
		RuntimeClass: d.RuntimeClass,
		State:        StateDeclared,
		URL:          url,
		ID:           id,
		Name:         strings.TrimSpace(d.Name),
		Version:      strings.TrimSpace(d.Version),
		Description:  strings.TrimSpace(d.Description),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.put(entry)
	return entry, nil
}

// put stores an entry, preserving first-registration order. Caller holds mu.
func (r *Registry) put(entry *Entry) {
	if _, exists := r.entries[entry.ID]; !exists {
		r.order = append(r.order, entry.ID)
	}
	r.entries[entry.ID] = entry
}

// SetDefault binds a runtime class to an already-registered app.
//
// An unknown ID is an error rather than a silent no-op: a config that names a
// default the node does not have is a mistake the operator must see, and a
// silently ignored default would leave a client opening the wrong app with no
// way to tell.
func (r *Registry) SetDefault(class RuntimeClass, appID string) error {
	if !class.Valid() {
		return fmt.Errorf("apps: unknown runtime class %q", class)
	}
	id := strings.TrimSpace(appID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if id == "" {
		delete(r.defaults, class)
		return nil
	}
	entry, ok := r.entries[id]
	if !ok {
		return fmt.Errorf("apps: no app %q is registered on this node", id)
	}
	if entry.RuntimeClass != class {
		return fmt.Errorf("apps: app %q targets runtime class %q, cannot be the %q default",
			id, entry.RuntimeClass, class)
	}
	r.defaults[class] = id
	return nil
}

// Default returns the default app of a runtime class.
//
// Resolution: an explicit SetDefault binding wins; otherwise the single
// registered app of that class is the default; otherwise there is none. The
// registry never guesses between two candidates — an ambiguous default is
// reported as absent so the operator declares one.
func (r *Registry) Default(class RuntimeClass) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.defaultLocked(class)
	if !ok {
		return Entry{}, false
	}
	return entry.view(true), true
}

// defaultLocked resolves the default entry. Caller holds mu.
func (r *Registry) defaultLocked(class RuntimeClass) (*Entry, bool) {
	if id, ok := r.defaults[class]; ok {
		if entry, ok := r.entries[id]; ok {
			return entry, true
		}
	}
	var only *Entry
	for _, id := range r.order {
		entry := r.entries[id]
		if entry == nil || entry.RuntimeClass != class {
			continue
		}
		if only != nil {
			return nil, false // ambiguous: two candidates, no declared winner
		}
		only = entry
	}
	if only == nil {
		return nil, false
	}
	return only, true
}

// Defaults returns the default app of every runtime class that has one, with
// each entry's cross-link to the other class's default filled in.
func (r *Registry) Defaults() map[RuntimeClass]Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	resolved := map[RuntimeClass]*Entry{}
	for _, class := range RuntimeClasses {
		if entry, ok := r.defaultLocked(class); ok {
			resolved[class] = entry
		}
	}

	out := make(map[RuntimeClass]Entry, len(resolved))
	for class, entry := range resolved {
		view := entry.view(true)
		for _, other := range RuntimeClasses {
			if other == class {
				continue
			}
			peer, ok := resolved[other]
			if !ok {
				continue
			}
			view.CrossLink = &CrossLink{
				RuntimeClass: other,
				AppID:        peer.ID,
				Name:         peer.Name,
				URL:          peer.URL,
			}
			break
		}
		out[class] = view
	}
	return out
}

// List returns every registered app in registration order.
func (r *Registry) List() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defaults := map[string]bool{}
	for _, class := range RuntimeClasses {
		if entry, ok := r.defaultLocked(class); ok {
			defaults[entry.ID] = true
		}
	}
	out := make([]Entry, 0, len(r.order))
	for _, id := range r.order {
		entry := r.entries[id]
		if entry == nil {
			continue
		}
		out = append(out, entry.view(defaults[entry.ID]))
	}
	return out
}

// Record returns an installed app's $APP FlatBuffer bytes.
func (r *Registry) Record(appID string) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[strings.TrimSpace(appID)]
	if !ok || len(entry.record) == 0 {
		return nil, false
	}
	return entry.record, true
}

// IDs returns the registered app IDs, sorted. Diagnostics only.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]string(nil), r.order...)
	sort.Strings(out)
	return out
}

// view returns a copy safe to hand out: the record bytes are dropped and the
// default flag is stamped for this response.
func (e *Entry) view(isDefault bool) Entry {
	out := *e
	out.record = nil
	out.Default = isDefault
	out.UI = append([]UIPage(nil), e.UI...)
	out.CrossLink = nil
	return out
}
