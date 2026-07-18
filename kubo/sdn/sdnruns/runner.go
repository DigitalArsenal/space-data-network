package sdnruns

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	cid "github.com/ipfs/go-cid"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/ipfs/kubo/sdn/plugins"
	"github.com/ipfs/kubo/sdn/sds"
)

// ModuleID / RunTimerMethod are the cron identity of the supplemental-OMM run
// engine: it registers with the node cron scheduler as this module with one timer
// firing the run on its effective interval (default hourly, overridable from the
// home-dir module config edited in the Modules UI).
const (
	ModuleID        = "supplemental-omm"
	ModuleName      = "Supplemental OMM (OD fit)"
	ModuleVersion   = "0.1.0"
	RunTimerMethod  = "run"
	defaultInterval = "1h"

	// DefaultProducedSource is the store lane the produced supplemental $OMM
	// records land under (the supplemental-GP product lane).
	DefaultProducedSource = "supplemental-omm"
)

// Logger is a minimal printf-style sink (nil is silent).
type Logger func(format string, args ...interface{})

// Ephemeris is one operator ephemeris object pulled for a provider: the raw
// upstream bytes (MEME text or CCSDS OEM KVN) plus the identity the OD fit stamps
// onto the produced OMM. It is the INPUT to the OD fit.
type Ephemeris struct {
	Provider   string
	Format     string // "oem" | "meme"
	ObjectName string
	ObjectID   string
	NoradCatID uint32
	DataSource string // provider source token (e.g. "ISS-E")
	Bytes      []byte
}

// EphemerisSource pulls operator ephemeris for a provider. In production this
// wraps the provider's data-source WASM module (which fetches upstream over the
// http capability); on a firewalled workstation a fixture source stands in. The
// OD fit downstream is real regardless of how the ephemeris was sourced.
type EphemerisSource interface {
	Pull(ctx context.Context, provider string) ([]Ephemeris, error)
}

// ReferenceElements is one reference OMM's mean-element set (CelesTrak SupGP or
// Space-Track), used ONLY for the same-ephemeris parity comparison — never as an
// input to the fit.
type ReferenceElements struct {
	Epoch           string
	MeanMotion      float64
	Eccentricity    float64
	Inclination     float64
	RaOfAscNode     float64
	ArgOfPericenter float64
	MeanAnomaly     float64
	Bstar           float64
	MeanMotionDot   float64
	MeanMotionDdot  float64
}

// RecordStore is the node record store seam: it stores the produced $OMM records
// and supplies the reference OMM lanes (CelesTrak SupGP / Space-Track).
// *sdnstore.Store satisfies it.
type RecordStore interface {
	Store(ctx context.Context, source, sdsType string, fb []byte) (cid.Cid, error)
	ReadBySourceType(ctx context.Context, source, sdsType string) ([][]byte, error)
}

// RunConfig is the effective run configuration resolved from the home-dir module
// config (editable in the Modules UI): which providers are enabled and which
// store lanes hold the CelesTrak SupGP + Space-Track references.
type RunConfig struct {
	EnabledProviders []string
	CelestrakSource  string
	SpacetrackSource string
	ProducedSource   string
	// ObjectCap bounds how many objects each provider fetches+fits per pull
	// (module config key `object_cap`). A provider fetches one upstream file per
	// object serially, so a full constellation cannot complete in one wasm
	// invocation; this caps the per-pull work to what finishes inside the
	// scheduled budget. 0 leaves each module's built-in default.
	ObjectCap int
}

// Config wires a Runner.
type Config struct {
	// Fitter runs the REAL analysis/od fit. Required.
	Fitter Fitter
	// Source pulls operator ephemeris per provider. Required.
	Source EphemerisSource
	// Records stores produced OMMs + supplies reference lanes. Optional: when nil
	// the produced OMM is still built (and asserted) but not persisted and no
	// reference comparison is populated.
	Records RecordStore
	// Runs persists runs + tracks the live run. Required.
	Runs *Store
	// Resolve returns the effective RunConfig at run time (home-dir config). When
	// nil the Runner uses ConfigDefault().
	Resolve func() RunConfig
	// Log is an optional printf sink.
	Log Logger
}

// Runner drives supplemental-OMM runs and registers with the cron scheduler as a
// self-scheduling module (sdncron.CronModule).
type Runner struct {
	cfg Config
}

// NewRunner builds a Runner. Fitter, Source and Runs are required.
func NewRunner(cfg Config) (*Runner, error) {
	if cfg.Fitter == nil {
		return nil, fmt.Errorf("sdnruns: Config.Fitter is required")
	}
	if cfg.Source == nil {
		return nil, fmt.Errorf("sdnruns: Config.Source is required")
	}
	if cfg.Runs == nil {
		return nil, fmt.Errorf("sdnruns: Config.Runs is required")
	}
	return &Runner{cfg: cfg}, nil
}

