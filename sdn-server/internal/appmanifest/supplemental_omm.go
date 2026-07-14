package appmanifest

import (
	_ "embed"
	"errors"
)

// Supplemental OMM app (App 2 of the SDN apps program).
//
// App 2 materializes the supplemental-TLE/OMM ingest + OD-parity program (task
// packet coordination/tasks/app2-supplemental-omm.md) as the SECOND SDS $APP
// record, mirroring how App 1 (the conjunction app, C4) was built:
// internal/appmanifest builds a canonical AppManifest, ToAPP() round-trips it
// through the published SDS $APP FlatBuffer, genapprecord derives it at
// build/release time, and a HARD drift gate asserts decode(CONTENT) byte-equals
// the checked-in page artifact.
//
// WHERE THE PAGE ARTIFACT LIVES (App 2 decision):
//
// Unlike the conjunction app — whose serving artifact is embedded in the daemon
// cmd (cmd/spacedatanetwork/embedded/conjunction_app.html) because C2 already
// serves it at "/" — App 2 does NOT (yet) ship a daemon serving route (that is a
// deliberate residual: the second-app serving/route decision is recorded, not
// implemented, so this change never touches the conjunction serving path). The
// App 2 status board therefore lives INSIDE this package at
// embedded/supplemental_omm_board.html and is go:embedded here. It is the single
// source of truth for the record's inline UI page; the drift gate re-reads the
// same checked-in file from disk and asserts the record's decoded CONTENT
// byte-equals it (and that the embed matches the checked-in file).
//
// UNLIKE the conjunction app, App 2 is NOT pure-UI: it composes NINE member
// modules — the seven Tier-1 provider source adapters, the OD fit-pipeline, and
// the catalog-synthesis module — plus data refs and source refs, so the record
// exercises the full $APP field surface (modules + data + sources + inline UI).

//go:embed embedded/supplemental_omm_board.html
var supplementalOMMBoardHTML []byte

const (
	// SupplementalOMMAppID is the stable identity of the supplemental-OMM app.
	SupplementalOMMAppID = "io.spaceaware.supplemental-omm"
	// SupplementalOMMAppName is the app's display name.
	SupplementalOMMAppName = "Supplemental OMM"
	// SupplementalOMMAppVersion is the app manifest version.
	SupplementalOMMAppVersion = "1.0.0"
	// SupplementalOMMAppDescription summarizes the app.
	SupplementalOMMAppDescription = "Supplemental TLE/OMM provider ingest with orbit-determination parity vs CelesTrak. " +
		"Pulls operator/agency ephemeris from seven Tier-1 upstreams, fits SGP4 SupGP/OMM mean elements, " +
		"and synthesizes a replacement catalog. Ships a provider status board wired to the node's anonymous gateway surfaces."
	// SupplementalOMMPageID is the app-local id of its single UI page.
	SupplementalOMMPageID = "provider-status-board"
	// SupplementalOMMPageMediaType is the media type of the decoded page bytes.
	SupplementalOMMPageMediaType = "text/html"
)

