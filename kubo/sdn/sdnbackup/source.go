package sdnbackup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/flowrt"
	"github.com/ipfs/kubo/sdn/sdnmodules"
	"github.com/ipfs/kubo/sdn/sdnstore"
)

// SourceFile declares an on-disk node file to include as a backup unit — the
// registry index (installed.json) and the backup config are the natural ones
// (spec 1.5 module_registry / config kinds). The path is read verbatim; the
// FilePath hint recorded in Meta is used by the restager to write it back under
// its own configured root (never an absolute path from a blob).
type SourceFile struct {
	Path     string
	Kind     Kind
	FilePath string // node-relative restage destination hint
	Name     string
}

// BackupSource is the node-side backup-source read surface (spec A.4): it
// enumerates the node's backup units and fetches their bytes. The repurposed
// storage_adapter capability wraps exactly this (storage.adapter.list_units /
// storage.adapter.get_unit); the Runner uses it directly in-process.
//
// Every field is optional: a source with only a Registry + Blockstore enumerates
// modules; add Flows for flow bundles; add Store for SDS records; add Files for
// registry/config. This lets the round-trip be proven without standing up the
// FlatSQL engine (no Store) when only modules + flows are under test.
type BackupSource struct {
	// Blockstore resolves module WASM bytes by content hash. Required for
	// module_wasm units.
	Blockstore appmanifest.ModuleBlockstore
	// Registry lists installed modules. Optional.
	Registry *sdnmodules.Registry
	// Flows lists installed flow artifacts. Optional.
	Flows *flowrt.FlowStore
	// Store reads SDS records / app manifests. Optional (needs the FlatSQL
	// engine); when nil, sds_record / app_manifest units are skipped.
	Store *sdnstore.Store
	// Node is the source id records + receipts are attributed to.
	Node string
	// Files are extra on-disk files (installed.json, backup config) to include.
	Files []SourceFile
}

// Units enumerates the node's backup units and materializes each as a
// BackupBlob (bytes included). When kinds is empty every applicable kind is
// returned; otherwise only the requested kinds. Modules are verified by hash on
// read (ResolveModuleByContentHash); flows are wrapped as a content-hashed
// $MBL; records are content-hashed from their FlatBuffer bytes.
//
// Materializing bytes up front is the simple, correct path for the round-trip
// (the Runner needs the bytes anyway). A streaming enumeration for very large
// node inventories is a documented follow-up.
func (s *BackupSource) Units(ctx context.Context, kinds ...Kind) ([]BackupBlob, error) {
	if s == nil {
		return nil, fmt.Errorf("sdnbackup: nil BackupSource")
	}
	want := kindSet(kinds)
	var out []BackupBlob

	if want[KindModuleWASM] && s.Registry != nil {
		mods, err := s.moduleUnits(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, mods...)
	}
	if want[KindFlowBundle] && s.Flows != nil {
		flows, err := s.flowUnits()
		if err != nil {
			return nil, err
		}
		out = append(out, flows...)
	}
	if (want[KindSDSRecord] || want[KindAppManifest]) && s.Store != nil {
		recs, err := s.recordUnits(ctx, want)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	files, err := s.fileUnits(want)
	if err != nil {
		return nil, err
	}
	out = append(out, files...)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ContentHash < out[j].ContentHash
	})
	return out, nil
}

// GetUnit fetches one unit by content hash. It re-enumerates and returns the
// match — O(n) but exact, and the read surface a WASM guest calls via
// storage.adapter.get_unit. The Runner uses Units directly and does not need
// this.
func (s *BackupSource) GetUnit(ctx context.Context, contentHash string) (BackupBlob, error) {
	h, err := NormalizeContentHash(contentHash)
	if err != nil {
		return BackupBlob{}, err
	}
	units, err := s.Units(ctx)
	if err != nil {
		return BackupBlob{}, err
	}
	for _, u := range units {
		if u.ContentHash == h {
			return u, nil
		}
	}
	return BackupBlob{}, adapterErr(ErrNotFound, "get_unit", "no backup unit with content hash %s", h)
}

func (s *BackupSource) moduleUnits(ctx context.Context) ([]BackupBlob, error) {
	if s.Blockstore == nil {
		return nil, fmt.Errorf("sdnbackup: module units require a Blockstore")
	}
	entries, err := s.Registry.List()
	if err != nil {
		return nil, fmt.Errorf("sdnbackup: list installed modules: %w", err)
	}
	out := make([]BackupBlob, 0, len(entries))
	for _, e := range entries {
		bytes, err := appmanifest.ResolveModuleByContentHash(ctx, s.Blockstore, e.ContentHash)
		if err != nil {
			return nil, fmt.Errorf("sdnbackup: resolve module %q bytes: %w", e.ID, err)
		}
		out = append(out, BackupBlob{
			ContentHash: strings.ToLower(strings.TrimSpace(e.ContentHash)),
			Kind:        KindModuleWASM,
			Meta: Meta{
				PluginID: e.ID,
				Name:     e.Name,
				Version:  e.Version,
				Enabled:  e.Enabled,
				Source:   e.Source,
			},
			Bytes: bytes,
		})
	}
	return out, nil
}

