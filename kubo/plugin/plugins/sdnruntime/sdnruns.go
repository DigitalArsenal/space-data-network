package sdnruntime

// Supplemental-OMM OD run engine wiring. This file OWNS the supplemental-OMM run
// engine's node-side wiring (the sibling-owned sdnruntime.go only calls
// startSupplementalOMMRuns from a small marked block). On boot it:
//
//   - opens the run store under <repo>/sdn/runs,
//   - builds a CommandFitter that resolves the installed analysis/od
//     (orbit-determination) module's bytes and drives its REAL WASM fit,
//   - registers the run engine as a self-scheduling cron module
//     ("supplemental-omm", default hourly) so it fires on its cadence AND appears
//     at GET /sdn/v1/modules with a home-dir config editable in the Modules UI
//     (enabled providers + reference lanes), and
//   - optionally (SDN_SUPPLEMENTAL_OMM_RUN, a local live-smoke affordance)
//     triggers one run from an embedded real ISS OEM fixture with a seeded
//     CelesTrak SupGP reference, so GET /sdn/v1/runs shows a real recorded run.
//
// The run engine is exposed to the sdnapi plugin's loopback listener through
// Runs() (the same stash-on-Start pattern Services()/Installer() use).

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	core "github.com/ipfs/kubo/core"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/sdncron"
	"github.com/ipfs/kubo/sdn/sdnmodules"
	"github.com/ipfs/kubo/sdn/sdnruns"
	"github.com/ipfs/kubo/sdn/sdnservices"
	"github.com/ipfs/kubo/sdn/sdnstore"
	"github.com/ipfs/kubo/sdn/sds"
)

// odModuleID is the analysis/od module's plugin id (its cron/registry key).
const odModuleID = "orbit-determination"

// issOEMFixture is a real, checked-in trimmed NASA public ISS OEM (CCSDS KVN,
// EME2000/UTC, ~12 h of 4-min position+velocity state vectors, NORAD 25544). It
// stands in for the firewalled operator-ephemeris fetch in the SDN_SUPPLEMENTAL_OMM_RUN
// live-smoke path — the OD fit, OMM production, RMS + reference parity are all real.
//
//go:embed fixtures/iss_oem_trimmed.txt
var issOEMFixture []byte

// ---------------------------------------------------------------------------
// Runs() accessor (stash-on-Start, mirrors Services()/Installer()).
// ---------------------------------------------------------------------------

var (
	runsMu   sync.RWMutex
	liveRuns *sdnruns.Store
)

// Runs returns the live supplemental-OMM run store, or nil when the runtime is
// disabled or has not started yet. The sdnapi plugin reaches it through this.
func Runs() *sdnruns.Store {
	runsMu.RLock()
	defer runsMu.RUnlock()
	return liveRuns
}

func setRuns(s *sdnruns.Store) {
	runsMu.Lock()
	liveRuns = s
	runsMu.Unlock()
}

// startSupplementalOMMRuns builds + registers the supplemental-OMM run engine and
// stashes its store. sdnDir is <repo>/sdn ("" => no-persistence). It is called
// once from sdnruntime.Start's marked block. Errors are logged, never fatal.
func startSupplementalOMMRuns(node *core.IpfsNode, svc *sdnservices.Services, installer *sdnmodules.Installer, sdnDir string) {
	runsDir := ""
	if strings.TrimSpace(sdnDir) != "" {
		runsDir = filepath.Join(sdnDir, "runs")
	}
	store, err := sdnruns.NewStore(runsDir)
	if err != nil {
		log.Warnf("SDN supplemental-OMM run store unavailable: %v", err)
		return
	}
	setRuns(store)

	ctx := node.Context()

	// Fitter over the REAL analysis/od module. It needs the module's raw WASM
	// bytes (a fresh instance per fit — the command surface proc_exits after one
	// request). Resolve, in order: the installed module by its blockstore content
	// hash; else an explicit SDN_OD_MODULE_WASM path; else the SDN_INSTALL_WASM
	// path the operator installed od from (the natural local source). The
	// blockstore is addressed by the raw-artifact hash, so the content-hash lookup
	// only hits when the installed artifact's canonical hash equals its raw hash;
	// the path fallbacks make the run work in every local install case.
	fitter := sdnruns.NewCommandFitter(func() ([]byte, error) {
		if installer != nil && node.Blockstore != nil {
			if mod := installer.Module(odModuleID); mod != nil {
				if b, err := appmanifest.ResolveModuleByContentHash(ctx, node.Blockstore, mod.ContentHash()); err == nil {
					return b, nil
				}
			}
		}
		for _, env := range []string{"SDN_OD_MODULE_WASM", "SDN_INSTALL_WASM"} {
			if p := strings.TrimSpace(os.Getenv(env)); p != "" {
				if b, err := os.ReadFile(p); err == nil && looksLikeODModule(b) {
					return b, nil
				}
			}
		}
		return nil, fmt.Errorf("analysis/od module (%s) is not installed and no od WASM path is configured", odModuleID)
	}, log.Infof)

	runner, err := sdnruns.NewRunner(sdnruns.Config{
		Fitter:  fitter,
		Source:  &embeddedEphemerisSource{},
		Records: svc.Store,
		Runs:    store,
		Resolve: func() sdnruns.RunConfig { return resolveRunConfig(svc.ConfigStore) },
		Log:     log.Infof,
	})
	if err != nil {
		log.Warnf("SDN supplemental-OMM runner unavailable: %v", err)
		return
	}

	// Register as a self-scheduling cron module (default hourly). It then fires on
	// its cadence and appears at GET /sdn/v1/modules with a home-dir config.
	if svc.Scheduler != nil {
		if err := svc.Scheduler.Register(sdncron.Registration{
			Module:  runner,
			Name:    sdnruns.ModuleName,
			Version: sdnruns.ModuleVersion,
		}); err != nil {
			log.Warnf("SDN supplemental-OMM cron registration failed: %v", err)
		} else {
			log.Infof("SDN supplemental-OMM run engine registered (cron module %q, default hourly)", sdnruns.ModuleID)
		}
	}

	// Optional live-smoke trigger (off by default; local affordance like
	// SDN_INSTALL_WASM). Seeds a CelesTrak SupGP reference for the embedded ISS
	// fixture and drives one run so GET /sdn/v1/runs shows a real recorded run.
	if os.Getenv("SDN_SUPPLEMENTAL_OMM_RUN") != "" {
		go func() {
			if n, err := seedSupplementalReferences(ctx, svc.Store); err != nil {
				log.Warnf("SDN supplemental-OMM reference seed failed: %v", err)
			} else {
				log.Infof("SDN supplemental-OMM: seeded %d CelesTrak reference OMM(s) for the live-smoke run", n)
			}
			run, err := runner.RunProviders(ctx, sdnruns.RunConfig{
				EnabledProviders: []string{"iss"},
				CelestrakSource:  "celestrak-supgp",
				SpacetrackSource: "spacetrack",
				ProducedSource:   sdnruns.DefaultProducedSource,
			})
			if err != nil {
				log.Warnf("SDN_SUPPLEMENTAL_OMM_RUN live-smoke run failed: %v", err)
				return
			}
			log.Infof("SDN_SUPPLEMENTAL_OMM_RUN live-smoke run %s: objects=%d avg_rms=%.3f beats=%d",
				run.ID, run.ObjectsDone, run.AvgRMS, run.BeatCount)
		}()
	}
}

