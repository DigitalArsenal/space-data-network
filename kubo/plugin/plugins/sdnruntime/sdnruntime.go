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
	"fmt"
	"os"
	"path/filepath"
	"sync"

	logging "github.com/ipfs/go-log/v2"
	core "github.com/ipfs/kubo/core"
	plugin "github.com/ipfs/kubo/plugin"

	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdnservices"
	"github.com/ipfs/kubo/sdn/sdnstore"
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

	deps := sdnservices.Deps{
		Blockstore:     node.Blockstore,
		Datastore:      node.Repo.Datastore(),
		PubSub:         node.PubSub, // optional; nil => storage-only
		Schemas:        defaultSchemas(),
		HotWindow:      p.hotWindow,
		RuntimeOptions: runtimeOpts,
		Policy:         policy,
		PeerID:         node.Identity.String(),
		FallbackSource: node.Identity.String(),
	}

	svc, err := sdnservices.BuildServices(deps)
	if err != nil {
		return fmt.Errorf("sdnruntime: build services: %w", err)
	}
	setServices(svc)

	if node.PubSub == nil {
		log.Warnf("SDN runtime active (STORAGE-ONLY): node pubsub is disabled (Pubsub.Enabled=false) — channel fan-out and the pubsub module capability are unavailable; peer=%s", node.Identity)
	} else {
		log.Infof("SDN runtime active: storage + channel fan-out wired; module runtime capability-gated by %q; peer=%s", policyPath, node.Identity)
	}

	// Release the store engine when the node shuts down (the durable
	// blockstore/datastore are the node's and are left untouched).
	go func() {
		<-node.Context().Done()
		svc.Close()
		setServices(nil)
	}()

	return nil
}

func (*sdnRuntimePlugin) Close() error { return nil }

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
