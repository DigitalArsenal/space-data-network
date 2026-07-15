// Package sdnservices wires the proven SDN libraries (sdnstore, channels,
// modulert) into one live "SDN services" bundle and reconnects the deferred
// module capabilities (storage, pubsub) to those services — Phase 6 of the
// kubo rebase.
//
// The single entry point is BuildServices(Deps) (*Services, error). Both the
// sdnruntime kubo plugin (over a real *core.IpfsNode) and the integration test
// (over an in-memory blockstore/datastore and a real go-libp2p-pubsub) call it,
// so the test exercises the SAME wiring the plugin runs — not a parallel path.
//
// What BuildServices wires:
//
//   - sdnstore.Store over the given blockstore + datastore (the durable
//     content-addressed record store keyed by (source, 3-letter type));
//   - channels.Channels over the given *pubsub.PubSub (per-(provider, standard)
//     gossipsub fan-out), with sdnstore.Config.OnStore = channels.Publisher()
//     so every newly stored record fans out on its channel (the Phase-4 path);
//   - a modulert capability registry whose storage_* and pubsub factories
//     target the two services above, plus a NodeContext carrying the operator
//     capability policy;
//   - LoadModule, which loads a WASM module through modulert with that registry
//     and NodeContext, so a module's declared storage/pubsub capabilities are
//     provisioned against these services — but only after modulert's operator
//     capability-policy gate approves the module's content hash (fail closed).
//
// PubSub is optional. When Deps.PubSub is nil the bundle is storage-only: the
// store has no fan-out hook and the pubsub capability is not registered (a
// module declaring it fails to provision, as the host cannot satisfy it). This
// mirrors kubo's Pubsub.Enabled gate — the plugin runs storage-only rather than
// crashing when pubsub is disabled.
package sdnservices

import (
	"errors"
	"fmt"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/ipfs/kubo/sdn/channels"
	"github.com/ipfs/kubo/sdn/credstore"
	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdncron"
	"github.com/ipfs/kubo/sdn/sdnstore"
)

// Deps are the raw inputs BuildServices wires together. The plugin fills these
// from a *core.IpfsNode; the test fills them from in-memory/localhost fixtures.
type Deps struct {
	// Blockstore is the durable content-addressed record store
	// (core.IpfsNode.Blockstore). Required.
	Blockstore blockstore.Blockstore
	// Datastore is the durable index/catalog keyspace
	// (core.IpfsNode.Repo.Datastore()). Required. sdnstore namespaces its keys.
	Datastore ds.Datastore
	// PubSub is the node's gossipsub instance (core.IpfsNode.PubSub). Optional:
	// nil => storage-only, no fan-out, no pubsub capability.
	PubSub *pubsub.PubSub
	// Schemas resolves SDS 3-letter types to FlatSQL schemas. Required.
	Schemas sdnstore.SchemaProvider
	// EpochOf optionally orders each type's hot window by record epoch.
	EpochOf sdnstore.EpochExtractor
	// HotWindow caps the per-(source,type) FlatSQL query-cache window. 0 =>
	// sdnstore default.
	HotWindow int
	// RuntimeOptions are passed through to every FlatSQL engine (e.g. an AOT
	// cache dir).
	RuntimeOptions []flatsqlrt.Option

	// Policy is the operator capability allowlist consulted at module load,
	// keyed by module content hash (fail closed). nil => default deny.
	Policy *modulert.CapabilityPolicyStore
	// PeerID / PublicKeyHex / Config populate the module NodeContext's
	// node-identity hostcalls. Optional.
	PeerID       string
	PublicKeyHex string
	Config       map[string]interface{}
	// FallbackSource attributes a storage write whose payload omits "source"
	// and whose module id is unavailable. Optional.
	FallbackSource string

	// ModulesConfigDir is the home-directory folder where per-module cron
	// configuration is persisted (<repo>/sdn/modules). Optional: an empty dir
	// puts the config store in no-persistence mode (schedules run on manifest
	// defaults and do not survive a restart).
	ModulesConfigDir string
	// FetchLedgerDir is the home-directory folder the http cap persists its
	// CelesTrak/Space-Track fetch ledger under (owner rule: <Repo.Path>/sdn/).
	// Optional: empty => in-memory ledger (spacing + within-process 3h TTL
	// still enforced, but the TTL does not survive a restart).
	FetchLedgerDir string
	// CronLog is an optional printf-style sink for the cron scheduler.
	CronLog sdncron.Logger

	// CredStore is the node's encrypted-at-rest credential keystore
	// (sdn/credstore), opened by the plugin from the node's unlocked libp2p
	// identity key. When non-nil, the secrets:<id> capability is registered for
	// every credstore.AllIDs lane so an operator-approved data-source module
	// (Space-Track etc.) can fetch its credential through the "secrets" hostcall.
	// Optional: nil => the secrets capability is not registered (a module
	// declaring it fails to provision, as the host cannot satisfy it).
	CredStore *credstore.Store
}

