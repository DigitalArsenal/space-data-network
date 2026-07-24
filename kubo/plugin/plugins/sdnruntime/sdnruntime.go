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
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	logging "github.com/ipfs/go-log/v2"
	core "github.com/ipfs/kubo/core"
	plugin "github.com/ipfs/kubo/plugin"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/credstore"
	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdnflows"
	"github.com/ipfs/kubo/sdn/sdnmodules"
	"github.com/ipfs/kubo/sdn/sdnservices"
	"github.com/ipfs/kubo/sdn/sdnstore"
)

var log = logging.Logger("plugin/sdnruntime")

const (
	trustedPublisherKeysEnv = "SDN_TRUSTED_PUBLISHER_KEYS"
	// 16384 wasm pages = 1 GiB. This bounds each signed runtime while leaving
	// room for a large frame page, routing copies, and allocator overhead on an
	// 8 GiB node; the same request is passed to independently loaded children.
	isomorphicParentMaxMemoryPages uint32 = 16384
)

type sdnRuntimePlugin struct {
	enabled   bool
	repoPath  string
	hotWindow int
}

type flowInstallerBootFunc func(context.Context, []sdnflows.FlowSpec) (int, error)

func startFlowInstallerBoot(ctx context.Context, boot flowInstallerBootFunc, report func(int, error)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if boot == nil {
			if report != nil {
				report(0, errors.New("flow installer boot function is nil"))
			}
			return
		}
		count, err := boot(ctx, nil)
		if report != nil {
			report(count, err)
		}
	}()
	return done
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
	sdnDir := ""
	if p.repoPath != "" {
		sdnDir = filepath.Join(p.repoPath, "sdn")
		modulesConfigDir = filepath.Join(sdnDir, "modules")
	}

	// Encrypted-at-rest credential keystore (<repo>/sdn/secrets/credentials.enc,
	// 0600). Its root key is derived DETERMINISTICALLY from the node's unlocked
	// libp2p identity private key (core.IpfsNode.PrivateKey.Raw()) + the machine
	// fingerprint + the hostname, so it re-derives unattended on every boot but a
	// copied file will not decrypt on another host/identity. Fail closed: with no
	// identity key, no repo path, or a resolve error, credStore stays nil — the
	// secrets capability is not registered and the credential API reports 503,
	// never a weaker key. The store is stashed for the sdnapi plugin's loopback
	// credential-entry routes (see CredentialStore()).
	var credStore *credstore.Store
	if sdnDir != "" && node.PrivateKey != nil {
		if raw, err := node.PrivateKey.Raw(); err != nil {
			log.Warnf("SDN credential store unavailable: node identity key material inaccessible: %v", err)
		} else if st, err := credstore.OpenStore(sdnDir, raw); err != nil {
			log.Warnf("SDN credential store unavailable: %v", err)
		} else {
			credStore = st
		}
	}
	setCredentialStore(credStore)

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
		// Credential keystore backing the secrets:<id> capability (nil-safe).
		CredStore: credStore,
	}

	svc, err := sdnservices.BuildServices(deps)
	if err != nil {
		return fmt.Errorf("sdnruntime: build services: %w", err)
	}
	signaturePolicy, err := moduleSignaturePolicyFromText(os.Getenv(trustedPublisherKeysEnv))
	if err != nil {
		svc.Close()
		return fmt.Errorf("sdnruntime: %s: %w", trustedPublisherKeysEnv, err)
	}
	// Production loading is fail closed even when the operator has not yet
	// configured a publisher. The same trust roots gate outer flow bundles and
	// every independently instantiated child node.
	svc.NodeCtx.ModuleSignaturePolicy = signaturePolicy
	if len(signaturePolicy.TrustedSigners) == 0 {
		log.Warnf("SDN signed artifact loading is fail-closed: %s contains no trusted publisher keys", trustedPublisherKeysEnv)
	}
	setServices(svc)

	// Real WASM-module install + register pipeline. The installed-modules
	// registry lives alongside the per-module cron config under
	// <repo>/sdn/modules (installed.json), and an optional operator drop-in
	// directory <repo>/sdn/modules/install/*.wasm is scanned at boot. The
	// installer loads each module through svc.LoadModule (fail-closed capability
	// gate, keyed by content hash) and registers the resulting real
	// *modulert.Module with the cron scheduler.
	installer, err := buildInstaller(svc, node.Blockstore, modulesConfigDir)
	if err != nil {
		return fmt.Errorf("sdnruntime: build module installer: %w", err)
	}
	setInstaller(installer)

	// Flow install + register pipeline (sdnflows): loads a compiled bundle with
	// canonical flow.plg topology and registers it as a timer-served
	// flow with the SAME cron scheduler modules use, so a flow both fires its
	// host-cron timer (fetch -> parse -> store) on its cadence and appears at
	// GET /sdn/v1/modules alongside modules. The installed-flows registry lives
	// under <repo>/sdn/flows.
	flowInstaller, err := buildFlowInstaller(svc, sdnDir)
	if err != nil {
		return fmt.Errorf("sdnruntime: build flow installer: %w", err)
	}
	setFlowInstaller(flowInstaller)

	// Optional developer install of a REAL WASM module (off by default; a
	// local/live-smoke affordance that is never enabled in a production config).
	// When SDN_INSTALL_WASM=<path> is set,
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

	// Optional developer install of a REAL compiled flow bundle (off by default;
	// a local/live-smoke affordance, mirroring SDN_INSTALL_WASM — never enabled
	// in a production config). When SDN_INSTALL_FLOW=<bundle dir> is set, the
	// node installs that flow through the sdnflows pipeline: it records DEV
	// operator approvals for the flow's declared sensitive capabilities (so the
	// fail-closed gate admits it), loads + registers the flow with the cron
	// scheduler, and persists it. SDN_FLOW_CONFIG=<json> supplies the flow's node
	// configuration. Timer semantics come from the canonical flow.plg artifact.
	if fp := strings.TrimSpace(os.Getenv("SDN_INSTALL_FLOW")); fp != "" {
		if err := devInstallFlow(node.Context(), flowInstaller, svc, fp); err != nil {
			log.Warnf("SDN_INSTALL_FLOW install failed for %q: %v", fp, err)
		}
	}
	// Re-establish the persisted installed-modules set + install any operator
	// drop-in *.wasm (before the scheduler starts, so every module's timers begin
	// together). Tolerant: a module whose bytes are missing or whose sensitive
	// capabilities are no longer approved is logged and skipped.
	if n, err := installer.Boot(node.Context()); err != nil {
		log.Errorf("SDN BOOT CHECK: module installer boot failed: %v", err)
	} else if n > 0 {
		log.Infof("SDN module installer: re-registered %d installed WASM module(s) at boot", n)
	}
	// Boot check (task sdn-licensing-module-load): a module that failed to
	// load is a unit of the node that is silently NOT running — fail-closed
	// capability denials and missing bytes must be loudly visible, not an
	// INFO "skipping" line. One ERROR per failure, grep-able marker.
	for _, failure := range installer.BootFailures() {
		log.Errorf("SDN BOOT CHECK: MODULE LOAD FAILED (module NOT running, fail closed): id=%q source=%q err=%s", failure.ID, failure.Source, failure.Error)
	}

	// Re-establish persisted flows and scan signed drop-ins without blocking
	// Kubo's HTTP/RPC startup. A signed flow's first activation is deliberately
	// atomic and may process a complete catalog before APP publication, so it
	// runs under the node lifecycle after this plugin returns. The scheduler
	// supports registrations added by the completed boot pass.
	flowBootDone := startFlowInstallerBoot(node.Context(), flowInstaller.Boot, func(n int, err error) {
		if err != nil {
			log.Errorf("SDN BOOT CHECK: flow installer boot failed: %v", err)
		} else if n > 0 {
			log.Infof("SDN flow installer: re-registered %d installed flow(s) at boot", n)
		}
		// Boot check (task sdn-licensing-module-load): every signed bundle /
		// flow that failed to restore is loudly visible — one ERROR per
		// failure, grep-able marker (matches the module installer above).
		for _, failure := range flowInstaller.BootFailures() {
			log.Errorf("SDN BOOT CHECK: FLOW/BUNDLE LOAD FAILED (flow NOT running, fail closed): id=%q source=%q err=%s", failure.ID, failure.Source, failure.Error)
		}
	})
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
		<-flowBootDone        // activation observes node cancellation before services close
		flowInstaller.Close() // close loaded flow runtimes first
		setFlowInstaller(nil)
		installer.Close() // close loaded module runtimes
		setInstaller(nil)
		svc.Close() // stops the scheduler + closes the store engine
		setServices(nil)
	}()

	return nil
}

