// Package sdnapps ports the SDN "apps program" applications onto the kubo-based
// SDN node, each modeled as a Space Data Standards $APP record.
//
// # An app is an $APP record, not a web server
//
// Each app here is an appmanifest.AppManifest — a COMPOSITION over existing SDS
// records (see the appmanifest package doc) — serialized to the published SDS
// $APP FlatBuffer (appmanifest.ToAPP). The app's entire UI is carried INLINE in
// the record as an APPUIPage (self-contained HTML, no external origins), and the
// app's data contract is declared as APPDataflow entries. Nothing about an app
// lives outside its $APP record: the node stores the record and serves the
// inline page straight from it.
//
//   - Supplemental OMM (App 2): the OD-fit run board. Its dataflow is one TO_PAGE
//     / OMM / GATEWAY_ROUTE entry whose locator is the node's own read-only run
//     route; the page fetches /sdn/v1/runs (run history + the live run), each
//     run's per-object RMS + CelesTrak/Space-Track parity, the searchable NORAD
//     rows, and the downloadable TLE/OMM/VCM elements, and it edits the run
//     engine's provider set + cron via /sdn/v1/modules/supplemental-omm/config.
//   - Conjunction (App 1): a working shell. Its dataflow is one TO_PAGE / CDM /
//     GATEWAY_ROUTE entry; the page renders the node's CDM records or an honest
//     "no conjunction data yet" state. The heavy screening logic is out of scope.
//
// Both apps are pure-UI (zero member modules): the schema/APP MODULES list is
// optional, and appmanifest.Validate accepts an app that declares one or more UI
// pages instead of modules.
//
// # Storage + serving
//
// Seed writes each app's $APP bytes into the node's record store under
// (source=Source, type="APP") via sdnstore.StoreManifest — a composition record
// is read back whole (never queried tabularly), so it is stored as a
// content-addressed block + index + catalog entry without a FlatSQL schema. The
// sdnapi surface then lists the installed apps at GET /sdn/v1/apps and serves
// each app's inline entry page at GET /sdn/v1/apps/<id>.
package sdnapps

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"

	cid "github.com/ipfs/go-cid"

	"github.com/ipfs/kubo/sdn/appmanifest"
)

// Source is the (source, type) provider label every app $APP record is stored
// under. The apps are published by the SDN project itself, so a single stable
// source keys the whole apps catalog: one (Source, "APP") pair holds every
// installed app record.
const Source = "sdn"

// SDSType is the 3-letter SDS record type an app record is stored under.
const SDSType = "APP"

//go:embed assets/omm_board.html assets/conjunction.html
var assets embed.FS

// ManifestStore is the minimal write surface Seed needs: the sdnstore method
// that durably stores a composition record without FlatSQL ingest. The narrow
// interface keeps this package free of the full sdnstore dependency in tests.
type ManifestStore interface {
	StoreManifest(ctx context.Context, source, sdsType string, fb []byte) (cid.Cid, error)
}

// inlinePage wraps raw HTML bytes as a self-contained inline APPUIPage: UTF-8
// content with a CONTENT_SHA256 over the exact bytes, marked as the entry page.
func inlinePage(id, title, description, htmlPath string) (appmanifest.UIPage, error) {
	raw, err := fs.ReadFile(assets, htmlPath)
	if err != nil {
		return appmanifest.UIPage{}, fmt.Errorf("sdnapps: read %s: %w", htmlPath, err)
	}
	sum := sha256.Sum256(raw)
	return appmanifest.UIPage{
		ID:            id,
		Title:         title,
		Description:   description,
		Content:       string(raw),
		Encoding:      appmanifest.EncodingUTF8,
		MediaType:     "text/html; charset=utf-8",
		ContentSHA256: hex.EncodeToString(sum[:]),
		Entry:         true,
	}, nil
}

