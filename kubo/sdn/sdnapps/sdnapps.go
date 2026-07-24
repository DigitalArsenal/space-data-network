// Package sdnapps ports the SDN "apps program" applications onto the kubo-based
// SDN node, each modeled as a Space Data Standards $APP record.
//
// # An app is an $APP record, not a web server
//
// Each app here is an appmanifest.AppManifest — a composition over existing SDS
// records — serialized to the published SDS $APP FlatBuffer. The app's entire UI
// is carried inline in the record, and its data contract is declared as
// APPDataflow entries.
//
// # Storage + serving
//
// Seed writes each app's $APP bytes into the node's record store under
// (source=Source, type="APP") via sdnstore.StoreManifest. The sdnapi surface
// lists installed apps at GET /sdn/v1/apps and serves each inline entry page at
// GET /sdn/v1/apps/<id>.
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
// under.
const Source = "sdn"

// SDSType is the 3-letter SDS record type an app record is stored under.
const SDSType = "APP"

//go:embed assets/conjunction.html assets/flow_editor.html
var assets embed.FS

// ManifestStore is the minimal write surface Seed needs.
type ManifestStore interface {
	StoreManifest(ctx context.Context, source, sdsType string, fb []byte) (cid.Cid, error)
}

// inlinePage wraps raw HTML bytes as a self-contained inline APPUIPage.
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

// conjunctionApp builds the Conjunction $APP manifest as a shell.
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

// flowEditorApp builds the SDN Flow Editor $APP manifest.
func flowEditorApp() (*appmanifest.AppManifest, error) {
	page, err := inlinePage(
		"editor",
		"SDN Flow Editor",
		"Compose locally-available modules into a flow and bake it on this node.",
		"assets/flow_editor.html",
	)
	if err != nil {
		return nil, err
	}
	return &appmanifest.AppManifest{
		ID:          "flow-editor",
		Name:        "SDN Flow Editor",
		Version:     "0.1.0",
		Description: "App 3 of the SDN apps program: a Node-RED-style flow editor served inline by the SDN node. Its palette is the node's locally-available node types (host capabilities + every guest-link module staged for baking, from GET /api/v1/flows/palette); compose a flow on a hand-rolled node/edge canvas, then Deploy to POST the graph to the node's proven bake endpoint (POST /api/v1/flows/bake), which composes, links, installs and runs a runtime.wasm.",
		Dataflow: []appmanifest.DataflowEntry{{
			Name:        "flow-bake",
			Direction:   appmanifest.FlowDirectionFromPage,
			SDSSchema:   "PLG",
			Transport:   appmanifest.FlowTransportGatewayRoute,
			Locator:     "/api/v1/flows/bake",
			Description: "The composed flow graph emitted from the page to the node's bake route.",
		}},
		Pages: []appmanifest.UIPage{page},
	}, nil
}

// Manifests returns the app manifests in stable order. Each is Validate-clean.
func Manifests() ([]*appmanifest.AppManifest, error) {
	conj, err := conjunctionApp()
	if err != nil {
		return nil, err
	}
	editor, err := flowEditorApp()
	if err != nil {
		return nil, err
	}
	out := []*appmanifest.AppManifest{conj, editor}
	for _, manifest := range out {
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("sdnapps: %s: %w", manifest.ID, err)
		}
	}
	return out, nil
}

// Records serializes every app manifest to its published $APP FlatBuffer,
// keyed by app ID.
func Records() (map[string][]byte, error) {
	manifests, err := Manifests()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(manifests))
	for _, manifest := range manifests {
		buf, err := manifest.ToAPP()
		if err != nil {
			return nil, fmt.Errorf("sdnapps: ToAPP %s: %w", manifest.ID, err)
		}
		out[manifest.ID] = buf
	}
	return out, nil
}

// Seed installs every app into the node's record store under (Source, "APP").
func Seed(ctx context.Context, store ManifestStore) (int, error) {
	if store == nil {
		return 0, fmt.Errorf("sdnapps: store is nil")
	}
	manifests, err := Manifests()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, manifest := range manifests {
		buf, err := manifest.ToAPP()
		if err != nil {
			return n, fmt.Errorf("sdnapps: ToAPP %s: %w", manifest.ID, err)
		}
		if _, err := store.StoreManifest(ctx, Source, SDSType, buf); err != nil {
			return n, fmt.Errorf("sdnapps: store %s: %w", manifest.ID, err)
		}
		n++
	}
	return n, nil
}