// Member-module identities (isomorphic dist wasm — the runtime-loaded artifact
// the node actually instantiates, matching modulert.Module.ContentHash()
// semantics; the top-level signed dist is a separate publishable copy and is
// intentionally not the identity keyed here). ContentHash values are the
// lowercase hex SHA-256 of each module's dist/isomorphic/module.wasm at modules
// HEAD (data-source/* adapters at 1.0.0; the two analysis modules at 0.1.0).
var supplementalOMMModules = []ModuleRef{
	{
		ID: "src-starlink", PluginID: "com.orbpro.spacex-starlink-source", Version: "1.0.0",
		ContentHash: "e637ae8609a58f5519fde0f60c2e972016d33dc4b172f677f5846ac5acbe5c97",
		Role:        "provider-adapter",
		Description: "SpaceX Starlink source adapter: fetches MEME ephemeris (api.starlink.com MANIFEST) → canonical CCSDS OEM records + signed PNM on sdn/data-source/spacex-starlink.",
	},
	{
		ID: "src-iss", PluginID: "com.orbpro.iss-source", Version: "1.0.0",
		ContentHash: "fadce99804b38b57bb6004ac3bee502256e80bae1ab3e4b9d2fe4aad68d50b03",
		Role:        "provider-adapter",
		Description: "ISS source adapter: NASA public S3 CCSDS OEM (EME2000/UTC as-declared) → canonical OEM records + PNM on sdn/data-source/iss.",
	},
	{
		ID: "src-oneweb", PluginID: "com.orbpro.oneweb-source", Version: "1.0.0",
		ContentHash: "37aac5cd1e5695c7a7f95ae0104e303823346bbd76817a581793d2a6d020c726",
		Role:        "provider-adapter",
		Description: "OneWeb source adapter: LTEF feed → honest OEM metadata shells (LTEF encoding undocumented; raw row preserved in signed provenance) + PNM on sdn/data-source/oneweb.",
	},
	{
		ID: "src-gps", PluginID: "com.orbpro.gps-source", Version: "1.0.0",
		ContentHash: "ef0f3525716089f4d019de21d9c166239a9805a2aa998ac037150180e3b0469d",
		Role:        "provider-adapter",
		Description: "GPS source adapter: NAVCEN SEM/YUMA broadcast almanac → honest OMM (GPS-LNAV-ALMANAC mean elements; no state ephemeris to fit) + PNM on sdn/data-source/gps.",
	},
	{
		ID: "src-glonass", PluginID: "com.orbpro.glonass-source", Version: "1.0.0",
		ContentHash: "e61bad02e2e58f3de7426d53fe2f8be31f1517d58ff21c9b802143165bc66faa",
		Role:        "provider-adapter",
		Description: "GLONASS source adapter: IAC precise SP3-d (IGS20/GPS-time as-declared, position-only) → canonical OEM records + PNM on sdn/data-source/glonass.",
	},
	{
		ID: "src-intelsat", PluginID: "com.orbpro.intelsat-source", Version: "1.0.0",
		ContentHash: "0a9c612f003b4042c452b97fdb7ade0bb59001ee45eb0f7d1b95c62b2cd87984",
		Role:        "provider-adapter",
		Description: "Intelsat source adapter: MyIntelsat public ECF ephemeris (independent of SES) → canonical OEM records (name→NORAD registry seam) + PNM on sdn/data-source/intelsat.",
	},
	{
		ID: "src-cpf", PluginID: "com.orbpro.cpf-source", Version: "1.0.0",
		ContentHash: "62adbcb46c0d390e4d83f899e8d50c9eb2fc02268c1318d12bf2a7b228995012",
		Role:        "provider-adapter",
		Description: "CPF source adapter: ILRS CPF v2 predictions via anonymous EDC (position-only) → canonical OEM records + PNM on sdn/data-source/cpf.",
	},
	{
		ID: "od-fit-pipeline", PluginID: "com.orbpro.od-fit-pipeline", Version: "0.1.0",
		ContentHash: "7b43935c3adaa1cdbc2c4d4c041ffddafce3723fd7a438937534bb084734dffd",
		Role:        "od-fit",
		Description: "OD fit pipeline: storage.query(OEM) → od::fit_sgp4_series (OD module compiled in, byte-untouched) → schema-exact fitted OMM + PNM on sdn/supgp/<provider>.",
	},
	{
		ID: "catalog-synthesis", PluginID: "com.orbpro.catalog-synthesis", Version: "0.1.0",
		ContentHash: "2181c44b9ca47336c187e5a3a2edb8513fe21a25ab427edf57071e2eaec1d684",
		Role:        "catalog-synthesis",
		Description: "Catalog synthesis: merges fitted OMMs + GPS almanac OMM + Space-Track GP into one canonical OMM catalog with deterministic per-NORAD precedence + a synthesis summary record on sdn/data-source/catalog-synthesis.",
	},
}