func (r *Runner) logf(format string, args ...interface{}) {
	if r.cfg.Log != nil {
		r.cfg.Log(format, args...)
	}
}

// ConfigDefault is the fallback RunConfig (no providers enabled, canonical
// reference lane names).
func ConfigDefault() RunConfig {
	return RunConfig{
		EnabledProviders: nil,
		CelestrakSource:  "celestrak-supgp",
		SpacetrackSource: "spacetrack",
		ProducedSource:   DefaultProducedSource,
	}
}

func (r *Runner) resolve() RunConfig {
	cfg := ConfigDefault()
	if r.cfg.Resolve != nil {
		got := r.cfg.Resolve()
		if len(got.EnabledProviders) > 0 {
			cfg.EnabledProviders = got.EnabledProviders
		}
		if strings.TrimSpace(got.CelestrakSource) != "" {
			cfg.CelestrakSource = got.CelestrakSource
		}
		if strings.TrimSpace(got.SpacetrackSource) != "" {
			cfg.SpacetrackSource = got.SpacetrackSource
		}
		if strings.TrimSpace(got.ProducedSource) != "" {
			cfg.ProducedSource = got.ProducedSource
		}
	}
	return cfg
}

// --- sdncron.CronModule ---

// ID identifies the module to the cron scheduler + the Modules UI.
func (r *Runner) ID() string { return ModuleID }

// CronMethods declares the single "run" timer (default hourly).
func (r *Runner) CronMethods() []plugins.CronMethodSpec {
	return []plugins.CronMethodSpec{{
		Method:          RunTimerMethod,
		Description:     "Fit enabled providers' operator ephemeris to supplemental OMMs and record a run.",
		DefaultInterval: defaultInterval,
		Input:           "none",
		Output:          "json",
	}}
}

// InvokeCron fires one run on the scheduled tick.
func (r *Runner) InvokeCron(ctx context.Context, method string, _ []byte) ([]byte, error) {
	if method != RunTimerMethod {
		return nil, fmt.Errorf("sdnruns: unknown timer method %q", method)
	}
	run, err := r.Run(ctx)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf(`{"ok":true,"run":%q,"objects":%d,"avg_rms":%.3f,"beats":%d}`,
		run.ID, run.ObjectsDone, run.AvgRMS, run.BeatCount)), nil
}

// Run executes one supplemental-OMM run over the currently-enabled providers,
// recording it. It is the cron entry point and the API run-now path.
func (r *Runner) Run(ctx context.Context) (*Run, error) {
	cfg := r.resolve()
	return r.RunProviders(ctx, cfg)
}

// RunProviders executes one run over cfg.EnabledProviders and records it.
func (r *Runner) RunProviders(ctx context.Context, cfg RunConfig) (*Run, error) {
	started := time.Now().UTC()
	providers := dedupe(cfg.EnabledProviders)

	// Pull ephemeris for each enabled provider (the stubbed/firewalled INPUT).
	var objects []Ephemeris
	for _, p := range providers {
		eps, err := r.cfg.Source.Pull(ctx, p)
		if err != nil {
			r.logf("sdnruns: provider %q pull failed: %v", p, err)
			continue
		}
		objects = append(objects, eps...)
	}

	run := &Run{
		ID:             NewRunID(started),
		Started:        started,
		Status:         StatusRunning,
		Providers:      providers,
		ObjectsTotal:   len(objects),
		EphemerisFiles: len(objects),
		Objects:        []ObjectResult{},
	}
	if err := r.cfg.Runs.StartRun(run); err != nil {
		return nil, fmt.Errorf("sdnruns: start run: %w", err)
	}
	r.logf("sdnruns: run %s started: providers=%v objects=%d", run.ID, providers, len(objects))

	// Build the reference indices once per run (read-only over the store lanes).
	celestrak := r.loadReferences(ctx, cfg.CelestrakSource)
	spacetrack := r.loadReferences(ctx, cfg.SpacetrackSource)

	produced := cfg.ProducedSource
	if produced == "" {
		produced = DefaultProducedSource
	}

	r.fitObjects(ctx, run.ID, objects, produced, celestrak, spacetrack)

	if err := r.cfg.Runs.FinishRun(run.ID, StatusCompleted, ""); err != nil {
		return nil, fmt.Errorf("sdnruns: finish run: %w", err)
	}
	final, err := r.cfg.Runs.Get(run.ID)
	if err != nil {
		return nil, err
	}
	r.logf("sdnruns: run %s completed: objects=%d avg_rms=%.3f beats=%d", final.ID, final.ObjectsDone, final.AvgRMS, final.BeatCount)
	return &final, nil
}

