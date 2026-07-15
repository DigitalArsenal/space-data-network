package sdnmodules

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdncron"
	"github.com/ipfs/kubo/sdn/sdnservices"
)

// Logger is a minimal printf-style sink (e.g. a go-log Logger's Infof). Nil is a
// silent logger.
type Logger func(format string, args ...interface{})

// Sentinel errors the admin install route maps to HTTP status codes:
// ErrModuleNotFound -> 404 (the content hash is well-formed but no block is
// present), ErrInstallDenied -> 403 (the module requests a sensitive capability
// no operator approval covers — fail closed). Anything else is a 400/500.
var (
	ErrModuleNotFound = errors.New("sdnmodules: module not found in blockstore")
	ErrInstallDenied  = errors.New("sdnmodules: install denied by capability policy")
)

// InstalledModule is the read model for one installed + registered module: its
// identity, content hash, display fields, enabled flag and declared timer method
// ids. It is what Install/List/AdminInstall return.
type InstalledModule struct {
	ID          string   `json:"id"`
	ContentHash string   `json:"content_hash"`
	Name        string   `json:"name,omitempty"`
	Version     string   `json:"version,omitempty"`
	Enabled     bool     `json:"enabled"`
	Source      string   `json:"source,omitempty"`
	Timers      []string `json:"timers"`
}

// CapabilityGrant is one operator capability approval accompanying an admin
// install: it records that the operator authorizes the module (identified by the
// install's content hash) to use Capability. Fail-closed still holds — a
// sensitive capability the module declares but the grants omit refuses the whole
// install.
type CapabilityGrant struct {
	Capability string
	ApprovedBy string
	Note       string
}

// Config wires an Installer to a live node.
type Config struct {
	// Services is the live SDN services bundle (sdnservices.BuildServices):
	// LoadModule (fail-closed capability gate + capability provisioning), the
	// cron Scheduler, and the NodeContext carrying the operator policy. Required.
	Services *sdnservices.Services
	// Blockstore is the node's content-addressed store. Module bytes are put here
	// on install (so a later boot re-resolves them by CONTENT_HASH) and fetched
	// from here by InstallByContentHash / Boot. Required for content-hash installs
	// and for persistence across boots.
	Blockstore appmanifest.ModuleBlockstore
	// Registry persists the installed-modules set (installed.json). May be a
	// no-persistence registry (empty dir) — installs then do not survive a
	// restart. Required (non-nil); use NewRegistry("") for no-persistence.
	Registry *Registry
	// DropinDir is an optional operator drop-in directory scanned at Boot for
	// *.wasm files to install. Empty disables drop-in installs.
	DropinDir string
	// Log is an optional printf sink.
	Log Logger
}

// Installer installs real module-sdk WASM artifacts onto a live SDN node and
// registers them with the cron scheduler. See the package doc for the flow.
type Installer struct {
	svc       *sdnservices.Services
	bs        appmanifest.ModuleBlockstore
	reg       *Registry
	dropinDir string
	log       Logger

	mu     sync.Mutex
	loaded map[string]*modulert.Module // id -> live module handle
}

// New builds an Installer. Services and Registry are required.
func New(cfg Config) (*Installer, error) {
	if cfg.Services == nil {
		return nil, errors.New("sdnmodules: Config.Services is required")
	}
	if cfg.Services.Scheduler == nil {
		return nil, errors.New("sdnmodules: Services.Scheduler is required")
	}
	if cfg.Registry == nil {
		return nil, errors.New("sdnmodules: Config.Registry is required (use NewRegistry(\"\") for no-persistence)")
	}
	return &Installer{
		svc:       cfg.Services,
		bs:        cfg.Blockstore,
		reg:       cfg.Registry,
		dropinDir: strings.TrimSpace(cfg.DropinDir),
		log:       cfg.Log,
		loaded:    make(map[string]*modulert.Module),
	}, nil
}

func (in *Installer) logf(format string, args ...interface{}) {
	if in.log != nil {
		in.log(format, args...)
	}
}

// policy returns the operator capability policy the services carry, or nil.
func (in *Installer) policy() *modulert.CapabilityPolicyStore {
	if in.svc == nil || in.svc.NodeCtx == nil {
		return nil
	}
	return in.svc.NodeCtx.CapabilityPolicy
}

