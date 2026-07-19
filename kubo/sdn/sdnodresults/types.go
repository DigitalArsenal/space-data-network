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
// REAL: run boundaries (from FireHistory's observed rowid ranges, or the
// one-time BackfillRange pseudo-run for rows a prior process already stored),
// object identity + fit telemetry (NORAD/name/epoch/mean-elements from $OMM,
// WRMS/iterations/fit-span from $OBD, joined by NORAD), and downloads (the
// exact stored $OMM/$OCM bytes by content-addressed cid).
//
// HONESTLY UNAVAILABLE (flagged, never fabricated): per-provider / per-source
// attribution. The composed flow's store node's "config" provenance port
// (provider/source_name/batch_id) is not wired in the current topology — see
// flatsql_store_module.cpp's read_provenance() and od_supplemental_flow.go's
// store node port list — so every wrapper row's provider/source_name/batch_id
// is empty today, and the OD node fans all 5 providers into ONE fit call with
// no per-object provider tag carried through to $OMM/$OCM/$OBD. This package
// therefore reports a run's PROVIDERS as the flow's DECLARED source-node set
// (ServiceFlow.SourceProviderPluginIDs, real topology metadata) and marks
// every per-provider stat (total/beats/avg_rms/skipped/errors/last_pulled) as
// Unavailable, with a Note naming exactly the missing telemetry — never a
// zero or empty string passed off as a real measurement. Likewise, the beats-
// CelesTrak reference comparison the OLD Go-orchestration engine used to
// compute is not performed by the new engine's plain fit at all (no ref*
// options are passed — see od_supplemental_flow.go), so BeatCount is always
// nil/unavailable, not zero.
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
// NORAD. Provider/Source are always "—"-worthy empty strings today (see the
// package doc); Unattributed flags that plainly so a UI never has to guess.
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
// (real topology metadata) plus its aggregate stats for one run. Every
// numeric/time stat is a pointer, left nil (Unavailable=true) rather than a
// fabricated 0 when the underlying per-provider attribution does not exist
// yet — see Note for exactly what telemetry would be needed.
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

// noProviderAttributionNote is attached to every ProviderStat and unattributed
// ObjectRow so the UI can surface EXACTLY what is missing, never a silent "0".
const noProviderAttributionNote = "per-provider attribution is not yet emitted: the OD flow's store node has no provenance wired to its \"config\" port (provider/source_name/batch_id are stored empty), and the OD fit node fans all providers into one call with no per-object provider tag carried onto $OMM/$OCM/$OBD. Needs: either a per-provider config edge into the store node, or a provider field added to the fit output."