// Data refs. OEM is BOTH-direction (produced as canonical CCSDS OEM by the six
// OEM adapters, consumed by the fit pipeline as fit input); the OMM refs are
// wired to their producing module for referential integrity. The GPS lane is
// distinct (broadcast almanac → OMM directly, not fittable). The synthesis
// summary record has no registered SDS schema code, so it is NOT modeled as a
// DataRef (that would require a non-schema SDSType, violating the schema-exact
// rule) — it is described on the catalog-synthesis module and the catalog OMM
// ref instead.
var supplementalOMMData = []DataRef{
	{
		ID: "oem-ephemeris", SDSType: "OEM", Direction: DataDirectionBoth,
		Description: "Provider operator/agency ephemeris as canonical CCSDS OEM: produced by the six OEM source adapters (starlink/iss/oneweb/glonass/intelsat/cpf), consumed by the OD fit pipeline as the fit window.",
	},
	{
		ID: "fitted-omm", SDSType: "OMM", Direction: DataDirectionProduces, ModuleID: "od-fit-pipeline",
		Description: "OD-fitted SGP4 SupGP/OMM mean elements, one per object per provider (A2.4 parity gate target vs CelesTrak SupGP).",
	},
	{
		ID: "gps-almanac-omm", SDSType: "OMM", Direction: DataDirectionProduces, ModuleID: "src-gps",
		Description: "GPS broadcast almanac emitted directly as OMM (GPS-LNAV-ALMANAC); no state ephemeris to fit, flows straight into synthesis.",
	},
	{
		ID: "catalog-omm", SDSType: "OMM", Direction: DataDirectionProduces, ModuleID: "catalog-synthesis",
		Description: "Synthesized replacement catalog (canonical OMM) merging fitted OMMs + GPS almanac OMM + Space-Track GP; published with a synthesis summary record and per-record provenance.",
	},
}

// Source refs (all external-api): the seven Tier-1 provider upstreams the
// adapters fetch (honest URLs from the adapter READMEs / the A2.1 inventory),
// the CelesTrak SupGP endpoint that is the parity ground truth for the A2.4
// gates, and the two Space-Track lanes (publicfiles operator ephemeris + the gp
// current-catalog class) that feed catalog synthesis.
var supplementalOMMSources = []SourceRef{
	{ID: "up-starlink", Kind: SourceKindExternalAPI, Ref: "https://api.starlink.com/public-files/ephemerides/MANIFEST.txt", Description: "SpaceX Starlink MEME ephemeris manifest (public)."},
	{ID: "up-iss", Kind: SourceKindExternalAPI, Ref: "https://nasa-public-data.s3.amazonaws.com/iss-coords/current/ISS_OEM/ISS.OEM_J2K_EPH.txt", Description: "NASA public ISS CCSDS OEM ephemeris (public S3)."},
	{ID: "up-oneweb", Kind: SourceKindExternalAPI, Ref: "https://ephemeris.oneweb.net/ltef/ltef.csv", Description: "OneWeb LTEF ephemeris feed (open; encoding undocumented)."},
	{ID: "up-gps", Kind: SourceKindExternalAPI, Ref: "https://www.navcen.uscg.gov/sites/default/files/gps/almanac/current_yuma.alm", Description: "NAVCEN GPS YUMA broadcast almanac (public)."},
	{ID: "up-glonass", Kind: SourceKindExternalAPI, Ref: "ftp://ftp.glonass-iac.ru/MCC/PRODUCTS/LATEST/Final.sp3", Description: "IAC GLONASS precise SP3-d ephemeris (public FTP; HTTPS mirror intermittently 502)."},
	{ID: "up-intelsat", Kind: SourceKindExternalAPI, Ref: "https://my.intelsat.com/ephemeris/public", Description: "MyIntelsat public operator ephemeris (ECF; weekly + maneuver files)."},
	{ID: "up-cpf", Kind: SourceKindExternalAPI, Ref: "https://edc.dgfi.tum.de/pub/slr/cpf_predicts_v2/", Description: "ILRS CPF v2 prediction files via anonymous EDC."},
	{ID: "ref-celestrak-supgp", Kind: SourceKindExternalAPI, Ref: "https://celestrak.org/NORAD/elements/supplemental/sup-gp.php", Description: "CelesTrak Supplemental GP (SupGP) — the parity ground truth for the A2.4 RMS/element gates."},
	{ID: "st-publicfiles", Kind: SourceKindExternalAPI, Ref: "https://www.space-track.org/publicfiles/", Description: "Space-Track publicfiles operator-ephemeris lane (credentialed; flat loadpublicdata listing → OEM)."},
	{ID: "st-gp", Kind: SourceKindExternalAPI, Ref: "https://www.space-track.org/basicspacedata/query/class/gp", Description: "Space-Track gp current-catalog class (credentialed; full-catalog schema-exact OMM+MPE feeding catalog synthesis)."},
}

