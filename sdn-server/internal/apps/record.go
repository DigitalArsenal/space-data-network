package apps

// $APP record encode/decode.
//
// The registry never invents app metadata: an installed entry is DECODED from
// a size-prefixed $APP FlatBuffer, and the one record this node mints itself —
// its own dashboard — is built here from the artifact bytes it already serves,
// so the record and the served page can never disagree.
//
// Nothing here is app-specific. BuildInlinePageRecord takes an identity and a
// self-contained page and returns the record; which page, and whether a node
// has one at all, is the caller's business.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	flatbuffers "github.com/google/flatbuffers/go"

	APPfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/APP"
)

// FileIdentifier is the $APP FlatBuffer file identifier.
const FileIdentifier = APPfb.APPIdentifier

// ContentType is the media type $APP record bytes are served as.
const ContentType = "application/x-flatbuffers; schema=APP"

// InlinePage is a self-contained UI page to carry inline in an $APP record.
//
// "Self-contained" is the $APP contract, not a suggestion: CSS and JS inlined,
// assets as data URIs, zero external requests (schema/APP APPUIPage). This
// package does not police that — it hashes and carries exactly what it is
// given — but a page that fetches off-origin is a contract violation wherever
// it was produced.
type InlinePage struct {
	// ID is the page's app-local key. Required.
	ID string
	// Title / Description fall back to the app's when empty.
	Title       string
	Description string
	// Icon is a launcher icon identifier or inline data URI.
	Icon string
	// HTML is the decoded page bytes.
	HTML []byte
	// MediaType defaults to text/html; charset=utf-8 when empty.
	MediaType string
	// Entry marks the page the launcher opens first. Exactly one page in an
	// app sets it.
	Entry bool
}

// AppIdentity is the record-level identity of an app.
type AppIdentity struct {
	ID          string
	Name        string
	Version     string
	Description string
	// CreatedAt / UpdatedAt are RFC 3339 UTC fixed-millisecond stamps
	// (YYYY-MM-DDTHH:mm:ss.sssZ) per the schema. Left empty by default: a
	// record that restamps itself every boot is not byte-stable, and a
	// content-addressed artifact must be.
	CreatedAt string
	UpdatedAt string
}

