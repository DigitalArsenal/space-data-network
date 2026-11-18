package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// MigrateStore5 is the offline command's entry point. First boot calls the
// same implementation while holding the store's normal exclusive lock.
func MigrateStore5(ctx context.Context, root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	lock, err := acquireStoreLock(root)
	if err != nil {
		return err
	}
	defer lock.release()
	m, err := marker.Read(root)
	if err != nil {
		return err
	}
	if !m.Format4() {
		return errors.New("store-migrate --to 5 requires an existing format-4 or format-5 store")
	}
	return migrateStore5Locked(ctx, root)
}

func migrateStore5Locked(ctx context.Context, root string) error {
	destination := filepath.Join(root, marker.Dir)
	if _, err := os.Lstat(destination); err == nil {
		m, err := marker.Read(root)
		if err != nil {
			return err
		}
		if m.Directory != marker.Dir || (!m.Activated() && !m.RecoverablePromotion) {
			return fmt.Errorf("%s is not a complete format-5 store; refusing to overwrite it", destination)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	legacy, err := marker.ReadLegacy(root)
	if err != nil || !legacy.Format4() {
		return err
	}
	if !legacy.ActivatedAs(4) && !legacy.ActivatedAs(5) {
		return fmt.Errorf("store-migrate --to 5 needs a complete format-4 store; %s has invalid or unsupported markers", root)
	}
	if ok, why := format4.Runnable(); !ok {
		return fmt.Errorf("store-migrate --to 5 cannot run: %s", why)
	}
	started := time.Now()
	source := filepath.Join(root, marker.LegacyDir)
	stage := filepath.Join(root, ".store-format5-pending")
	owner := fmt.Sprintf("SDN format-5 candidate v1\nsource=%x\n", legacy.UUID)
	// A killed first boot leaves only an unpublished candidate. Reclaim that
	// candidate under the exclusive store lock before checking free space.
	if info, err := os.Lstat(stage); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("conversion candidate %s is not a directory", stage)
		}
		manifest := filepath.Join(stage, "OWNER")
		info, err := os.Lstat(manifest)
		if errors.Is(err, os.ErrNotExist) {
			entries, readErr := os.ReadDir(stage)
			if readErr != nil || len(entries) != 0 {
				return fmt.Errorf("conversion candidate %s has no ownership marker; refusing to remove it", stage)
			}
		} else if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("conversion candidate %s has an invalid ownership marker", stage)
		} else if data, readErr := os.ReadFile(manifest); readErr != nil || string(data) != owner {
			return fmt.Errorf("conversion candidate %s belongs to another source; refusing to remove it", stage)
		}
		if err := os.RemoveAll(stage); err != nil {
			return err
		}
		log.Infof("store conversion 4 -> 5: removed interrupted private candidate; source remains intact")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var bytes int64
	var feedIndexes uint64
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("conversion refuses nonregular entry %s", path)
		}
		if info.Mode().IsRegular() {
			bytes += info.Size()
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			parts := strings.Split(filepath.ToSlash(relative), "/")
			if len(parts) == 3 && parts[0] == "P" && strings.HasSuffix(parts[2], ".db") {
				feedIndexes++
			}
		}
		return ctx.Err()
	}); err != nil {
		return err
	}
	aux := filepath.Join(root, auxiliaryMetadataFileName)
	var auxiliaryBytes uint64
	if info, err := os.Lstat(aux); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("conversion refuses nonregular file %s", aux)
		}
		auxiliaryBytes = uint64(info.Size())
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	free, err := migrationFreeBytes(root)
	if err != nil {
		return err
	}
	need := uint64(bytes)*3 + auxiliaryBytes + (1 << 30)
	if free < need {
		return fmt.Errorf("format-5 conversion needs %d bytes free for a verified copy and rollback; %s has %d", need, root, free)
	}
	if err := os.Mkdir(stage, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(stage) // only this invocation's unpublished private copy
	manifest, err := os.OpenFile(filepath.Join(stage, "OWNER"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := manifest.WriteString(owner)
	if err := errors.Join(writeErr, manifest.Sync(), manifest.Close()); err != nil {
		return err
	}
	if err := syncStore5Dir(stage); err != nil {
		return err
	}
	log.Infof("store conversion 4 -> 5: copying %d bytes from %s; serving remains disabled", bytes, source)
	if err := copyStore5Tree(ctx, source, filepath.Join(stage, marker.Dir)); err != nil {
		return err
	}
	mode := format4.ConvertFormat4
	if legacy.Format == 5 {
		mode = format4.OpenExisting // previous builds used the wrong directory name
	}
	log.Infof("store conversion 4 -> 5: rewriting and verifying records and metadata in %s", stage)
	// Conversion includes draining the rebuilt indexes before activation.
	// A background caller must not fall back to the normal 30-second close
	// budget after a full-store rewrite. A shorter caller deadline still wins.
	conversionCtx, cancelConversion := context.WithTimeout(ctx, 24*time.Hour)
	defer cancelConversion()
	engine, err := format4OpenEngine(conversionCtx, format4.Options{
		DataRoot: stage, Create: mode, Cores: 2, AOTCacheDir: format4AOTCacheDir(),
		CompileOnMiss: true, ControlBudget: 24 * time.Hour,
		ConversionFeeds: feedIndexes,
	})
	if err != nil {
		return fmt.Errorf("format-5 conversion failed; format-4 source retained at %s: %w", source, err)
	}
	log.Infof("store conversion 4 -> 5: verified records; draining and closing before switch")
	if err := engine.Close(conversionCtx); err != nil {
		return fmt.Errorf("close converted store before switch: %w", err)
	}
	log.Infof("store conversion 4 -> 5: converted engine closed")
	converted, err := marker.Read(stage)
	if err != nil {
		return err
	}
	if !converted.Activated() {
		return errors.New("conversion did not activate a verified format-5 store")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The old engine directory already retains control.db and its publication
	// catalog. Keep the auxiliary journal before normal boot can append to it.
	if _, err := os.Stat(aux); err == nil {
		backup := filepath.Join(source, "rollback-auxiliary.flatsqlmeta")
		temporary := filepath.Join(stage, "rollback-auxiliary.flatsqlmeta")
		if err := copyStore5File(ctx, aux, temporary); err != nil {
			return err
		}
		// A prior attempt may have failed before switching, after which the
		// old daemon could have appended auxiliary metadata. Snapshot this
		// attempt's source atomically, including on a retry.
		if err := os.Rename(temporary, backup); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncStore5Dir(source); err != nil {
		return err
	}
	// An atomic directory rename is the only selection switch. Before it,
	// boot sees the old source; afterward it sees the fully verified target.
	if err := os.Rename(filepath.Join(stage, marker.Dir), destination); err != nil {
		return err
	}
	if err := syncStore5Dir(root); err != nil {
		return err
	}
	log.Infof("store conversion 4 -> 5: verified and switched in %s; rollback store retained at %s", time.Since(started).Round(time.Millisecond), source)
	return nil
}

func copyStore5Tree(ctx context.Context, source, target string) error {
	if err := os.Mkdir(target, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		from, to := filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())
		if entry.IsDir() {
			err = copyStore5Tree(ctx, from, to)
		} else {
			err = copyStore5File(ctx, from, to)
		}
		if err != nil {
			return err
		}
	}
	return syncStore5Dir(target)
}

func copyStore5File(ctx context.Context, source, target string) (err error) {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("conversion refuses nonregular file %s", source)
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	buffer := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := in.Read(buffer)
		if n > 0 {
			if _, err := out.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return out.Sync()
}

func syncStore5Dir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
