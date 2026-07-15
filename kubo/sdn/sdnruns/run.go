// Package sdnruns is the supplemental-OMM Orbit-Determination (OD) run engine
// for a kubo SDN node. It drives, on an hourly cron, the owner-confirmed
// supplemental-GP pipeline: for each ENABLED provider it pulls operator
// EPHEMERIS (the INPUT), fits it to SGP4 mean elements with the REAL analysis/od
// WASM module (the OD fit — never a Go re-implementation), produces an $OMM
// record, computes the per-object position-residual RMS, and compares that RMS
// against the CelesTrak SupGP and Space-Track OMM REFERENCES (never inputs) using
// the module's own same-ephemeris scoring. Every execution is recorded as a Run.
//
// # What is real vs stubbed
//
// The OD fit, the OMM production, the RMS, the reference parity and the run
// recording are all REAL. Only the ephemeris SOURCE fetch is pluggable
// (EphemerisSource): the data-source modules that pull operator ephemeris are
// firewalled/credentialed from a workstation, so a canned real-ephemeris fixture
// stands in for the fetch. The fit the run records is the actual analysis/od
// WASM module executing.
//
// # Parity reproduction (from analysis/od/scripts)
//
// The old JS parity harness computes, per object:
//
//   - RMS = sqrt(Σ(Δx²+Δy²+Δz²)/N) over the fit points (km, TEME) — the module's
//     own RMS field.
//   - beatsCelestrak: our fitted RMS strictly less than the reference RMS.
//   - same-ephemeris score (A2.4d): the reference's OWN mean elements propagated
//     via the SAME SGP4 over the SAME fit points (the module's REFERENCE_RMS,
//     emitted only when ref* options are supplied). CelesTrak SupGP carries its
//     own fit RMS; Space-Track OMM does not, so BOTH references are compared
//     apples-to-apples through this same-ephemeris score.
//
// This package reproduces that ON the node in Go orchestration around the real
// WASM fit: it invokes od.fit once plain (our elements + our RMS) and once per
// available reference with the reference's elements as ref* options (yielding
// that reference's same-ephemeris RMS).
package sdnruns

import (
	"encoding/json"
	"time"
)

// Run status values.
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Elements is one object's fitted SGP4 mean-element set (the supplemental OMM the
// OD fit produced), carried on an ObjectResult and used to render the downloadable
// TLE / OMM / VCM element messages. Field names mirror the CCSDS OMM / SDS schema.
type Elements struct {
	ObjectName      string  `json:"object_name"`
	ObjectID        string  `json:"object_id"`
	Epoch           string  `json:"epoch"`
	NoradCatID      uint32  `json:"norad_cat_id"`
	Classification  string  `json:"classification_type,omitempty"`
	MeanMotion      float64 `json:"mean_motion"`
	Eccentricity    float64 `json:"eccentricity"`
	Inclination     float64 `json:"inclination"`
	RaOfAscNode     float64 `json:"ra_of_asc_node"`
	ArgOfPericenter float64 `json:"arg_of_pericenter"`
	MeanAnomaly     float64 `json:"mean_anomaly"`
	Bstar           float64 `json:"bstar"`
	MeanMotionDot   float64 `json:"mean_motion_dot"`
	MeanMotionDdot  float64 `json:"mean_motion_ddot"`
	EphemerisType   int     `json:"ephemeris_type"`
	ElementSetNo    uint32  `json:"element_set_no"`
	RevAtEpoch      float64 `json:"rev_at_epoch"`
	RMSKm           float64 `json:"rms_km"`
	Converged       bool    `json:"converged"`
	DataSource      string  `json:"data_source,omitempty"`
}

// ObjectResult is one fitted object within a Run: its identity, the fit RMS, the
// reference comparisons (same-ephemeris RMS of the CelesTrak SupGP and Space-Track
// references, when a reference was available for the NORAD), the beats flag, and
// the CID of the produced $OMM record. Elements carries the fitted element set for
// the element downloads.
type ObjectResult struct {
	Norad          uint32   `json:"norad"`
	ObjectName     string   `json:"object_name,omitempty"`
	ObjectID       string   `json:"object_id,omitempty"`
	Provider       string   `json:"provider"`
	Epoch          string   `json:"epoch,omitempty"`
	RMS            float64  `json:"rms"`
	CelestrakRMS   *float64 `json:"celestrak_rms,omitempty"`
	SpacetrackRMS  *float64 `json:"spacetrack_rms,omitempty"`
	BeatsCelestrak *bool    `json:"beats_celestrak,omitempty"`
	Converged      bool     `json:"converged"`
	OMMCid         string   `json:"omm_cid,omitempty"`
	Error          string   `json:"error,omitempty"`
	Elements       Elements `json:"elements"`
}