// SupplementalOMMBoardHTML returns the go:embedded App 2 status-board bytes (the
// checked-in serving artifact the record is derived from). Callers that want to
// build the record from the on-disk file instead (the drift gate) read it
// directly and pass it to NewSupplementalOMMApp.
func SupplementalOMMBoardHTML() []byte {
	out := make([]byte, len(supplementalOMMBoardHTML))
	copy(out, supplementalOMMBoardHTML)
	return out
}

// NewSupplementalOMMApp builds the canonical supplemental-OMM APP record (App 2)
// from the DECODED status-board HTML bytes. The caller supplies the bytes (read
// from the embedded/checked-in copy) so the record is derived from — and
// verifiable against — that one source of truth, never a checked-in duplicate.
//
// Modeling decisions (documented honestly):
//
//   - MODULES: nine member modules — the seven Tier-1 provider adapters, the OD
//     fit-pipeline, and the catalog-synthesis module — referenced by PluginID +
//     the isomorphic dist wasm ContentHash (the runtime-loaded identity).
//
//   - DATA: OEM (both — adapters produce, fit-pipeline consumes), fitted OMM
//     (produced by the fit pipeline), the GPS almanac OMM lane, and the
//     synthesized catalog OMM. The synthesis summary has no SDS schema code and
//     is described rather than modeled as a DataRef.
//
//   - SOURCES (kind external-api): the seven honest provider upstreams, the
//     CelesTrak SupGP parity reference, and the two Space-Track lanes.
//
//   - UI: exactly one inline, entry page — the whole self-contained status board
//     as BASE64_GZIP CONTENT (chosen by size: the board is ~25 KB raw, gzips
//     ~3x; mirrors the conjunction app's encoding decision), MediaType text/html,
//     ContentSHA256 over the decoded bytes.
//
// CreatedAt/UpdatedAt are left empty so the record is byte-deterministic for the
// drift gate; a release-signing step stamps them.
func NewSupplementalOMMApp(htmlBytes []byte) (*AppManifest, error) {
	if len(htmlBytes) == 0 {
		return nil, errors.New("appmanifest: supplemental-omm status board html bytes are empty")
	}

	content, err := EncodingBase64Gzip.encodeContent(htmlBytes)
	if err != nil {
		return nil, err
	}

	manifest := &AppManifest{
		ID:          SupplementalOMMAppID,
		Name:        SupplementalOMMAppName,
		Version:     SupplementalOMMAppVersion,
		Description: SupplementalOMMAppDescription,
		Modules:     append([]ModuleRef(nil), supplementalOMMModules...),
		Data:        append([]DataRef(nil), supplementalOMMData...),
		Sources:     append([]SourceRef(nil), supplementalOMMSources...),
		Pages: []UIPage{
			{
				ID:            SupplementalOMMPageID,
				Title:         SupplementalOMMAppName + " — Provider Status Board",
				Description:   SupplementalOMMAppDescription,
				Content:       content,
				Encoding:      EncodingBase64Gzip,
				MediaType:     SupplementalOMMPageMediaType,
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