// fitObjects fits every object and appends each result to the run. When the
// Fitter is a ConcurrentFitter (e.g. ReactorFitter, a pool of resident OD reactor
// instances), the object loop fans out to Concurrency() worker goroutines that
// fit objects IN PARALLEL — the whole point of the resident-reactor conversion:
// N single-threaded OD instances running at once instead of one _start per
// object. A plain Fitter (CommandFitter, process-wide serialized by odStartMu)
// runs sequentially. AppendObject is store-mutex guarded, so concurrent appends
// are safe; the reference index maps are read-only here. Object order in the run
// record is not significant (rows are keyed/searched by NORAD).
func (r *Runner) fitObjects(ctx context.Context, runID string, objects []Ephemeris, produced string, celestrak, spacetrack map[uint32]ReferenceElements) {
	workers := 1
	if cf, ok := r.cfg.Fitter.(ConcurrentFitter); ok {
		if c := cf.Concurrency(); c > 1 {
			workers = c
		}
	}
	if workers > len(objects) {
		workers = len(objects)
	}

	appendResult := func(eph Ephemeris) {
		obj := r.fitOne(ctx, eph, produced, celestrak, spacetrack)
		if err := r.cfg.Runs.AppendObject(runID, obj); err != nil {
			r.logf("sdnruns: run %s append object %d failed: %v", runID, obj.Norad, err)
		}
	}

	if workers <= 1 {
		for _, eph := range objects {
			appendResult(eph)
		}
		return
	}

	jobs := make(chan Ephemeris)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for eph := range jobs {
				appendResult(eph)
			}
		}()
	}
	for _, eph := range objects {
		jobs <- eph
	}
	close(jobs)
	wg.Wait()
}

// fitOne runs the REAL OD fit for one object, produces + stores the OMM, and
// scores it against the CelesTrak + Space-Track references (same-ephemeris).
func (r *Runner) fitOne(ctx context.Context, eph Ephemeris, producedSource string, celestrak, spacetrack map[uint32]ReferenceElements) ObjectResult {
	obj := ObjectResult{
		Norad:      eph.NoradCatID,
		ObjectName: eph.ObjectName,
		ObjectID:   eph.ObjectID,
		Provider:   eph.Provider,
	}

	base := FitOptions{
		InputFormat: eph.Format,
		DataSource:  eph.DataSource,
		ObjectName:  eph.ObjectName,
		ObjectID:    eph.ObjectID,
		NoradCatID:  eph.NoradCatID,
	}

	// Our OD fit (real WASM module).
	plain, err := r.cfg.Fitter.Fit(ctx, eph.Bytes, base)
	if err != nil {
		obj.Error = err.Error()
		return obj
	}
	rms, err := plain.RMSKm()
	if err != nil {
		obj.Error = fmt.Sprintf("parse RMS: %v", err)
		return obj
	}
	// The fit may resolve identity fields the source did not carry (NORAD 0, etc).
	if plain.NoradCatID != 0 {
		obj.Norad = plain.NoradCatID
	}
	if plain.ObjectName != "" {
		obj.ObjectName = plain.ObjectName
	}
	if plain.ObjectID != "" {
		obj.ObjectID = plain.ObjectID
	}
	obj.Epoch = plain.Epoch
	obj.RMS = rms
	obj.Converged = plain.Converged
	obj.Elements = plain.elements()

	// Produce the $OMM and store it (the supplemental-GP product).
	ommFB := buildOMM(plain, producedSource)
	if r.cfg.Records != nil {
		if c, err := r.cfg.Records.Store(ctx, producedSource, "OMM", ommFB); err != nil {
			r.logf("sdnruns: store OMM for %d failed: %v", obj.Norad, err)
		} else {
			obj.OMMCid = c.String()
		}
	}

	// Same-ephemeris reference scoring: propagate the reference's OWN elements via
	// the SAME SGP4 over the SAME fit points (REFERENCE_RMS). CelesTrak SupGP also
	// gives the beats flag (ours strictly < theirs).
	if ref, ok := celestrak[obj.Norad]; ok {
		if refRMS, ok := r.scoreReference(ctx, eph, base, ref); ok {
			obj.CelestrakRMS = floatPtr(refRMS)
			obj.BeatsCelestrak = boolPtr(rms < refRMS)
		}
	}
	if ref, ok := spacetrack[obj.Norad]; ok {
		if refRMS, ok := r.scoreReference(ctx, eph, base, ref); ok {
			obj.SpacetrackRMS = floatPtr(refRMS)
		}
	}
	return obj
}