// InstallBytes installs a module from its portable WASM bytes: it stores the
// bytes in the blockstore (so a later boot re-resolves them by content hash),
// then loads, registers and persists it. source is a provenance tag recorded in
// the registry. The capability policy is enforced FAIL CLOSED inside LoadModule —
// a module requesting an unapproved sensitive capability is refused here and is
// NOT installed, registered or persisted.
func (in *Installer) InstallBytes(ctx context.Context, wasm []byte, source string) (InstalledModule, error) {
	if len(wasm) == 0 {
		return InstalledModule{}, errors.New("sdnmodules: module bytes are empty")
	}
	// Persist the bytes to the content-addressed store first so the exact
	// artifact re-resolves by CONTENT_HASH on the next boot (idempotent).
	if in.bs != nil {
		if _, _, err := appmanifest.StoreModuleBytes(ctx, in.bs, wasm); err != nil {
			return InstalledModule{}, fmt.Errorf("sdnmodules: store module bytes: %w", err)
		}
	}
	return in.installResolved(wasm, source, true)
}

// InstallByContentHash installs a module already present in the node's
// blockstore, addressed by its APPModuleRef.CONTENT_HASH. The bytes are fetched
// (and verified-by-hash) via appmanifest, then loaded, registered and persisted.
// A missing block or a hash mismatch is an error; an unapproved sensitive
// capability refuses the install (fail closed).
func (in *Installer) InstallByContentHash(ctx context.Context, contentHash, source string) (InstalledModule, error) {
	if in.bs == nil {
		return InstalledModule{}, errors.New("sdnmodules: no blockstore configured; cannot install by content hash")
	}
	wasm, err := appmanifest.ResolveModuleByContentHash(ctx, in.bs, contentHash)
	if err != nil {
		return InstalledModule{}, fmt.Errorf("%w: %s: %v", ErrModuleNotFound, contentHash, err)
	}
	return in.installResolved(wasm, source, true)
}

// AdminInstall is the loopback admin-route entry point: it records the operator
// capability grants against the module's content hash in the operator policy,
// then installs the module by that content hash. Fail-closed still holds — if
// the module declares a sensitive capability the grants do not cover, LoadModule
// refuses and the install fails (nothing is registered or persisted). The
// recorded approvals persist with the policy so a later boot re-registers the
// module without re-granting.
func (in *Installer) AdminInstall(ctx context.Context, contentHash string, grants []CapabilityGrant) (InstalledModule, error) {
	contentHash = strings.ToLower(strings.TrimSpace(contentHash))
	if contentHash == "" {
		return InstalledModule{}, errors.New("sdnmodules: content_hash is required")
	}
	policy := in.policy()
	for _, g := range grants {
		cap := strings.TrimSpace(g.Capability)
		if cap == "" {
			continue
		}
		if policy == nil {
			return InstalledModule{}, errors.New("sdnmodules: no capability policy; cannot record operator grants")
		}
		approvedBy := strings.TrimSpace(g.ApprovedBy)
		if approvedBy == "" {
			approvedBy = "admin"
		}
		if _, err := policy.Approve(modulert.CapabilityApproval{
			ModuleHash: contentHash,
			Capability: cap,
			ApprovedBy: approvedBy,
			Note:       g.Note,
		}); err != nil {
			return InstalledModule{}, fmt.Errorf("sdnmodules: record grant %q: %w", cap, err)
		}
	}
	return in.InstallByContentHash(ctx, contentHash, "admin")
}

