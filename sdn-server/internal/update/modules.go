// G4: built-in module (e.g. flatsql) targeted swap. A module-update manifest
// (Manifest.IsModuleUpdate) travels through the exact same signed +
// per-peer-encrypted carrier/bundle lane as a full-bundle update (G1/G2) and
// the same staged-verify pipeline (Stage, ScanStaged) as any other update;
// the only thing that differs is what Apply installs: instead of swapping
// the whole bundle-root payload (swapBundleContents / applyTwoPhase), it
// installs ONLY the artifacts declared in Manifest.Modules[], leaving every
// other bundle-root entry untouched. It reuses the same rollback primitive
// (restoreEntries) the bundle-swap paths use, so a failure partway through
// restores every module already swapped — never a partial install.
package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// verifiedModuleTarget is a module target whose declared hash has already
// been checked against the staged artifact bytes, resolved to a concrete
// source path inside the extracted incoming bundle. applyModuleTargets
// builds the full list of these (phase 1, read-only) before installing
// anything (phase 2), so a single tampered or misdeclared artifact aborts
// the whole apply without touching the live bundle root at all.
type verifiedModuleTarget struct {
	id     string
	rel    string // bundle-relative path, forward-slash, e.g. "runtime/modules/flatsql/flatsql.wasm"
	source string // absolute path to the verified artifact inside the extracted incoming bundle
}

// applyModuleTargets is the G4 targeted-swap apply path. newRoot is the
// already-extracted incoming bundle payload (same extraction Apply uses for
// a full bundle: extractBundleArchive + locateBundleRoot); rollbackDir is
// the same updates/rollback/<update_id>/ directory a full-bundle apply would
// use. modules is the manifest's declared Modules[].
//
// Phase 1 (verify) resolves and sha256-checks every module artifact against
// its declared Hash and rejects any target whose Path would land on a
// protectedEntries top-level name — all before any file under paths.Root is
// touched. Phase 2 (install) then, for each verified module in order: moves
// any pre-existing file at its install Path into rollbackDir (preserving the
// pre-apply bytes, exactly like the bundle-swap paths), and moves the new
// artifact into place. If any phase-2 step fails, every module already
// swapped in this call is restored via restoreEntries before the error is
// returned — the same all-or-nothing discipline swapBundleContents and
// applyTwoPhase provide for a full bundle.
func applyModuleTargets(paths Paths, newRoot, rollbackDir string, modules []ManifestModuleTarget) error {
	if len(modules) == 0 {
		return errors.New("update: module-targeted apply requires at least one module target")
	}

	verified := make([]verifiedModuleTarget, 0, len(modules))
	for _, module := range modules {
		rel := filepath.ToSlash(module.Path)
		if firstSeg := strings.SplitN(rel, "/", 2)[0]; protectedEntries[firstSeg] {
			return fmt.Errorf("module %s: install path %q targets a protected entry", module.ID, module.Path)
		}
		sourcePath, err := safeJoin(newRoot, module.Path)
		if err != nil {
			return fmt.Errorf("module %s: %w", module.ID, err)
		}
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return fmt.Errorf("module %s: read staged artifact: %w", module.ID, err)
		}
		if sha256Hex(data) != strings.ToLower(module.Hash) {
			return fmt.Errorf("module %s: artifact checksum mismatch", module.ID)
		}
		verified = append(verified, verifiedModuleTarget{id: module.ID, rel: rel, source: sourcePath})
	}

	if err := os.RemoveAll(rollbackDir); err != nil {
		return err
	}
	if err := os.MkdirAll(rollbackDir, 0o755); err != nil {
		return err
	}

	var movedToRollback, installed []string
	for _, module := range verified {
		if err := swapModuleArtifact(paths, rollbackDir, module, &movedToRollback, &installed); err != nil {
			restoreEntries(paths, rollbackDir, movedToRollback, installed)
			return err
		}
	}
	return nil
}

// swapModuleArtifact installs a single verified module artifact at its
// bundle-relative path, moving any pre-existing file there into rollbackDir
// first (recorded in movedToRollback) and then moving the new artifact into
// place (recorded in installed). Both slices use the same bundle-relative,
// forward-slash-joined path convention as swapEntrySet/restoreEntries
// (joinRel), so the caller can pass them straight to restoreEntries.
func swapModuleArtifact(paths Paths, rollbackDir string, module verifiedModuleTarget, movedToRollback, installed *[]string) error {
	target, err := safeJoin(paths.Root, module.rel)
	if err != nil {
		return fmt.Errorf("module %s: %w", module.id, err)
	}
	switch info, statErr := os.Lstat(target); {
	case statErr == nil && info.IsDir():
		return fmt.Errorf("module %s: install path %q is a directory, not a file", module.id, module.rel)
	case statErr == nil:
		rollbackTarget := filepath.Join(rollbackDir, filepath.FromSlash(module.rel))
		if err := os.MkdirAll(filepath.Dir(rollbackTarget), 0o755); err != nil {
			return err
		}
		if err := os.Rename(target, rollbackTarget); err != nil {
			return fmt.Errorf("module %s: retire current artifact: %w", module.id, err)
		}
		*movedToRollback = append(*movedToRollback, module.rel)
	case !os.IsNotExist(statErr):
		return statErr
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.Rename(module.source, target); err != nil {
		return fmt.Errorf("module %s: install artifact: %w", module.id, err)
	}
	*installed = append(*installed, module.rel)
	return nil
}