// BuildInlinePageRecord builds a size-prefixed $APP FlatBuffer carrying one or
// more inline, self-contained UI pages.
//
// CONTENT_SHA256 is computed here from the decoded bytes, never accepted from
// the caller: the whole point of the field is that a launcher can verify the
// page it decoded, and a declared-but-unchecked hash would defeat it.
func BuildInlinePageRecord(id AppIdentity, pages []InlinePage) ([]byte, error) {
	appID := strings.TrimSpace(id.ID)
	if appID == "" {
		return nil, fmt.Errorf("apps: $APP record needs an ID")
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("apps: $APP record %q needs at least one UI page", appID)
	}
	entries := 0
	for _, p := range pages {
		if strings.TrimSpace(p.ID) == "" {
			return nil, fmt.Errorf("apps: $APP record %q has a UI page with no ID", appID)
		}
		if len(p.HTML) == 0 {
			return nil, fmt.Errorf("apps: $APP record %q page %q has no content", appID, p.ID)
		}
		if p.Entry {
			entries++
		}
	}
	if entries != 1 {
		return nil, fmt.Errorf("apps: $APP record %q must mark exactly one ENTRY page, found %d",
			appID, entries)
	}

	b := flatbuffers.NewBuilder(0)
	for i := range pages {
		if pages[i].MediaType == "" {
			pages[i].MediaType = "text/html; charset=utf-8"
		}
	}

	pageOffsets := make([]flatbuffers.UOffsetT, 0, len(pages))
	for _, p := range pages {
		sum := sha256.Sum256(p.HTML)
		pageID := b.CreateString(strings.TrimSpace(p.ID))
		title := b.CreateString(p.Title)
		desc := b.CreateString(p.Description)
		icon := b.CreateString(p.Icon)
		content := b.CreateByteString(p.HTML)
		mediaType := b.CreateString(p.MediaType)
		contentHash := b.CreateString(hex.EncodeToString(sum[:]))

		APPfb.APPUIPageStart(b)
		APPfb.APPUIPageAddID(b, pageID)
		if p.Title != "" {
			APPfb.APPUIPageAddTITLE(b, title)
		}
		if p.Description != "" {
			APPfb.APPUIPageAddDESCRIPTION(b, desc)
		}
		if p.Icon != "" {
			APPfb.APPUIPageAddICON(b, icon)
		}
		APPfb.APPUIPageAddCONTENT(b, content)
		// UTF8 — the literal page text. Untyped 0 so the generated enum type
		// stays package-private, exactly as flatc emitted it.
		APPfb.APPUIPageAddENCODING(b, 0)
		APPfb.APPUIPageAddMEDIA_TYPE(b, mediaType)
		APPfb.APPUIPageAddCONTENT_SHA256(b, contentHash)
		APPfb.APPUIPageAddENTRY(b, p.Entry)
		pageOffsets = append(pageOffsets, APPfb.APPUIPageEnd(b))
	}
	// UI is a keyed vector (APPUIPage.ID is the key), so it is written sorted
	// — otherwise a launcher's ByKey lookup binary-searches an unsorted vector.
	uiVec := b.CreateVectorOfSortedTables(pageOffsets, APPfb.APPUIPageKeyCompare)

	idOff := b.CreateString(appID)
	nameOff := b.CreateString(strings.TrimSpace(id.Name))
	versionOff := b.CreateString(strings.TrimSpace(id.Version))
	descOff := b.CreateString(strings.TrimSpace(id.Description))
	createdOff := b.CreateString(strings.TrimSpace(id.CreatedAt))
	updatedOff := b.CreateString(strings.TrimSpace(id.UpdatedAt))

	APPfb.APPStart(b)
	APPfb.APPAddID(b, idOff)
	if strings.TrimSpace(id.Name) != "" {
		APPfb.APPAddNAME(b, nameOff)
	}
	if strings.TrimSpace(id.Version) != "" {
		APPfb.APPAddVERSION(b, versionOff)
	}
	if strings.TrimSpace(id.Description) != "" {
		APPfb.APPAddDESCRIPTION(b, descOff)
	}
	APPfb.APPAddUI(b, uiVec)
	if strings.TrimSpace(id.CreatedAt) != "" {
		APPfb.APPAddCREATED_AT(b, createdOff)
	}
	if strings.TrimSpace(id.UpdatedAt) != "" {
		APPfb.APPAddUPDATED_AT(b, updatedOff)
	}
	root := APPfb.APPEnd(b)
	APPfb.FinishSizePrefixedAPPBuffer(b, root)
	return b.FinishedBytes(), nil
}

// DecodeAPP reads a size-prefixed $APP FlatBuffer into a registry Entry.
//
// It refuses a buffer whose file identifier is not "$APP": a record surface
// that decodes whatever it is handed is how a wrong-typed buffer becomes a
// plausible-looking app listing.
func DecodeAPP(record []byte) (*Entry, error) {
	if len(record) < flatbuffers.SizeUint32 {
		return nil, fmt.Errorf("apps: $APP record is empty")
	}
	if !APPfb.SizePrefixedAPPBufferHasIdentifier(record) {
		return nil, fmt.Errorf("apps: buffer is not an $APP record (file identifier mismatch)")
	}
	root := APPfb.GetSizePrefixedRootAsAPP(record, 0)
	id := strings.TrimSpace(string(root.ID()))
	if id == "" {
		return nil, fmt.Errorf("apps: $APP record has no ID")
	}
	entry := &Entry{
		ID:          id,
		Name:        string(root.NAME()),
		Version:     string(root.VERSION()),
		Description: string(root.DESCRIPTION()),
		CreatedAt:   string(root.CREATED_AT()),
		UpdatedAt:   string(root.UPDATED_AT()),
	}
	for i := 0; i < root.UILength(); i++ {
		var page APPfb.APPUIPage
		if !root.UI(&page, i) {
			continue
		}
		entry.UI = append(entry.UI, UIPage{
			ID:            string(page.ID()),
			Title:         string(page.TITLE()),
			Description:   string(page.DESCRIPTION()),
			Icon:          string(page.ICON()),
			MediaType:     string(page.MEDIA_TYPE()),
			Encoding:      page.ENCODING().String(),
			ContentSHA256: string(page.CONTENT_SHA256()),
			Entry:         page.ENTRY(),
			ModuleID:      string(page.MODULE_ID()),
			URL:           string(page.URL()),
			ContentBytes:  len(page.CONTENT()),
		})
	}
	return entry, nil
}