// looksLikeODModule cheaply confirms a WASM artifact is the analysis/od module
// (its embedded manifest declares pluginId "orbit-determination") before feeding
// it to the fitter, so an SDN_INSTALL_WASM path pointing at a different module is
// not mistaken for the OD fitter.
func looksLikeODModule(wasm []byte) bool {
	return len(wasm) > 0 && bytes.Contains(wasm, []byte(odModuleID))
}

// resolveRunConfig reads the supplemental-OMM module's home-dir config (edited in
// the Modules UI) into a RunConfig: enabled_providers + reference lane names.
func resolveRunConfig(cs *sdncron.ConfigStore) sdnruns.RunConfig {
	cfg := sdnruns.ConfigDefault()
	if cs == nil {
		return cfg
	}
	raw, err := cs.Load(sdnruns.ModuleID)
	if err != nil || raw == nil {
		return cfg
	}
	if v, ok := raw["enabled_providers"].([]interface{}); ok {
		var providers []string
		for _, p := range v {
			if s, ok := p.(string); ok && strings.TrimSpace(s) != "" {
				providers = append(providers, s)
			}
		}
		if len(providers) > 0 {
			cfg.EnabledProviders = providers
		}
	}
	if s, ok := raw["celestrak_reference_source"].(string); ok && strings.TrimSpace(s) != "" {
		cfg.CelestrakSource = s
	}
	if s, ok := raw["spacetrack_reference_source"].(string); ok && strings.TrimSpace(s) != "" {
		cfg.SpacetrackSource = s
	}
	if s, ok := raw["produced_source"].(string); ok && strings.TrimSpace(s) != "" {
		cfg.ProducedSource = s
	}
	return cfg
}

// embeddedEphemerisSource is the stubbed ephemeris source for the local live
// smoke: it yields the embedded real ISS OEM for provider "iss". The production
// source (invoking the firewalled data-source WASM modules over the http
// capability) is a drop-in EphemerisSource replacing this; the OD fit downstream
// is real regardless.
type embeddedEphemerisSource struct{}

func (embeddedEphemerisSource) Pull(_ context.Context, provider string) ([]sdnruns.Ephemeris, error) {
	if provider != "iss" || len(issOEMFixture) == 0 {
		return nil, nil
	}
	return []sdnruns.Ephemeris{{
		Provider:   "iss",
		Format:     "oem",
		ObjectName: "ISS",
		ObjectID:   "1998-067-A",
		NoradCatID: 25544,
		DataSource: "ISS-E",
		Bytes:      append([]byte(nil), issOEMFixture...),
	}}, nil
}

// seedSupplementalReferences stores the real same-day CelesTrak SupGP ISS
// [Segment 01] element set (NORAD 25544) as the reference lane the live-smoke run
// scores against. Idempotent (content-addressed). It is a reference for
// comparison only — never an input to the fit.
func seedSupplementalReferences(ctx context.Context, store *sdnstore.Store) (int, error) {
	if store == nil {
		return 0, fmt.Errorf("store is nil")
	}
	sized := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS [Segment 01]").
		WithObjectID("1998-067A").
		WithEpoch("2026-07-13T12:00:00.000000").
		WithEpochTimestamp(float64(time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).Unix())).
		WithMeanMotion(15.48986033).
		WithEccentricity(0.0006726).
		WithInclination(51.6300).
		WithRaOfAscNode(169.8722).
		WithArgOfPericenter(293.0452).
		WithMeanAnomaly(20.1755).
		WithBStar(0.00092967).
		WithMeanMotionDot(0.00051371).
		WithOriginator("celestrak-supgp").
		Build()
	if _, err := store.Store(ctx, "celestrak-supgp", "OMM", sized[4:]); err != nil {
		return 0, err
	}
	return 1, nil
}