func (s *BackupSource) flowUnits() ([]BackupBlob, error) {
	flows, err := s.Flows.List()
	if err != nil {
		return nil, fmt.Errorf("sdnbackup: list flows: %w", err)
	}
	out := make([]BackupBlob, 0, len(flows))
	for _, f := range flows {
		wasm, err := os.ReadFile(filepath.Join(f.Dir, "runtime.wasm"))
		if err != nil {
			return nil, fmt.Errorf("sdnbackup: read flow %q runtime.wasm: %w", f.ProgramID, err)
		}
		flowPLG, _ := os.ReadFile(filepath.Join(f.Dir, "flow.plg"))
		artifact, _ := os.ReadFile(filepath.Join(f.Dir, "artifact.json"))
		bundle, err := FlowBundleToMBL(f.ProgramID, wasm, flowPLG, artifact)
		if err != nil {
			return nil, fmt.Errorf("sdnbackup: wrap flow %q bundle: %w", f.ProgramID, err)
		}
		out = append(out, BackupBlob{
			ContentHash: HashBytes(bundle),
			Kind:        KindFlowBundle,
			Meta: Meta{
				ProgramID: f.ProgramID,
				Name:      f.Name,
				Version:   f.Version,
			},
			Bytes: bundle,
		})
	}
	return out, nil
}

func (s *BackupSource) recordUnits(ctx context.Context, want map[Kind]bool) ([]BackupBlob, error) {
	pairs, err := s.Store.Catalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("sdnbackup: catalog: %w", err)
	}
	var out []BackupBlob
	for _, p := range pairs {
		// Never back up receipts recursively.
		if p.Type == ReceiptType {
			continue
		}
		kind := KindSDSRecord
		if p.Type == "APP" {
			kind = KindAppManifest
		}
		if !want[kind] {
			continue
		}
		recs, err := s.Store.ReadBySourceType(ctx, p.Source, p.Type)
		if err != nil {
			return nil, fmt.Errorf("sdnbackup: read %s/%s: %w", p.Source, p.Type, err)
		}
		for _, fb := range recs {
			out = append(out, BackupBlob{
				ContentHash: HashBytes(fb),
				Kind:        kind,
				Meta:        Meta{Source: p.Source, SDSType: p.Type},
				Bytes:       fb,
			})
		}
	}
	return out, nil
}

func (s *BackupSource) fileUnits(want map[Kind]bool) ([]BackupBlob, error) {
	var out []BackupBlob
	for _, f := range s.Files {
		if !want[f.Kind] {
			continue
		}
		data, err := os.ReadFile(f.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // an absent optional file (e.g. no config yet) is not an error
			}
			return nil, fmt.Errorf("sdnbackup: read source file %q: %w", f.Path, err)
		}
		if len(data) == 0 {
			continue
		}
		dest := f.FilePath
		if dest == "" {
			dest = filepath.Base(f.Path)
		}
		out = append(out, BackupBlob{
			ContentHash: HashBytes(data),
			Kind:        f.Kind,
			Meta:        Meta{Name: f.Name, FilePath: dest},
			Bytes:       data,
		})
	}
	return out, nil
}

// BackupUnitsJSON implements the sdnservices.BackupReadSurface list half: it
// returns a JSON array of {contentHash, kind, size, meta} descriptors (bytes
// omitted). The primitive (JSON) return type keeps the storage_adapter cap
// boundary in sdnservices free of an sdnbackup import — sdnbackup imports
// sdnmodules, which imports sdnservices, so the reverse edge would cycle.
func (s *BackupSource) BackupUnitsJSON(ctx context.Context, kinds []string) ([]byte, error) {
	ks := make([]Kind, 0, len(kinds))
	for _, k := range kinds {
		ks = append(ks, Kind(k))
	}
	units, err := s.Units(ctx, ks...)
	if err != nil {
		return nil, err
	}
	type desc struct {
		ContentHash string `json:"contentHash"`
		Kind        string `json:"kind"`
		Size        int    `json:"size"`
		Meta        Meta   `json:"meta"`
	}
	out := make([]desc, len(units))
	for i, u := range units {
		out[i] = desc{ContentHash: u.ContentHash, Kind: string(u.Kind), Size: len(u.Bytes), Meta: u.Meta}
	}
	return json.Marshal(out)
}

// BackupUnitBytes implements the sdnservices.BackupReadSurface fetch half.
func (s *BackupSource) BackupUnitBytes(ctx context.Context, contentHash string) (kind string, metaJSON []byte, data []byte, err error) {
	blob, err := s.GetUnit(ctx, contentHash)
	if err != nil {
		return "", nil, nil, err
	}
	metaJSON, err = json.Marshal(blob.Meta)
	if err != nil {
		return "", nil, nil, err
	}
	return string(blob.Kind), metaJSON, blob.Bytes, nil
}

// kindSet returns the requested set, or the full applicable set when empty.
func kindSet(kinds []Kind) map[Kind]bool {
	if len(kinds) == 0 {
		return map[Kind]bool{
			KindModuleWASM:     true,
			KindFlowBundle:     true,
			KindSDSRecord:      true,
			KindAppManifest:    true,
			KindModuleRegistry: true,
			KindConfig:         true,
		}
	}
	set := make(map[Kind]bool, len(kinds))
	for _, k := range kinds {
		set[k] = true
	}
	return set
}