// Services is the live SDN services bundle a node holds.
type Services struct {
	// Store is the durable (source, type) record store.
	Store *sdnstore.Store
	// Channels is the per-(provider, standard) fan-out. Nil when PubSub was nil
	// (storage-only mode).
	Channels *channels.Channels
	// CapReg is the module capability registry whose storage_*/pubsub factories
	// target Store/Channels. LoadModule provisions declared capabilities from it.
	CapReg *modulert.CapabilityRegistry
	// NodeCtx carries the operator capability policy and node identity into
	// every module loaded via LoadModule.
	NodeCtx *modulert.NodeContext
	// Scheduler fires each registered module's timers on their effective
	// interval and is the target of both the schedule_cron capability and the
	// settings API. Start it after registering modules; Stop on node shutdown.
	Scheduler *sdncron.Scheduler
	// ConfigStore persists per-module cron configuration under the home dir.
	ConfigStore *sdncron.ConfigStore
}

// A real WASM module (modulert.Module) must satisfy the cron scheduler seam so
// it can be registered and fired on its manifest timers exactly like the test
// double — this static assertion fails the build if that ever drifts.
var _ sdncron.CronModule = (*modulert.Module)(nil)

// storageCapabilityNames are the four storage_* manifest capabilities that all
// route to the single "storage" hostcall handler. The factory is registered
// under each so whichever a module declares resolves to the handler; the
// handler enforces the specific grant per operation (see storage_cap.go).
var storageCapabilityNames = []string{
	"storage_query",
	"storage_write",
	"storage_adapter",
	"storage_ingest",
}

