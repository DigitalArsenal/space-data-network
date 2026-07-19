// Package sdnodresults is the READ-ONLY derive/serve layer over the mounted
// supplemental-OMM OD ServiceFlow (org.sdn.flows.od-supplemental-omm) and its
// linked in-wasm FlatSQL store: it turns the flow's OBSERVED fire history
// (flowrt.ServiceFlow.FireHistory/OngoingFire) plus real decoded $OMM/$OBD
// rows into the supplemental-OMM board's run log / two-level drill-down /
// download API. It replaces the disconnected, inert sdnruns.Store (the
// pre-existing Go-orchestration run engine, made fully inert per the
// SDN_OD_FLOW_LOOP.md STOP block — see plugin/plugins/sdnruntime/sdnruns.go)
// as the run log's data source: derived runs ARE the run log going forward.
//
// # What is real vs honestly unavailable
//
// REAL (as of the module's cid-keyed provenance sidecar, space-data-network-
// modules commits 5654618/5a6d684): run boundaries (from FireHistory's
// observed rowid ranges, or the one-time BackfillRange pseudo-run for rows a
// prior process already stored), object identity + fit telemetry (NORAD/
// name/epoch/mean-elements from $OMM, WRMS-or-BEST_PASS_WRMS/iterations/
// fit-span from $OBD, joined by NORAD), downloads (the exact stored $OMM/
// $OCM/$OBD bytes by content-addressed cid), AND per-provider totals/avg-RMS/
// last-pulled (Level 1): the OD flow's store node now fills provider/
// source_name/pulled_at per record from a CID-keyed sidecar the fit node
// emits (per-provider input ports give it the provider identity per object,
// entirely in-wasm — no data_source in the $OMM/$OCM/$OBD builders
// themselves, so RMS parity is untouched). This package reads
// provider/source_name/pulled_at directly as SQL columns (all three are
// declared additively in the SAME flowrt schema this binary compiles against
// — see storeRow's doc — so there is exactly one column set to query, never
// a version-skew fallback) plus decodes $OBD's WRMS/BEST_PASS_WRMS per
// provider (the module-side ruling is "no wrms store column, read-side BLOB
// decode only," so per-provider RMS is necessarily a Go-side decode grouped
// by the SQL provider column, never a store-side aggregate).
//
// BACKWARD COMPATIBILITY: records stored before the sidecar landed (e.g. the
// pre-existing ~10,847-row full-catalog backfill) carry an empty provider
// tag. Those records are NEVER vanished: they surface as a single synthesized
// "unattributed" Level-1 row with a REAL total (and avg RMS, when decodable),
// labeled plainly as pre-attribution. A DECLARED provider absent from a run's
// totals is reported as a real 0 when the run has ANY attributed rows at all
// (it fired in a run we can attribute; it just contributed nothing), or
// honestly Unavailable (nil) when NONE of the run's rows carry attribution
// yet (a run entirely predating the sidecar).
//
// STILL HONESTLY UNAVAILABLE (flagged, never fabricated): per-provider
// skipped/errors (no telemetry exists at any layer for these yet) and
// BeatCount (the new engine's plain fit performs no same-ephemeris reference
// comparison at all — no ref* options are passed, see od_supplemental_flow.go
// — so this is always nil, never a real zero).
//
// # Zero orchestration
//
// This package issues ONLY read-only SQL (flowrt.LinkedStore.Query, which
// itself refuses anything but a single SELECT) and reads flowrt's already-
// recorded, already-bounded FireHistory. It never fires a trigger, never
// touches FlowRuntime.Drain, and never decides what the flow should fetch or
// fit.
package sdnodresults

import "time"

// RunSummary is one run's list-view row — the run log's REAL replacement for
// sdnruns.Summary. Field names/shapes intentionally mirror sdnruns.Summary
// (a JSON API-synthesized read-model, not a raw SDS record — the
// json-schema-capitalization rule governs SDS record serialization, not this)
// so the existing board JS needs no rewrite for the parts that carry over.
type RunSummary struct {
	ID             string     `json:"id"`
	Started        *time.Time `json:"started,omitempty"`
	Finished       *time.Time `json:"finished,omitempty"`
	Status         string     `json:"status"`
	Providers      []string   `json:"providers"`
	ObjectsTotal   int        `json:"objects_total"`
	ObjectsDone    int        `json:"objects_done"`
	EphemerisFiles int        `json:"ephemeris_files"`
	AvgRMS         float64    `json:"avg_rms"`
	// BeatCount is ALWAYS nil: the new engine's plain fit performs no
	// same-ephemeris reference comparison at all (no ref* options are passed
	// — see od_supplemental_flow.go), so this is honestly "not computed",
	// never a real zero. A board renders nil as "—", never "0".
	BeatCount *int   `json:"beat_count,omitempty"`
	Error     string `json:"error,omitempty"`
	// Note explains anything a reader might otherwise mis-read as a
	// measurement — e.g. why BeatCount is always 0 (unavailable, never
	// computed) or why Started is absent (a synthesized backfill row).
	Note string `json:"note,omitempty"`
}