func (*sdnRuntimePlugin) Close() error { return nil }

func moduleSignaturePolicyFromText(raw string) (*modulert.ModuleSignaturePolicy, error) {
	policy := &modulert.ModuleSignaturePolicy{
		AllowUnsignedByContentHash: make(map[string]bool),
	}
	seen := make(map[string]bool)
	for _, encoded := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) {
		encoded = strings.ToLower(strings.TrimSpace(encoded))
		if encoded == "" || seen[encoded] {
			continue
		}
		decoded, err := hex.DecodeString(encoded)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("trusted publisher key %q must be exactly %d hexadecimal Ed25519 public-key bytes", encoded, ed25519.PublicKeySize)
		}
		seen[encoded] = true
		policy.TrustedSigners = append(policy.TrustedSigners, ed25519.PublicKey(append([]byte(nil), decoded...)))
	}
	return policy, nil
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

// ---------------------------------------------------------------------------
// Package-level accessor for the node's encrypted-at-rest credential keystore.
// The sdnapi plugin reaches it through CredentialStore() to serve the loopback
// credential-entry admin routes (GET/PUT/DELETE /sdn/v1/admin/credentials) —
// the same stash-on-Start pattern Services()/Installer() use. nil when the
// runtime is disabled, not started, or the keystore could not be opened
// (fail closed: the credential routes then report 503).
// ---------------------------------------------------------------------------