// ommApp builds the Supplemental OMM $APP manifest (App 2).
func ommApp() (*appmanifest.AppManifest, error) {
	page, err := inlinePage(
		"board",
		"Supplemental OMM — Provider Status Board",
		"Per-provider OMM record listing served by the SDN node.",
		"assets/omm_board.html",
	)
	if err != nil {
		return nil, err
	}
	return &appmanifest.AppManifest{
		ID:          "supplemental-omm",
		Name:        "Supplemental OMM — OD-fit Run Board",
		Version:     "1.1.0",
		Description: "App 2 of the SDN apps program: the supplemental-OMM (Orbit Mean-Elements Message) OD-fit run board served inline by the SDN node — run history, per-object RMS with CelesTrak/Space-Track parity, downloadable elements, and the run engine's provider + cron controls.",
		Dataflow: []appmanifest.DataflowEntry{{
			Name:        "omm-runs",
			Direction:   appmanifest.FlowDirectionToPage,
			SDSSchema:   "OMM",
			Transport:   appmanifest.FlowTransportGatewayRoute,
			Locator:     "/sdn/v1/runs",
			Description: "Supplemental-OMM OD-fit run history (runs, per-object RMS + CelesTrak/Space-Track reference parity, and downloadable TLE/OMM/VCM elements) delivered to the page over the node's read-only gateway route.",
		}},
		Pages: []appmanifest.UIPage{page},
	}, nil
}

// conjunctionApp builds the Conjunction $APP manifest (App 1) as a shell.
func conjunctionApp() (*appmanifest.AppManifest, error) {
	page, err := inlinePage(
		"screen",
		"Conjunction Screening",
		"Conjunction (CDM) screening shell served by the SDN node.",
		"assets/conjunction.html",
	)
	if err != nil {
		return nil, err
	}
	return &appmanifest.AppManifest{
		ID:          "conjunction",
		Name:        "Conjunction Screening",
		Version:     "1.0.0",
		Description: "App 1 of the SDN apps program: a conjunction-screening shell that declares its CDM data contract and renders the node's Conjunction Data Message records (or an honest empty state).",
		Dataflow: []appmanifest.DataflowEntry{{
			Name:        "cdm-in",
			Direction:   appmanifest.FlowDirectionToPage,
			SDSSchema:   "CDM",
			Transport:   appmanifest.FlowTransportGatewayRoute,
			Locator:     "/sdn/v1/data?source={source}&type=CDM",
			Description: "Conjunction Data Messages delivered to the page over the node's read-only gateway route.",
		}},
		Pages: []appmanifest.UIPage{page},
	}, nil
}

// Manifests returns the app manifests in a stable order (OMM board first). Each
// is Validate-clean.
func Manifests() ([]*appmanifest.AppManifest, error) {
	omm, err := ommApp()
	if err != nil {
		return nil, err
	}
	conj, err := conjunctionApp()
	if err != nil {
		return nil, err
	}
	out := []*appmanifest.AppManifest{omm, conj}
	for _, m := range out {
		if err := m.Validate(); err != nil {
			return nil, fmt.Errorf("sdnapps: %s: %w", m.ID, err)
		}
	}
	return out, nil
}

// Records serializes every app manifest to its published $APP FlatBuffer,
// keyed by app ID. Each buffer round-trips through appmanifest.FromAPP.
func Records() (map[string][]byte, error) {
	manifests, err := Manifests()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(manifests))
	for _, m := range manifests {
		buf, err := m.ToAPP()
		if err != nil {
			return nil, fmt.Errorf("sdnapps: ToAPP %s: %w", m.ID, err)
		}
		out[m.ID] = buf
	}
	return out, nil
}

// Seed installs every app into the node's record store under (Source, "APP").
// It is idempotent: the store keys records by content, so re-seeding
// byte-identical app records on a later boot is a no-op. Returns the number of
// distinct app records written (or already present).
func Seed(ctx context.Context, store ManifestStore) (int, error) {
	if store == nil {
		return 0, fmt.Errorf("sdnapps: store is nil")
	}
	manifests, err := Manifests()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range manifests {
		buf, err := m.ToAPP()
		if err != nil {
			return n, fmt.Errorf("sdnapps: ToAPP %s: %w", m.ID, err)
		}
		if _, err := store.StoreManifest(ctx, Source, SDSType, buf); err != nil {
			return n, fmt.Errorf("sdnapps: store %s: %w", m.ID, err)
		}
		n++
	}
	return n, nil
}