// installResolved is the shared core: it loads the WASM into a live module
// (LoadModule runs the fail-closed capability gate), registers it with the cron
// scheduler under its manifest timers, tracks the handle and — when persist is
// set — records it in the installed-modules registry. Idempotent by module id:
// re-installing an already-registered id refreshes the registry entry and closes
// the freshly-loaded duplicate rather than double-registering.
func (in *Installer) installResolved(wasm []byte, source string, persist bool) (InstalledModule, error) {
	contentHash := modulert.ContentHashHex(wasm)

	// LoadModule enforces the operator capability policy FAIL CLOSED (keyed by
	// contentHash) and provisions the granted storage_*/pubsub/schedule_cron
	// capabilities. A denial returns here — before any scheduler registration or
	// registry write — so an unapproved module never runs or is recorded.
	mod, err := in.svc.LoadModule(wasm)
	if err != nil {
		// A capability-policy denial (fail closed) is surfaced as ErrInstallDenied
		// so the admin route can answer 403; every other load failure stays a
		// plain error. The original message (which names the denied capabilities)
		// is preserved for the operator.
		if strings.Contains(err.Error(), "capability policy") {
			return InstalledModule{}, fmt.Errorf("%w: %s: %v", ErrInstallDenied, contentHash, err)
		}
		return InstalledModule{}, fmt.Errorf("sdnmodules: load module %s: %w", contentHash, err)
	}
	man := mod.Manifest()
	if man == nil {
		_ = mod.Close()
		return InstalledModule{}, fmt.Errorf("sdnmodules: module %s has no manifest", contentHash)
	}
	id := mod.ID()
	if strings.TrimSpace(id) == "" {
		_ = mod.Close()
		return InstalledModule{}, fmt.Errorf("sdnmodules: module %s has empty id", contentHash)
	}

	in.mu.Lock()
	if _, exists := in.loaded[id]; exists {
		// Already installed this boot: keep the running instance, drop the dup.
		in.mu.Unlock()
		_ = mod.Close()
		if persist {
			if err := in.persistEntry(id, contentHash, man.Name, man.Version, source); err != nil {
				return InstalledModule{}, err
			}
		}
		in.logf("sdnmodules: module %q (%s) already installed; refreshed registry entry", id, shortHash(contentHash))
		return in.view(id, contentHash, man, source, persist), nil
	}

	if err := in.svc.Scheduler.Register(sdncron.Registration{
		Module:  mod,
		Name:    man.Name,
		Version: man.Version,
	}); err != nil {
		in.mu.Unlock()
		_ = mod.Close()
		return InstalledModule{}, fmt.Errorf("sdnmodules: register module %q with scheduler: %w", id, err)
	}
	in.loaded[id] = mod
	in.mu.Unlock()

	if persist {
		if err := in.persistEntry(id, contentHash, man.Name, man.Version, source); err != nil {
			return InstalledModule{}, err
		}
	}
	in.logf("sdnmodules: installed + registered module %q (%s) with %d timer(s) [source=%s]", id, shortHash(contentHash), len(man.Timers), source)
	return in.view(id, contentHash, man, source, persist), nil
}

// persistEntry writes (or refreshes) the installed-modules registry record for a
// module, marking it enabled.
func (in *Installer) persistEntry(id, contentHash, name, version, source string) error {
	if err := in.reg.Put(InstalledEntry{
		ID:          id,
		ContentHash: contentHash,
		Name:        name,
		Version:     version,
		Enabled:     true,
		Source:      source,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return fmt.Errorf("sdnmodules: persist registry entry for %q: %w", id, err)
	}
	return nil
}

// view builds the read model for an installed module from its manifest.
func (in *Installer) view(id, contentHash string, man *modulert.Manifest, source string, enabled bool) InstalledModule {
	timers := make([]string, 0, len(man.Timers))
	for _, t := range man.Timers {
		timers = append(timers, t.MethodID)
	}
	sort.Strings(timers)
	return InstalledModule{
		ID:          id,
		ContentHash: contentHash,
		Name:        man.Name,
		Version:     man.Version,
		Enabled:     enabled,
		Source:      source,
		Timers:      timers,
	}
}

// Boot re-establishes the installed-modules set on a fresh Services build: it
// re-registers every ENABLED persisted registry entry (re-resolving its bytes
// from the blockstore by content hash), then scans the optional drop-in
// directory for new *.wasm files and installs those. It is idempotent and
// tolerant: a persisted entry whose bytes are missing, or a drop-in whose
// sensitive capabilities are unapproved, is logged and skipped rather than
// failing the whole boot. Returns the number of modules registered.
//
// Boot registers modules but does NOT start the scheduler — the caller starts it
// after Boot so every module's timers begin together (matching the plugin's
// register-then-Start ordering).
func (in *Installer) Boot(ctx context.Context) (int, error) {
	registered := 0
	seen := map[string]bool{} // content hashes handled this boot (dedup)

	// 1) Re-register persisted, enabled entries by content hash.
	entries, err := in.reg.List()
	if err != nil {
		return 0, fmt.Errorf("sdnmodules: read installed registry: %w", err)
	}
	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		if e.ContentHash == "" {
			in.logf("sdnmodules: boot: registry entry %q has no content hash; skipping", e.ID)
			continue
		}
		if seen[e.ContentHash] {
			continue
		}
		seen[e.ContentHash] = true
		if in.bs == nil {
			in.logf("sdnmodules: boot: no blockstore; cannot re-register %q", e.ID)
			continue
		}
		wasm, err := appmanifest.ResolveModuleByContentHash(ctx, in.bs, e.ContentHash)
		if err != nil {
			in.logf("sdnmodules: boot: resolve %q (%s) failed; skipping: %v", e.ID, shortHash(e.ContentHash), err)
			continue
		}
		// persist=false: the entry already exists; re-registration must not
		// rewrite its InstalledAt/source. A capability denial here is logged and
		// skipped (an operator may have revoked an approval since install).
		if _, err := in.installResolved(wasm, e.Source, false); err != nil {
			in.logf("sdnmodules: boot: register %q failed; skipping: %v", e.ID, err)
			continue
		}
		registered++
	}

	// 2) Install any drop-in *.wasm files not already installed this boot.
	if in.dropinDir != "" {
		n, err := in.installDropins(ctx, seen)
		if err != nil {
			in.logf("sdnmodules: boot: drop-in scan error: %v", err)
		}
		registered += n
	}

	return registered, nil
}