var (
	credStoreMu    sync.RWMutex
	liveCredential *credstore.Store
)

// CredentialStore returns the live credential keystore, or nil when unavailable.
func CredentialStore() *credstore.Store {
	credStoreMu.RLock()
	defer credStoreMu.RUnlock()
	return liveCredential
}

func setCredentialStore(s *credstore.Store) {
	credStoreMu.Lock()
	liveCredential = s
	credStoreMu.Unlock()
}

// ---------------------------------------------------------------------------
// Package-level accessor for the live FLOW installer (the sdnflows pipeline).
// Mirrors Installer()/Services(): stashed on Start so a later API/admin surface
// can reach it.
// ---------------------------------------------------------------------------

var (
	flowInstallerMu   sync.RWMutex
	liveFlowInstaller *sdnflows.Installer
)

// FlowInstaller returns the live flow installer, or nil if the runtime plugin
// is disabled or has not started yet.
func FlowInstaller() *sdnflows.Installer {
	flowInstallerMu.RLock()
	defer flowInstallerMu.RUnlock()
	return liveFlowInstaller
}

func setFlowInstaller(in *sdnflows.Installer) {
	flowInstallerMu.Lock()
	liveFlowInstaller = in
	flowInstallerMu.Unlock()
}

// buildFlowInstaller constructs the flow install pipeline over the live
// services and the <repo>/sdn/flows registry directory. sdnDir may be "" (no
// repo path), in which case the registry is in no-persistence mode.
func buildFlowInstaller(svc *sdnservices.Services, sdnDir string) (*sdnflows.Installer, error) {
	flowsDir := ""
	dropinDir := ""
	if sdnDir != "" {
		flowsDir = filepath.Join(sdnDir, "flows")
		dropinDir = filepath.Join(flowsDir, "install")
	}
	registry, err := sdnflows.NewRegistry(flowsDir)
	if err != nil {
		return nil, err
	}
	var trustedSigners []ed25519.PublicKey
	if svc != nil && svc.NodeCtx != nil && svc.NodeCtx.ModuleSignaturePolicy != nil {
		trustedSigners = svc.NodeCtx.ModuleSignaturePolicy.TrustedSigners
	}
	return sdnflows.New(sdnflows.Config{
		Services:       svc,
		Registry:       registry,
		MaxMemoryPages: isomorphicParentMaxMemoryPages,
		DropinDir:      dropinDir,
		TrustedSigners: trustedSigners,
		Log:            log.Infof,
	})
}