// Run is one supplemental-OMM OD execution: the providers it swept, object
// counts, the ephemeris files consumed, the fleet-average RMS, status, and the
// per-object results with their RMS + reference comparisons.
type Run struct {
	ID             string         `json:"id"`
	Started        time.Time      `json:"started"`
	Finished       *time.Time     `json:"finished,omitempty"`
	Status         string         `json:"status"`
	Providers      []string       `json:"providers"`
	ObjectsTotal   int            `json:"objects_total"`
	ObjectsDone    int            `json:"objects_done"`
	EphemerisFiles int            `json:"ephemeris_files"`
	AvgRMS         float64        `json:"avg_rms"`
	BeatCount      int            `json:"beat_count"`
	Error          string         `json:"error,omitempty"`
	Objects        []ObjectResult `json:"objects"`
}

// Summary is a Run without its per-object rows — what the run list returns.
type Summary struct {
	ID             string     `json:"id"`
	Started        time.Time  `json:"started"`
	Finished       *time.Time `json:"finished,omitempty"`
	Status         string     `json:"status"`
	Providers      []string   `json:"providers"`
	ObjectsTotal   int        `json:"objects_total"`
	ObjectsDone    int        `json:"objects_done"`
	EphemerisFiles int        `json:"ephemeris_files"`
	AvgRMS         float64    `json:"avg_rms"`
	BeatCount      int        `json:"beat_count"`
	Error          string     `json:"error,omitempty"`
}

// LiveRun is a snapshot of the currently executing run's progress: what remains
// and the running fleet-average RMS. Absent when no run is executing.
type LiveRun struct {
	ID               string    `json:"id"`
	Started          time.Time `json:"started"`
	Providers        []string  `json:"providers"`
	ObjectsTotal     int       `json:"objects_total"`
	ObjectsDone      int       `json:"objects_done"`
	ObjectsRemaining int       `json:"objects_remaining"`
	CurrentAvgRMS    float64   `json:"current_avg_rms"`
	ElapsedSeconds   float64   `json:"elapsed_seconds"`
	RemainingSeconds float64   `json:"remaining_seconds"`
}

// summary projects a Run to its list form.
func (r *Run) summary() Summary {
	return Summary{
		ID:             r.ID,
		Started:        r.Started,
		Finished:       r.Finished,
		Status:         r.Status,
		Providers:      append([]string(nil), r.Providers...),
		ObjectsTotal:   r.ObjectsTotal,
		ObjectsDone:    r.ObjectsDone,
		EphemerisFiles: r.EphemerisFiles,
		AvgRMS:         r.AvgRMS,
		BeatCount:      r.BeatCount,
		Error:          r.Error,
	}
}

// clone deep-copies a Run through a JSON round-trip so a stored run can never be
// mutated by a caller holding a returned value.
func (r *Run) clone() *Run {
	b, err := json.Marshal(r)
	if err != nil {
		return &Run{ID: r.ID}
	}
	var out Run
	if err := json.Unmarshal(b, &out); err != nil {
		return &Run{ID: r.ID}
	}
	return &out
}

// recompute refreshes the derived aggregates (objects_done, avg_rms, beat_count)
// from the object rows.
func (r *Run) recompute() {
	done := 0
	beats := 0
	var sum float64
	var n int
	for i := range r.Objects {
		obj := &r.Objects[i]
		done++
		if obj.Error == "" && obj.Converged {
			sum += obj.RMS
			n++
		}
		if obj.BeatsCelestrak != nil && *obj.BeatsCelestrak {
			beats++
		}
	}
	r.ObjectsDone = done
	r.BeatCount = beats
	if n > 0 {
		r.AvgRMS = sum / float64(n)
	} else {
		r.AvgRMS = 0
	}
}

// floatPtr returns a pointer to v (helper for the optional RMS fields).
func floatPtr(v float64) *float64 { return &v }

// boolPtr returns a pointer to v (helper for the optional beats flag).
func boolPtr(v bool) *bool { return &v }