// scoreReference re-invokes the fit with the reference's mean elements as ref*
// options, returning the reference's same-ephemeris RMS (REFERENCE_RMS).
func (r *Runner) scoreReference(ctx context.Context, eph Ephemeris, base FitOptions, ref ReferenceElements) (float64, bool) {
	opts := base
	opts.RefEpoch = ref.Epoch
	opts.RefMeanMotion = floatPtr(ref.MeanMotion)
	opts.RefEccentricity = floatPtr(ref.Eccentricity)
	opts.RefInclination = floatPtr(ref.Inclination)
	opts.RefRaan = floatPtr(ref.RaOfAscNode)
	opts.RefArgPericenter = floatPtr(ref.ArgOfPericenter)
	opts.RefMeanAnomaly = floatPtr(ref.MeanAnomaly)
	opts.RefBstar = floatPtr(ref.Bstar)
	opts.RefMeanMotionDot = floatPtr(ref.MeanMotionDot)
	opts.RefMeanMotionDdot = floatPtr(ref.MeanMotionDdot)
	scored, err := r.cfg.Fitter.Fit(ctx, eph.Bytes, opts)
	if err != nil {
		r.logf("sdnruns: reference score for %d failed: %v", eph.NoradCatID, err)
		return 0, false
	}
	return scored.ReferenceRMSKm()
}

// loadReferences reads every OMM record under a store lane and indexes it by
// NORAD. An empty lane name or a nil store yields an empty index (no comparison).
func (r *Runner) loadReferences(ctx context.Context, source string) map[uint32]ReferenceElements {
	out := map[uint32]ReferenceElements{}
	if r.cfg.Records == nil || strings.TrimSpace(source) == "" {
		return out
	}
	recs, err := r.cfg.Records.ReadBySourceType(ctx, source, "OMM")
	if err != nil {
		r.logf("sdnruns: read reference lane %q failed: %v", source, err)
		return out
	}
	for _, fb := range recs {
		o := OMM.GetRootAsOMM(fb, 0)
		norad := o.NORAD_CAT_ID()
		if norad == 0 {
			continue
		}
		out[norad] = ReferenceElements{
			Epoch:           string(o.EPOCH()),
			MeanMotion:      o.MEAN_MOTION(),
			Eccentricity:    o.ECCENTRICITY(),
			Inclination:     o.INCLINATION(),
			RaOfAscNode:     o.RA_OF_ASC_NODE(),
			ArgOfPericenter: o.ARG_OF_PERICENTER(),
			MeanAnomaly:     o.MEAN_ANOMALY(),
			Bstar:           o.BSTAR(),
			MeanMotionDot:   o.MEAN_MOTION_DOT(),
			MeanMotionDdot:  o.MEAN_MOTION_DDOT(),
		}
	}
	return out
}

// buildOMM produces a single (non-size-prefixed) $OMM FlatBuffer from a fit
// result — the store's canonical content-addressed form. ORIGINATOR marks the
// record as OUR OD fit (supplemental GP).
func buildOMM(fit *FitResult, source string) []byte {
	b := sds.NewOMMBuilder().
		WithNoradCatID(fit.NoradCatID).
		WithObjectName(fit.ObjectName).
		WithObjectID(fit.ObjectID).
		WithEpoch(fit.Epoch).
		WithMeanMotion(fit.MeanMotion).
		WithEccentricity(fit.Eccentricity).
		WithInclination(fit.Inclination).
		WithRaOfAscNode(fit.RaOfAscNode).
		WithArgOfPericenter(fit.ArgOfPericenter).
		WithMeanAnomaly(fit.MeanAnomaly).
		WithBStar(fit.Bstar).
		WithMeanMotionDot(fit.MeanMotionDot).
		WithMeanMotionDdot(fit.MeanMotionDdot).
		WithElementSetNo(fit.ElementSetNo).
		WithRevAtEpoch(fit.RevAtEpoch).
		WithClassificationType(fit.Classification).
		WithOriginator("SDN-OD").
		WithCreationDate(time.Now().UTC().Format(time.RFC3339))
	if ts, err := time.Parse(time.RFC3339, normalizeEpoch(fit.Epoch)); err == nil {
		b = b.WithEpochTimestamp(float64(ts.Unix()))
	}
	sized := b.Build()
	return sized[4:] // strip 4-byte size prefix -> single FlatBuffer
}

// normalizeEpoch trims fractional seconds beyond RFC3339 tolerance / trailing
// microseconds so an epoch like "2026-07-13T12:00:00.000000Z" parses.
func normalizeEpoch(epoch string) string {
	epoch = strings.TrimSpace(epoch)
	if epoch == "" {
		return epoch
	}
	if !strings.HasSuffix(epoch, "Z") && !strings.Contains(epoch, "+") {
		epoch += "Z"
	}
	return epoch
}

// dedupe returns providers with blanks removed and order-stable de-duplication,
// sorted for deterministic run summaries.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