// devInstallFlow is the SDN_INSTALL_FLOW boot affordance: it reads a compiled
// flow bundle from disk, records DEV operator approvals for its declared
// sensitive capabilities (so the fail-closed gate admits it), and installs +
// registers it through the pipeline. SDN_FLOW_CONFIG (JSON) is the flow's node
// CONFIG (URL overrides etc.). Local/live-smoke only.
func devInstallFlow(ctx context.Context, installer *sdnflows.Installer, svc *sdnservices.Services, bundleDir string) error {
	raw, err := os.ReadFile(filepath.Join(bundleDir, "runtime.wasm"))
	if err != nil {
		return fmt.Errorf("read flow runtime.wasm: %w", err)
	}
	portable, _, err := modulert.EnforceModuleSignaturePolicy(nil, raw)
	if err != nil {
		return fmt.Errorf("strip flow trailer: %w", err)
	}
	hash := modulert.ContentHashHex(portable)
	if svc.NodeCtx != nil && svc.NodeCtx.CapabilityPolicy != nil {
		for _, cap := range devSensitiveCapabilities {
			if _, err := svc.NodeCtx.CapabilityPolicy.Approve(modulert.CapabilityApproval{
				ModuleHash: hash,
				Capability: cap,
				ApprovedBy: "dev:SDN_INSTALL_FLOW",
				Note:       "auto-approved by SDN_INSTALL_FLOW developer affordance (local only)",
			}); err != nil {
				return fmt.Errorf("dev-approve %s: %w", cap, err)
			}
		}
		log.Warnf("SDN_INSTALL_FLOW (DEV, local only): auto-approved sensitive capabilities for flow hash %s… — this MUST NOT be used in production", hash[:12])
	}

	spec := sdnflows.FlowSpec{Ref: bundleDir}
	if cfg := strings.TrimSpace(os.Getenv("SDN_FLOW_CONFIG")); cfg != "" {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(cfg), &m); err != nil {
			return fmt.Errorf("SDN_FLOW_CONFIG is not valid JSON: %w", err)
		}
		spec.Config = m
	}
	f, err := installer.Install(spec, "dev:SDN_INSTALL_FLOW")
	if err != nil {
		return err
	}
	log.Infof("SDN_INSTALL_FLOW: installed + registered flow id=%q hash=%s… timers=%v", f.ID, hash[:12], f.Timers)
	return nil
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
		switch t {
		case "OMM":
			return ommSchema, "$OMM", "OMM", true
		case "SPW":
			// Space weather records. A later phase replaces this per-type shipping
			// with the full SDS
			// registry; the wiring (Deps.Schemas) does not change.
			return spwSchema, "$SPW", "SPW", true
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

// spwSchema is the Space Weather (SPW) FlatSQL table shape. Enums are included
// because the SPW table references F107DataType.
const spwSchema = `
  enum FluxQualifier: byte { OBSERVED = 0, BURST_ADJUSTED = 1, INTERPOLATED_EXTRAPOLATED = 2, NO_OBSERVATION = 3, CELESTRAK_INTERPOLATED = 4 }
  enum F107DataType: byte { OBS = 0, INT = 1, PRD = 2, PRM = 3 }
  table SPW {
    DATE: string;
    BSRN: int;
    ND: int;
    KP1: int; KP2: int; KP3: int; KP4: int; KP5: int; KP6: int; KP7: int; KP8: int;
    KP_SUM: int;
    AP1: int; AP2: int; AP3: int; AP4: int; AP5: int; AP6: int; AP7: int; AP8: int;
    AP_AVG: int;
    CP: float;
    C9: int;
    ISN: int;
    F107_OBS: float;
    F107_ADJ: float;
    F107_DATA_TYPE: F107DataType;
    F107_OBS_CENTER81: float;
    F107_OBS_LAST81: float;
    F107_ADJ_CENTER81: float;
    F107_ADJ_LAST81: float;
  }
  root_type SPW;
  file_identifier "$SPW";
`