// BuildServices wires sdnstore + channels + a module capability registry into a
// live Services bundle. See the package doc for the full contract.
func BuildServices(deps Deps) (*Services, error) {
	if deps.Blockstore == nil {
		return nil, errors.New("sdnservices: Deps.Blockstore is required")
	}
	if deps.Datastore == nil {
		return nil, errors.New("sdnservices: Deps.Datastore is required")
	}
	if deps.Schemas == nil {
		return nil, errors.New("sdnservices: Deps.Schemas is required")
	}

	nodeCtx := &modulert.NodeContext{
		PeerID:           deps.PeerID,
		PublicKeyHex:     deps.PublicKeyHex,
		Config:           deps.Config,
		CapabilityPolicy: deps.Policy,
	}

	// Optional pubsub fan-out. When enabled, wire the store's OnStore hook to
	// the channels publisher — the Phase-4 store->fan-out path.
	var ch *channels.Channels
	cfg := sdnstore.Config{
		Blockstore:     deps.Blockstore,
		Datastore:      deps.Datastore,
		Schemas:        deps.Schemas,
		EpochOf:        deps.EpochOf,
		HotWindow:      deps.HotWindow,
		RuntimeOptions: deps.RuntimeOptions,
	}
	if deps.PubSub != nil {
		ch = channels.New(deps.PubSub)
		cfg.OnStore = ch.Publisher()
	}

	store, err := sdnstore.Open(cfg)
	if err != nil {
		return nil, fmt.Errorf("sdnservices: open store: %w", err)
	}

	capReg := modulert.NewCapabilityRegistry()
	storageFactory := NewStorageCapFactory(store, deps.FallbackSource)
	for _, name := range storageCapabilityNames {
		capReg.RegisterBridgeAware(name, storageFactory)
	}
	if ch != nil {
		capReg.RegisterBridgeAware("pubsub", NewPubSubCapFactory(ch))
	}
	// Outbound HTTP capability (fetch flows: CelesTrak ingest, supplemental
	// OMM). Fail-closed/operator-gated by module content hash (http is a
	// modulert sensitiveCapability); the factory additionally re-checks the
	// grant per call and enforces the owner's CelesTrak/Space-Track fetch
	// policy (>= 2.5s spacing + 3h URL ledger persisted under FetchLedgerDir).
	capReg.RegisterBridgeAware("http", NewHTTPCapFactory(HTTPCapConfig{LedgerDir: deps.FetchLedgerDir}))

	// Credential-keystore capability (secrets:<id>): a data-source module the
	// operator approved for a lane fetches that credential through the "secrets"
	// hostcall. Registered under each credstore.AllIDs lane name (like the
	// storage_* family) so whichever lane a module declares resolves to the one
	// handler; the handler re-checks the exact lane per call. secrets:* is a
	// modulert sensitive capability, so an unapproved module is denied at load
	// (fail closed). Only wired when the node opened a keystore.
	if deps.CredStore != nil {
		secretsFactory := NewSecretsCapFactory(deps.CredStore)
		for _, id := range credstore.AllIDs() {
			capReg.RegisterBridgeAware(CapabilityForID(id), secretsFactory)
		}
	}

	// Per-module cron config store + scheduler. The schedule_cron capability is
	// wired here so a running module can register/update its own schedule; it is
	// classified SENSITIVE (modulert), so this factory only ever runs for a
	// module an operator has approved for schedule_cron (fail closed). The
	// handler routes the module's schedule.* hostcalls to the live scheduler.
	configStore, err := sdncron.NewConfigStore(deps.ModulesConfigDir)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("sdnservices: open module config store: %w", err)
	}
	scheduler := sdncron.NewScheduler(configStore, deps.CronLog)
	capReg.RegisterBridgeAware("schedule_cron", func(mod *modulert.Module, _ *modulert.HostBridge) modulert.CapHandler {
		id := ""
		if mod != nil {
			id = mod.ID()
		}
		return modulert.CapHandler(scheduler.CronCapHandler(id))
	})

	return &Services{
		Store:       store,
		Channels:    ch,
		CapReg:      capReg,
		NodeCtx:     nodeCtx,
		Scheduler:   scheduler,
		ConfigStore: configStore,
	}, nil
}

// LoadModule loads a WASM module through modulert with this bundle's capability
// registry and NodeContext. A module's declared storage/pubsub capabilities are
// provisioned against these services — but modulert's operator capability
// policy gate (keyed by the module's content hash, fail closed) runs first, so
// an unapproved module requesting a sensitive capability is refused here.
func (s *Services) LoadModule(wasmBytes []byte) (*modulert.Module, error) {
	if s == nil {
		return nil, errors.New("sdnservices: nil Services")
	}
	return modulert.NewModule(wasmBytes, s.CapReg, s.NodeCtx)
}

// Close releases the store's FlatSQL engine. The durable blockstore + datastore
// are owned by the caller (the node) and are not touched.
func (s *Services) Close() {
	if s == nil {
		return
	}
	if s.Scheduler != nil {
		s.Scheduler.Stop()
	}
	if s.Store != nil {
		s.Store.Close()
	}
}
