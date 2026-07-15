// Package sdnruntime is the Space Data Network module-runtime plugin: the
// second of the two SDN additions to upstream kubo (the other being sdnflag).
// It mounts the SDN services stack into a running kubo node — Phase 6 of the
// kubo rebase.
//
// On daemon start it constructs, from the node's own blockstore, datastore and
// (optional) gossipsub, a live SDN services bundle (sdnservices.BuildServices):
// the durable (source, type) record store (sdnstore), the per-(provider,
// standard) channel fan-out (channels), and a WASM module capability registry
// whose storage_* and pubsub factories target those two services. A WASM module
// loaded through this runtime therefore reads/writes the node's record store and
// publishes/subscribes its channels — but only for the capabilities an operator
// has approved for that module's content hash (fail closed).
//
// Enabled by default (like sdnflag): the module runtime is a reason this fork
// exists. Set Plugins.sdnruntime.Config.Enabled=false to opt out.
//
// This plugin adds NO kubo core patch: it reads only existing *core.IpfsNode
// fields (Blockstore, Repo.Datastore(), PubSub, Identity) — PubSub being the
// optional, config-gated gossipsub instance. When PubSub is nil (kubo
// Pubsub.Enabled=false) it logs a warning and runs storage-only rather than
// crashing.
package sdnruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	logging "github.com/ipfs/go-log/v2"
	core "github.com/ipfs/kubo/core"
	plugin "github.com/ipfs/kubo/plugin"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/plugins"
	"github.com/ipfs/kubo/sdn/sdnapps"
	"github.com/ipfs/kubo/sdn/sdncron"
	"github.com/ipfs/kubo/sdn/sdnmodules"
	"github.com/ipfs/kubo/sdn/sdnservices"
	"github.com/ipfs/kubo/sdn/sdnstore"
	"github.com/ipfs/kubo/sdn/sds"
)

var log = logging.Logger("plugin/sdnruntime")

type sdnRuntimePlugin struct {
	enabled   bool
	repoPath  string
	hotWindow int
}

var _ plugin.PluginDaemonInternal = (*sdnRuntimePlugin)(nil)

// Plugins is the exported list of plugins that will be loaded.
var Plugins = []plugin.Plugin{
	&sdnRuntimePlugin{},
}

func (*sdnRuntimePlugin) Name() string    { return "sdnruntime" }
func (*sdnRuntimePlugin) Version() string { return "0.1.0" }

// Init reads optional config. Enabled by default (see package doc). Set
// Plugins.sdnruntime.Config.Enabled=false to opt out, or .HotWindow to override
// the FlatSQL query-cache window.
func (p *sdnRuntimePlugin) Init(env *plugin.Environment) error {
	p.enabled = true
	if env != nil {
		p.repoPath = env.Repo
		if cfg, ok := env.Config.(map[string]interface{}); ok {
			if v, ok := cfg["Enabled"].(bool); ok {
				p.enabled = v
			}
			if v, ok := cfg["HotWindow"].(float64); ok && v > 0 {
				p.hotWindow = int(v)
			}
		}
	}
	return nil
}