// installDropins installs every *.wasm under the drop-in directory whose content
// hash has not already been handled this boot. Errors on individual files are
// logged and skipped. Returns the count installed.
func (in *Installer) installDropins(ctx context.Context, seen map[string]bool) (int, error) {
	ents, err := os.ReadDir(in.dropinDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	installed := 0
	for _, de := range ents {
		if de.IsDir() || !strings.EqualFold(filepath.Ext(de.Name()), ".wasm") {
			continue
		}
		path := filepath.Join(in.dropinDir, de.Name())
		wasm, err := os.ReadFile(path)
		if err != nil {
			in.logf("sdnmodules: boot: read drop-in %q failed; skipping: %v", de.Name(), err)
			continue
		}
		if seen[modulert.ContentHashHex(wasm)] {
			continue
		}
		seen[modulert.ContentHashHex(wasm)] = true
		if _, err := in.InstallBytes(ctx, wasm, "dropin:"+de.Name()); err != nil {
			in.logf("sdnmodules: boot: install drop-in %q failed; skipping: %v", de.Name(), err)
			continue
		}
		installed++
	}
	return installed, nil
}

// List returns the read model for every module installed in THIS process,
// sorted by id. It reflects live registrations (the loaded handles), each joined
// with its persisted registry provenance.
func (in *Installer) List() []InstalledModule {
	in.mu.Lock()
	ids := make([]string, 0, len(in.loaded))
	mods := make(map[string]*modulert.Module, len(in.loaded))
	for id, m := range in.loaded {
		ids = append(ids, id)
		mods[id] = m
	}
	in.mu.Unlock()

	sort.Strings(ids)
	out := make([]InstalledModule, 0, len(ids))
	for _, id := range ids {
		mod := mods[id]
		man := mod.Manifest()
		source := ""
		enabled := true
		if e, ok, _ := in.reg.Get(id); ok {
			source = e.Source
			enabled = e.Enabled
		}
		if man == nil {
			out = append(out, InstalledModule{ID: id, ContentHash: mod.ContentHash(), Enabled: enabled, Source: source, Timers: []string{}})
			continue
		}
		out = append(out, in.view(id, mod.ContentHash(), man, source, enabled))
	}
	return out
}

// Module returns the live module handle for id, or nil. For the scheduler +
// settings API the module is already reachable via the scheduler; this accessor
// exists for the runtime plugin/UI and tests that want the module's own
// invocation stats (RuntimeDescriptor).
func (in *Installer) Module(id string) *modulert.Module {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.loaded[id]
}

// Close releases every loaded module handle. The scheduler is owned by the
// Services bundle (svc.Close stops it); this only closes the module runtimes.
func (in *Installer) Close() {
	in.mu.Lock()
	mods := make([]*modulert.Module, 0, len(in.loaded))
	for _, m := range in.loaded {
		mods = append(mods, m)
	}
	in.loaded = make(map[string]*modulert.Module)
	in.mu.Unlock()
	for _, m := range mods {
		_ = m.Close()
	}
}

// shortHash truncates a content hash for log lines.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12] + "…"
	}
	return h
}