// LiveRun mirrors sdnruns.LiveRun's shape for the board's synthesized
// "ongoing" row. ObjectsTotal/ObjectsRemaining/RemainingSeconds are honestly
// 0 (unknowable in advance — the flow discovers its object set during the
// fetch, it is never pre-declared); CurrentAvgRMS and ElapsedSeconds are
// real, live-computed values.
type LiveRun struct {
	ID             string    `json:"id"`
	Started        time.Time `json:"started"`
	Providers      []string  `json:"providers"`
	ObjectsDone    int       `json:"objects_done"`
	CurrentAvgRMS  float64   `json:"current_avg_rms"`
	ElapsedSeconds float64   `json:"elapsed_seconds"`
}

// ObjectRow is one fitted object within a run — decoded from a REAL $OMM
// (identity + mean elements) optionally joined to its $OBD (fit telemetry) by
// NORAD. Provider/Source are the store's real provenance columns when the
// record was written after the module's cid-keyed sidecar landed;
// Unattributed is true only for a record that genuinely carries neither
// (e.g. a pre-sidecar backfill row) — never a blanket default.
type ObjectRow struct {
	Norad        uint32  `json:"norad"`
	ObjectName   string  `json:"object_name,omitempty"`
	ObjectID     string  `json:"object_id,omitempty"`
	Provider     string  `json:"provider,omitempty"`
	Source       string  `json:"source,omitempty"`
	Unattributed bool    `json:"unattributed"`
	Epoch        string  `json:"epoch,omitempty"`
	RMS          float64 `json:"rms"`
	HasRMS       bool    `json:"has_rms"`
	Iterations   int     `json:"iterations,omitempty"`
	FitSpanDays  float64 `json:"fit_span_days,omitempty"`
	OMMCid       string  `json:"omm_cid,omitempty"`
	OBDCid       string  `json:"obd_cid,omitempty"`
	MeanMotion   float64 `json:"mean_motion,omitempty"`
	Eccentricity float64 `json:"eccentricity,omitempty"`
	Inclination  float64 `json:"inclination,omitempty"`
}

// ProviderStat is one Level-1 drill-down row: a provider the flow DECLARES
// (real topology metadata, or the synthesized "unattributed" bucket) plus
// its aggregate stats for one run. Total/AvgRMS/LastPulled are real when the
// store's provenance attribution covers this run; Beats/Skipped are ALWAYS
// nil (no telemetry exists for them at any layer yet). Every numeric/time
// stat is a pointer, left nil rather than a fabricated 0/""; Unavailable is
// true only when this run predates provider attribution entirely for this
// provider — see Note for exactly what is missing and why.
type ProviderStat struct {
	Provider    string   `json:"provider"`
	Label       string   `json:"label"`
	Total       *int     `json:"total,omitempty"`
	Beats       *int     `json:"beats,omitempty"`
	AvgRMS      *float64 `json:"avg_rms,omitempty"`
	Skipped     *int     `json:"skipped,omitempty"`
	Errors      *int     `json:"errors,omitempty"`
	LastPulled  *string  `json:"last_pulled,omitempty"`
	Unavailable bool     `json:"unavailable"`
	Note        string   `json:"note,omitempty"`
}

// providerLabels maps a declared provider's short id to a display label,
// mirroring the board's static PROVIDERS table (omm_board.html).
var providerLabels = map[string]string{
	"spacex-starlink": "SpaceX Starlink",
	"iss":             "ISS",
	"gps":             "GPS",
	"glonass":         "GLONASS",
	"cpf":             "CPF",
	"intelsat":        "Intelsat",
	"oneweb":          "OneWeb",
}

func providerLabel(id string) string {
	if l, ok := providerLabels[id]; ok {
		return l
	}
	return id
}