func (p *sdnRuntimePlugin) Start(node *core.IpfsNode) error {
	if !p.enabled {
		return nil
	}
	if err := logging.SetLogLevel("plugin/sdnruntime", "info"); err != nil {
		return fmt.Errorf("failed to set log level: %w", err)
	}

	// Operator capability allowlist, persisted under the repo (fail closed:
	// a missing file is an empty policy — every sensitive capability denied
	// until an operator records an approval keyed by module content hash).
	// With no repo path (unusual) the store is in-memory default-deny.
	policyPath := ""
	if p.repoPath != "" {
		sdnDir := filepath.Join(p.repoPath, "sdn")
		_ = os.MkdirAll(sdnDir, 0o700)
		policyPath = filepath.Join(sdnDir, "capability_policy.json")
	}
	policy, err := modulert.NewCapabilityPolicyStore(policyPath)
	if err != nil {
		return fmt.Errorf("sdnruntime: open capability policy: %w", err)
	}

	// FlatSQL AOT cache under the repo so the engine loads at native speed.
	var runtimeOpts []flatsqlrt.Option
	if p.repoPath != "" {
		aotDir := filepath.Join(p.repoPath, "sdn", "flatsql-aot")
		_ = os.MkdirAll(aotDir, 0o700)
		runtimeOpts = append(runtimeOpts, flatsqlrt.WithAOTCache(aotDir))
	}

	// Per-module cron configuration lives under the repo home dir so a module's
	// user-tunable schedule (and other inputs) survive restarts:
	// <repo>/sdn/modules/<moduleId>.json (0600, atomic writes).
	modulesConfigDir := ""
	if p.repoPath != "" {
		modulesConfigDir = filepath.Join(p.repoPath, "sdn", "modules")
	}

	deps := sdnservices.Deps{
		Blockstore:       node.Blockstore,
		Datastore:        node.Repo.Datastore(),
		PubSub:           node.PubSub, // optional; nil => storage-only
		Schemas:          defaultSchemas(),
		HotWindow:        p.hotWindow,
		RuntimeOptions:   runtimeOpts,
		Policy:           policy,
		PeerID:           node.Identity.String(),
		FallbackSource:   node.Identity.String(),
		ModulesConfigDir: modulesConfigDir,
		CronLog:          log.Infof,
	}

	svc, err := sdnservices.BuildServices(deps)
	if err != nil {
		return fmt.Errorf("sdnruntime: build services: %w", err)
	}
	setServices(svc)

	// Real WASM-module install + register pipeline. The installed-modules
	// registry lives alongside the per-module cron config under
	// <repo>/sdn/modules (installed.json), and an optional operator drop-in
	// directory <repo>/sdn/modules/install/*.wasm is scanned at boot. The
	// installer loads each module through svc.LoadModule (fail-closed capability
	// gate, keyed by content hash) and registers the resulting real
	// *modulert.Module with the cron scheduler — closing the gap the cron demo
	// left (a NATIVE heartbeat stub).
	installer, err := buildInstaller(svc, node.Blockstore, modulesConfigDir)
	if err != nil {
		return fmt.Errorf("sdnruntime: build module installer: %w", err)
	}
	setInstaller(installer)

	// Install the SDN apps program's $APP records (Supplemental OMM + Conjunction)
	// into the node's record store so the node lists them at GET /sdn/v1/apps and
	// serves each app's inline UI at GET /sdn/v1/apps/<id>. Idempotent: the store
	// keys records by content, so re-seeding on a later boot is a no-op.
	if n, err := sdnapps.Seed(node.Context(), svc.Store); err != nil {
		log.Warnf("SDN apps seed failed: %v", err)
	} else {
		log.Infof("SDN apps installed: %d $APP record(s) under (source=%q, type=%q)", n, sdnapps.Source, sdnapps.SDSType)
	}

	// Optional developer OMM seed (off by default). When SDN_DEV_SEED_OMM is set,
	// a handful of real OMM FlatBuffers are stored under a couple of provider
	// lanes so the Supplemental OMM board has live records to render on a fresh
	// isolated repo. This is a local dev/verification affordance only — never
	// enabled in a production config.
	if os.Getenv("SDN_DEV_SEED_OMM") != "" {
		if n, err := seedDevOMM(node.Context(), svc.Store); err != nil {
			log.Warnf("SDN dev OMM seed failed: %v", err)
		} else {
			log.Infof("SDN dev OMM seed: stored %d OMM record(s) (SDN_DEV_SEED_OMM set)", n)
		}
	}

	// Optional developer install of a REAL WASM module (off by default; a
	// local/live-smoke affordance, mirroring SDN_DEV_SEED_OMM / SDN_CRON_DEMO —
	// never enabled in a production config). When SDN_INSTALL_WASM=<path> is set,
	// the node installs that real module-sdk .wasm through the pipeline: it
	// records DEV operator approvals for the module's declared sensitive
	// capabilities (so the fail-closed gate admits it), stores the bytes in the
	// blockstore, loads + registers the real module with the cron scheduler, and
	// persists it to the installed-modules registry. This is the live evidence
	// that a real WASM module (not a native stub) rides the cron path.
	if p := strings.TrimSpace(os.Getenv("SDN_INSTALL_WASM")); p != "" {
		if err := devInstallWasm(node.Context(), installer, svc, p); err != nil {
			log.Warnf("SDN_INSTALL_WASM install failed for %q: %v", p, err)
		}
	}

	// Optional demo cron module (off by default; mirrors SDN_DEV_SEED_OMM). When
	// SDN_CRON_DEMO is set, register a native heartbeat CronModule so a fresh
	// node has a registered, self-scheduling module to observe on GET
	// /sdn/v1/modules and drive from the settings API — the live evidence for
	// the cron scheduler foundation. Never enabled in a production config.
	if svc.Scheduler != nil && os.Getenv("SDN_CRON_DEMO") != "" {
		if err := svc.Scheduler.Register(sdncron.Registration{
			Module:  newDemoCronModule("cron-demo"),
			Name:    "Cron Demo (heartbeat)",
			Version: "0.1.0",
		}); err != nil {
			log.Warnf("SDN cron demo module registration failed: %v", err)
		} else {
			log.Infof("SDN cron demo module registered (SDN_CRON_DEMO set): id=cron-demo timer=heartbeat")
		}
	}

	// Re-establish the persisted installed-modules set + install any operator
	// drop-in *.wasm (before the scheduler starts, so every module's timers begin
	// together). Tolerant: a module whose bytes are missing or whose sensitive
	// capabilities are no longer approved is logged and skipped.
	if n, err := installer.Boot(node.Context()); err != nil {
		log.Warnf("SDN module installer boot failed: %v", err)
	} else if n > 0 {
		log.Infof("SDN module installer: re-registered %d installed WASM module(s) at boot", n)
	}

	// Start the cron scheduler after modules are registered; it fires each
	// registered module's timers on their effective interval (config override,
	// else manifest default) and stops when the node context is cancelled
	// (svc.Close() below, on node ctx Done, calls Scheduler.Stop()).
	if svc.Scheduler != nil {
		svc.Scheduler.Start(node.Context())
		mods, timers := svc.Scheduler.Summary()
		log.Infof("SDN cron scheduler started: %d module(s), %d timer(s); per-module config dir=%q", mods, timers, modulesConfigDir)
	}

	if node.PubSub == nil {
		log.Warnf("SDN runtime active (STORAGE-ONLY): node pubsub is disabled (Pubsub.Enabled=false) — channel fan-out and the pubsub module capability are unavailable; peer=%s", node.Identity)
	} else {
		log.Infof("SDN runtime active: storage + channel fan-out wired; module runtime capability-gated by %q; peer=%s", policyPath, node.Identity)
	}

	// Release the store engine + module runtimes when the node shuts down (the
	// durable blockstore/datastore are the node's and are left untouched).
	go func() {
		<-node.Context().Done()
		installer.Close() // close loaded module runtimes first
		setInstaller(nil)
		svc.Close() // stops the scheduler + closes the store engine
		setServices(nil)
	}()

	return nil
}

