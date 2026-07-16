package sdnbackup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/flowrt"
	"github.com/ipfs/kubo/sdn/sdnmodules"
	"github.com/ipfs/kubo/sdn/sdnstore"
)

// NodeRestager re-installs restored blobs into a live node by kind (spec C.7):
//
//	module_wasm     -> StoreModuleBytes(bs, wasm) then Registry.Put(InstalledEntry)
//	flow_bundle     -> FlowStore.Install(programId, wasm, flow.json, artifact.json)
//	sds_record      -> sdnstore.Store(source, type, fb)
//	app_manifest    -> sdnstore.StoreManifest(source, "APP", fb)
//	module_registry -> write installed.json back under FileRoot
//	config          -> write the config file back under FileRoot
//
// Every field is optional; a restage for a kind whose target is nil is a typed
// error, so a partial-node restore fails loudly rather than silently dropping a
// unit.
type NodeRestager struct {
	Blockstore appmanifest.ModuleBlockstore
	Registry   *sdnmodules.Registry
	Flows      *flowrt.FlowStore
	Store      *sdnstore.Store
	// FileRoot is the directory module_registry / config units are written under
	// (their FilePath hint is resolved relative to it, never as an absolute path
	// from the blob).
	FileRoot string
	// CapabilityPrecheck, when set, must return nil before a module_wasm blob is
	// re-staged — the fail-closed re-check the spec requires (C.7). It cannot
	// smuggle an unapproved capability regardless: a restored module still passes
	// the load-time capability-policy gate (Services.LoadModule ->
	// checkCapabilityPolicy, keyed by content hash) when it is next loaded.
	CapabilityPrecheck func(blob BackupBlob) error
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

var _ Restager = (*NodeRestager)(nil)

func (n *NodeRestager) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// Restage dispatches a blob to the correct re-stage path by kind. The caller
// (Runner.Restore) has already verified sha256(bytes) == content hash.
func (n *NodeRestager) Restage(ctx context.Context, blob BackupBlob) error {
	switch blob.Kind {
	case KindModuleWASM:
		return n.restageModule(ctx, blob)
	case KindFlowBundle:
		return n.restageFlow(blob)
	case KindSDSRecord:
		return n.restageRecord(ctx, blob)
	case KindAppManifest:
		return n.restageAppManifest(ctx, blob)
	case KindModuleRegistry, KindConfig:
		return n.restageFile(blob)
	default:
		return fmt.Errorf("sdnbackup: restage: unsupported kind %q", blob.Kind)
	}
}

func (n *NodeRestager) restageModule(ctx context.Context, blob BackupBlob) error {
	if n.Blockstore == nil {
		return fmt.Errorf("sdnbackup: restage module: no blockstore configured")
	}
	if n.CapabilityPrecheck != nil {
		if err := n.CapabilityPrecheck(blob); err != nil {
			return fmt.Errorf("sdnbackup: restage module %q refused by capability precheck: %w", blob.Meta.PluginID, err)
		}
	}
	hash, _, err := appmanifest.StoreModuleBytes(ctx, n.Blockstore, blob.Bytes)
	if err != nil {
		return fmt.Errorf("sdnbackup: restage module bytes: %w", err)
	}
	if hash != blob.ContentHash {
		return fmt.Errorf("sdnbackup: restaged module hash %s != backup hash %s", hash, blob.ContentHash)
	}
	if n.Registry != nil {
		id := blob.Meta.PluginID
		if id == "" {
			id = hash
		}
		if err := n.Registry.Put(sdnmodules.InstalledEntry{
			ID:          id,
			ContentHash: hash,
			Name:        blob.Meta.Name,
			Version:     blob.Meta.Version,
			Enabled:     blob.Meta.Enabled,
			Source:      "restore",
			InstalledAt: n.now().UTC().Format(time.RFC3339),
		}); err != nil {
			return fmt.Errorf("sdnbackup: restage module registry entry: %w", err)
		}
	}
	return nil
}

func (n *NodeRestager) restageFlow(blob BackupBlob) error {
	if n.Flows == nil {
		return fmt.Errorf("sdnbackup: restage flow: no flow store configured")
	}
	programID, wasm, flowJSON, artifact, err := FlowBundleFromMBL(blob.Bytes)
	if err != nil {
		return fmt.Errorf("sdnbackup: restage flow: %w", err)
	}
	if programID == "" {
		programID = blob.Meta.ProgramID
	}
	if err := n.Flows.Install(programID, wasm, flowJSON, artifact); err != nil {
		return fmt.Errorf("sdnbackup: restage flow install: %w", err)
	}
	return nil
}

func (n *NodeRestager) restageRecord(ctx context.Context, blob BackupBlob) error {
	if n.Store == nil {
		return fmt.Errorf("sdnbackup: restage record: no store configured")
	}
	if blob.Meta.Source == "" || blob.Meta.SDSType == "" {
		return fmt.Errorf("sdnbackup: restage record: missing source/type meta")
	}
	if _, err := n.Store.Store(ctx, blob.Meta.Source, blob.Meta.SDSType, blob.Bytes); err != nil {
		return fmt.Errorf("sdnbackup: restage record: %w", err)
	}
	return nil
}

func (n *NodeRestager) restageAppManifest(ctx context.Context, blob BackupBlob) error {
	if n.Store == nil {
		return fmt.Errorf("sdnbackup: restage app manifest: no store configured")
	}
	source := blob.Meta.Source
	if source == "" {
		return fmt.Errorf("sdnbackup: restage app manifest: missing source meta")
	}
	if _, err := n.Store.StoreManifest(ctx, source, "APP", blob.Bytes); err != nil {
		return fmt.Errorf("sdnbackup: restage app manifest: %w", err)
	}
	return nil
}

func (n *NodeRestager) restageFile(blob BackupBlob) error {
	if n.FileRoot == "" {
		return fmt.Errorf("sdnbackup: restage %s: no FileRoot configured", blob.Kind)
	}
	rel := blob.Meta.FilePath
	if rel == "" {
		return fmt.Errorf("sdnbackup: restage %s: no destination file path", blob.Kind)
	}
	// Resolve strictly under FileRoot — reject traversal from an untrusted blob.
	clean := filepath.Clean("/" + filepath.FromSlash(rel))
	dest := filepath.Join(n.FileRoot, clean)
	if !strings.HasPrefix(dest, filepath.Clean(n.FileRoot)+string(os.PathSeparator)) && dest != filepath.Clean(n.FileRoot) {
		return fmt.Errorf("sdnbackup: restage %s: path %q escapes FileRoot", blob.Kind, rel)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("sdnbackup: restage %s: mkdir: %w", blob.Kind, err)
	}
	if err := writeFileAtomic(dest, blob.Bytes); err != nil {
		return fmt.Errorf("sdnbackup: restage %s: write: %w", blob.Kind, err)
	}
	return os.Chmod(dest, 0o600)
}
