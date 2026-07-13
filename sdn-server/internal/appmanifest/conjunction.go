package appmanifest

import "errors"

// Conjunction app (App 1 of the SDN apps program).
//
// WHERE THE RECORD LIVES (C4 decision, documented per task item 5):
//
// The canonical conjunction APP record is NOT a committed file. It is DERIVED
// at build/release time from the single source of truth — the self-contained
// serving artifact at cmd/spacedatanetwork/embedded/conjunction_app.html (the
// C1/C2/C3 build output). NewConjunctionApp(htmlBytes) is the pure builder;
// the small generator in internal/appmanifest/genapprecord runs it against the
// embedded artifact to emit the canonical record on demand. Checking in a
// second copy of the ~231 KiB artifact (as inline CONTENT) would create a
// drift hazard, so we deliberately do not: the record is generated, and the
// HARD acceptance test (conjunction_test.go) re-derives it from the embed and
// asserts decode(CONTENT) byte-equals the embed — the record is the source of
// truth for the app definition, the embed is the serving copy, and the test
// fails the moment the two disagree.
//
// FUTURE SERVING INTEGRATION (follow-up, intentionally NOT implemented here):
// C2's daemon handler currently serves the raw embedded artifact at "/". The
// next step is to have the daemon serve decode(record.Pages[entry].Content)
// instead, making the APP record the served object and letting a release step
// sign the record (CreatedAt/UpdatedAt stamped then, ContentSHA256 already
// covering the bytes). That is a serving-code change (out of C4's locked
// scope) and is left as the documented follow-up.

const (
	// ConjunctionAppID is the stable identity of the conjunction app.
	ConjunctionAppID = "io.spaceaware.conjunction"
	// ConjunctionAppName is the app's display name.
	ConjunctionAppName = "Conjunction Screening"
	// ConjunctionAppVersion is the app manifest version.
	ConjunctionAppVersion = "1.0.0"
	// ConjunctionAppDescription summarizes the app.
	ConjunctionAppDescription = "Anonymous conjunction-screening console for the Space Data Network. " +
		"Pure-UI application (screening runs in demo mode per wiring-analysis D4); " +
		"consumes the SDN's anonymous gateway surfaces."
	// ConjunctionPageID is the app-local id of its single UI page.
	ConjunctionPageID = "conjunction"
	// ConjunctionPageMediaType is the media type of the decoded page bytes.
	ConjunctionPageMediaType = "text/html"
)

// NewConjunctionApp builds the canonical conjunction APP record (App 1) from
// the DECODED conjunction_app.html bytes. The caller supplies the bytes (read
// from the embedded serving copy) so the record is derived from — and
// verifiable against — that one source of truth, never a checked-in duplicate.
//
// Modeling decisions (documented honestly):
//
//   - MODULES: none. The conjunction app is pure-UI in v1 (no member WASM
//     modules), which schema/APP allows.
//
//   - SOURCES (kind external-api): the anonymous, same-origin gateway surfaces
//     the app actually fetches — /api/v1/peers, /api/v1/channels, /api/v1/stats,
//     /api/v1/data/health, and /api/storefront/listings/search. These are API
//     endpoints external to the app, not SDS record types, so external-api is
//     the honest classification (they are NOT modeled as DataRef.SDSType, which
//     must name an SDS schema code).
//
//   - DATA (consumes): the SDS record types the app's screening domain consumes
//     THROUGH those surfaces — OMM (the orbit catalog it screens) and MPE (the
//     sealed-ephemeris channel it treats as a precedence source when a provider
//     publishes one). It PRODUCES no SDS data in demo mode (results are D4
//     demo), so nothing is declared PRODUCES.
//
//   - UI: exactly one inline, entry page — the whole self-contained HTML
//     document as BASE64_GZIP CONTENT (chosen by size), with ContentSHA256 over
//     the decoded bytes and MediaType text/html.
//
// CreatedAt/UpdatedAt are left empty so the record is byte-deterministic for
// the drift gate; a release-signing step stamps them.
func NewConjunctionApp(htmlBytes []byte) (*AppManifest, error) {
	if len(htmlBytes) == 0 {
		return nil, errors.New("appmanifest: conjunction app html bytes are empty")
	}

	content, err := EncodingBase64Gzip.encodeContent(htmlBytes)
	if err != nil {
		return nil, err
	}

	manifest := &AppManifest{
		ID:          ConjunctionAppID,
		Name:        ConjunctionAppName,
		Version:     ConjunctionAppVersion,
		Description: ConjunctionAppDescription,
		Data: []DataRef{
			{
				ID:          "catalog-omm",
				SDSType:     "OMM",
				Direction:   DataDirectionConsumes,
				Description: "Orbit catalog (OMM) screened for conjunctions; sourced via the anonymous gateway in demo mode (D4).",
			},
			{
				ID:          "mpe-ephemeris",
				SDSType:     "MPE",
				Direction:   DataDirectionConsumes,
				Description: "Sealed MPE ephemeris channel used as a precedence source when a provider publishes one.",
			},
		},
		Sources: []SourceRef{
			{ID: "gateway-peers", Kind: SourceKindExternalAPI, Ref: "/api/v1/peers", Description: "Anonymous peer directory surface."},
			{ID: "gateway-channels", Kind: SourceKindExternalAPI, Ref: "/api/v1/channels", Description: "Anonymous channel directory (MPE sealed-ephemeris detection)."},
			{ID: "gateway-stats", Kind: SourceKindExternalAPI, Ref: "/api/v1/stats", Description: "Anonymous node stats (local catalog record counts)."},
			{ID: "gateway-data-health", Kind: SourceKindExternalAPI, Ref: "/api/v1/data/health", Description: "Anonymous data-plane health surface."},
			{ID: "storefront-search", Kind: SourceKindExternalAPI, Ref: "/api/storefront/listings/search", Description: "Storefront listings search for PAID-provider linkage."},
		},
		Pages: []UIPage{
			{
				ID:            ConjunctionPageID,
				Title:         ConjunctionAppName,
				Description:   ConjunctionAppDescription,
				Content:       content,
				Encoding:      EncodingBase64Gzip,
				MediaType:     ConjunctionPageMediaType,
				ContentSHA256: sha256Hex(htmlBytes),
				Entry:         true,
			},
		},
	}

	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return manifest, nil
}