func (*sdnRuntimePlugin) Close() error { return nil }

// seedDevOMM stores a small set of real OMM FlatBuffers under two provider lanes
// (celestrak-gp, spacex) so a fresh isolated repo has live OMM records for the
// Supplemental OMM board to render. Gated behind SDN_DEV_SEED_OMM by the caller;
// it uses the sds OMM builder (the same fixture the storage tests use) and the
// store's normal Store path (OMM has a registered FlatSQL schema). Idempotent:
// byte-identical records dedup by content.
func seedDevOMM(ctx context.Context, store *sdnstore.Store) (int, error) {
	if store == nil {
		return 0, fmt.Errorf("sdnruntime: store is nil")
	}
	type sat struct {
		norad uint32
		name  string
		epoch string
		mm    float64
		inc   float64
	}
	lanes := map[string][]sat{
		"celestrak-gp": {
			{25544, "ISS (ZARYA)", "2026-07-15T00:00:00Z", 15.50, 51.64},
			{20580, "HST", "2026-07-15T00:00:00Z", 15.09, 28.47},
			{33591, "NOAA 19", "2026-07-15T00:00:00Z", 14.13, 99.19},
		},
		"spacex": {
			{44713, "STARLINK-1007", "2026-07-15T00:00:00Z", 15.06, 53.05},
			{44714, "STARLINK-1008", "2026-07-15T00:00:00Z", 15.06, 53.05},
		},
	}
	n := 0
	for source, sats := range lanes {
		for _, s := range sats {
			sized := sds.NewOMMBuilder().
				WithNoradCatID(s.norad).
				WithObjectName(s.name).
				WithObjectID(fmt.Sprintf("2024-%03dA", s.norad%1000)).
				WithEpoch(s.epoch).
				WithMeanMotion(s.mm).
				WithEccentricity(0.0001).
				WithInclination(s.inc).
				WithOriginator(source).
				Build()
			// sds builds a size-prefixed OMM; sdnstore.Store takes a single
			// FlatBuffer without the 4-byte size prefix.
			if _, err := store.Store(ctx, source, "OMM", sized[4:]); err != nil {
				return n, fmt.Errorf("sdnruntime: seed OMM %s/%d: %w", source, s.norad, err)
			}
			n++
		}
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Package-level accessor for the live SDN services.
//
// Later phases (the API surface, admin commands) reach the node's live SDN
// services through Services(). A package-level singleton is intentional for
// now: kubo's plugin API hands a plugin no way to publish a value back to the
// node/API layer, so the plugin stashes the built services here on Start.
// ---------------------------------------------------------------------------

var (
	servicesMu   sync.RWMutex
	liveServices *sdnservices.Services
)

// Services returns the live SDN services bundle, or nil if the runtime plugin
// is disabled or has not started yet.
func Services() *sdnservices.Services {
	servicesMu.RLock()
	defer servicesMu.RUnlock()
	return liveServices
}

func setServices(s *sdnservices.Services) {
	servicesMu.Lock()
	liveServices = s
	servicesMu.Unlock()
}

// ---------------------------------------------------------------------------
// Package-level accessor for the live module installer (the real WASM-module
// install + register pipeline). The sdnapi plugin reaches it through Installer()
// to serve POST /sdn/v1/admin/modules/install on its loopback listener — the
// same stash-on-Start pattern Services() uses.
// ---------------------------------------------------------------------------

var (
	installerMu   sync.RWMutex
	liveInstaller *sdnmodules.Installer
)

// Installer returns the live module installer, or nil if the runtime plugin is
// disabled or has not started yet.
func Installer() *sdnmodules.Installer {
	installerMu.RLock()
	defer installerMu.RUnlock()
	return liveInstaller
}

func setInstaller(in *sdnmodules.Installer) {
	installerMu.Lock()
	liveInstaller = in
	installerMu.Unlock()
}

// buildInstaller constructs the module install pipeline over the live services,
// the node blockstore, and the <repo>/sdn/modules registry directory (with an
// install/ drop-in subdirectory). modulesConfigDir may be "" (no repo path), in
// which case the registry is in no-persistence mode.
func buildInstaller(svc *sdnservices.Services, bs appmanifest.ModuleBlockstore, modulesConfigDir string) (*sdnmodules.Installer, error) {
	registry, err := sdnmodules.NewRegistry(modulesConfigDir)
	if err != nil {
		return nil, err
	}
	dropinDir := ""
	if modulesConfigDir != "" {
		dropinDir = filepath.Join(modulesConfigDir, "install")
	}
	return sdnmodules.New(sdnmodules.Config{
		Services:   svc,
		Blockstore: bs,
		Registry:   registry,
		DropinDir:  dropinDir,
		Log:        log.Infof,
	})
}

// devSensitiveCapabilities is the fixed set of sensitive capabilities the
// SDN_INSTALL_WASM developer affordance auto-approves for the installed module's
// content hash so the fail-closed gate admits a real data-source/analysis module
// locally. It is the union of sensitive capabilities the bundled real modules
// declare; a module requesting a sensitive capability outside this set is still
// refused (fail closed holds even for the dev path). NEVER used in production —
// the whole SDN_INSTALL_WASM path is env-gated and off by default.
var devSensitiveCapabilities = []string{
	"http", "storage_query", "storage_write", "storage_adapter", "storage_ingest",
	"wallet_sign", "pubsub", "schedule_cron", "ipfs", "protocol_dial",
}

// devInstallWasm is the SDN_INSTALL_WASM boot affordance: it reads a real
// module-sdk .wasm from disk, records DEV operator approvals for its declared
// sensitive capabilities (so the fail-closed gate admits it), and installs +
// registers it through the pipeline. Local/live-smoke only.
func devInstallWasm(ctx context.Context, installer *sdnmodules.Installer, svc *sdnservices.Services, path string) error {
	wasm, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read wasm: %w", err)
	}
	hash := modulert.ContentHashHex(wasm)
	if svc.NodeCtx != nil && svc.NodeCtx.CapabilityPolicy != nil {
		for _, cap := range devSensitiveCapabilities {
			if _, err := svc.NodeCtx.CapabilityPolicy.Approve(modulert.CapabilityApproval{
				ModuleHash: hash,
				Capability: cap,
				ApprovedBy: "dev:SDN_INSTALL_WASM",
				Note:       "auto-approved by SDN_INSTALL_WASM developer affordance (local only)",
			}); err != nil {
				return fmt.Errorf("dev-approve %s: %w", cap, err)
			}
		}
		log.Warnf("SDN_INSTALL_WASM (DEV, local only): auto-approved sensitive capabilities for module hash %s… — this MUST NOT be used in production", hash[:12])
	}
	m, err := installer.InstallBytes(ctx, wasm, "dev:SDN_INSTALL_WASM")
	if err != nil {
		return err
	}
	log.Infof("SDN_INSTALL_WASM: installed + registered real WASM module id=%q hash=%s… timers=%v", m.ID, hash[:12], m.Timers)
	return nil
}

// ---------------------------------------------------------------------------
// Demo cron module (SDN_CRON_DEMO).
//
// A native sdncron.CronModule used as the live-evidence module for the cron
// scheduler foundation: it declares one "heartbeat" timer (default 15s) and, on
// each scheduled fire, logs a heartbeat and returns a small JSON result. It has
// no capabilities and touches nothing, so it is safe to register on any node —
// but it is gated behind SDN_CRON_DEMO so production nodes stay clean. A real
// WASM module (modulert.Module) plugs into the same scheduler seam unchanged.
// ---------------------------------------------------------------------------

type demoCronModule struct {
	id    string
	count atomic.Int64
}

func newDemoCronModule(id string) *demoCronModule { return &demoCronModule{id: id} }

func (d *demoCronModule) ID() string { return d.id }

func (d *demoCronModule) CronMethods() []plugins.CronMethodSpec {
	return []plugins.CronMethodSpec{{
		Method:          "heartbeat",
		Description:     "Demo heartbeat: proves the SDN cron scheduler fires registered module timers.",
		DefaultInterval: "15s",
		Input:           "none",
		Output:          "json",
	}}
}

func (d *demoCronModule) InvokeCron(_ context.Context, method string, _ []byte) ([]byte, error) {
	n := d.count.Add(1)
	log.Infof("SDN cron demo module %q fired: method=%s count=%d", d.id, method, n)
	return []byte(fmt.Sprintf(`{"ok":true,"module":%q,"method":%q,"count":%d}`, d.id, method, n)), nil
}

// ---------------------------------------------------------------------------
// Built-in schema provider (Phase-6 placeholder).
//
// sdnstore is SDS-type-neutral: it embeds no schemas and stores any 3-letter
// type its SchemaProvider knows. The full SDS schema registry is not yet
// rebased onto kubo, so this ships the one canonical OMM table shape so the
// node has real (source, OMM) storage on boot. A later phase replaces this with
// the SDS registry; the wiring (Deps.Schemas) does not change.
// ---------------------------------------------------------------------------

func defaultSchemas() sdnstore.SchemaProvider {
	return sdnstore.SchemaProviderFunc(func(t string) (schema, fileID, tableName string, ok bool) {
		if t == "OMM" {
			return ommSchema, "$OMM", "OMM", true
		}
		return "", "", "", false
	})
}

// Epoch-ordered hot windows are deferred to the SDS-registry phase: it will
// supply a real EpochExtractor alongside the real schema provider. Until then
// Deps.EpochOf is left nil and sdnstore orders each hot window by monotonic
// store sequence — a correct (if less epoch-aware) ordering.

const ommSchema = `
  table OMM {
    CCSDS_OMM_VERS:double;
    CREATION_DATE:string;
    ORIGINATOR:string;
    OBJECT_NAME:string;
    OBJECT_ID:string;
    CENTER_NAME:string;
    REFERENCE_FRAME:RFM;
    REFERENCE_FRAME_EPOCH:string;
    TIME_SYSTEM:timingStandard = UTC;
    MEAN_ELEMENT_THEORY:meanElementSource = SGP4;
    COMMENT:string;
    EPOCH:string;
    SEMI_MAJOR_AXIS:double;
    MEAN_MOTION:double;
    ECCENTRICITY:double;
    INCLINATION:double;
    RA_OF_ASC_NODE:double;
    ARG_OF_PERICENTER:double;
    MEAN_ANOMALY:double;
    GM:double;
    MASS:double;
    SOLAR_RAD_AREA:double;
    SOLAR_RAD_COEFF:double;
    DRAG_AREA:double;
    DRAG_COEFF:double;
    EPHEMERIS_TYPE:ephemerisFormat = SGP4;
    CLASSIFICATION_TYPE:string;
    NORAD_CAT_ID:uint32;
    ELEMENT_SET_NO:uint32;
    REV_AT_EPOCH:double;
    BSTAR:double;
    MEAN_MOTION_DOT:double;
    MEAN_MOTION_DDOT:double;
    COV_REFERENCE_FRAME:RFM;
    COVARIANCE:[double];
    USER_DEFINED_BIP_0044_TYPE:uint;
    USER_DEFINED_OBJECT_DESIGNATOR:string;
    USER_DEFINED_EARTH_MODEL:string;
    USER_DEFINED_EPOCH_TIMESTAMP: double;
    USER_DEFINED_MICROSECONDS: double;
  }
  root_type OMM;
  file_identifier "$OMM";
`
