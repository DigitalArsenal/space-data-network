package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// applyPendingStorageMove moves the node's data folder into storage.move_to
// before anything opens it, when the dashboard's storage editor chose a new
// folder (owner 2026-10-07). storage.path does not change: it becomes a link
// to the new folder, so the keys and everything else the node keeps beside
// its data folder stay where they are, and every component (an older
// binary too) follows the link. On one drive the move is a rename; across
// drives the data is copied and the old folder is kept beside itself as
// "<folder>.moved-<time>", never deleted. Moving to storage.path itself
// brings the data home and removes the link. If the move fails, the node
// starts with its data where it was and says so.
func applyPendingStorageMove(cfg *config.Config, configFile string) {
	to := strings.TrimSpace(cfg.Storage.MoveTo)
	if to == "" {
		return
	}
	defer func() {
		cfg.Storage.MoveTo = ""
		if configFile != "" {
			if err := config.SetStorageKeys(configFile, map[string]string{"move_to": ""}); err != nil {
				log.Warnf("Storage move: the config could not be updated: %v", err)
			}
		}
	}()
	link := filepath.Clean(cfg.Storage.Path)
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		log.Warnf("Storage move: %s cannot be read (%v); nothing moved", link, err)
		return
	}
	to = filepath.Clean(to)
	if samePath(to, real) {
		return
	}
	log.Infof("Storage move: moving %s to %s", real, to)
	if samePath(to, link) {
		err = moveStoreHome(link, real)
	} else {
		err = moveStoreAway(link, real, to)
	}
	if err != nil {
		log.Errorf("Storage move: the data stays in %s: %v", real, err)
		return
	}
	log.Infof("Storage move: the node's data is now in %s", to)
}

// moveStoreAway moves the data folder to `to` and points the link at it.
func moveStoreAway(link, real, to string) error {
	if err := moveStoreDir(real, to); err != nil {
		return err
	}
	return pointStorageLink(link, to)
}

// moveStoreHome brings the data back into storage.path, replacing the link.
func moveStoreHome(link, real string) error {
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return fmt.Errorf("%s is not a link to move the data back into", link)
	}
	aside := link + ".link"
	if err := os.Rename(link, aside); err != nil {
		return err
	}
	if err := moveStoreDir(real, link); err != nil {
		_ = os.Rename(aside, link)
		return err
	}
	return os.Remove(aside)
}

// pointStorageLink makes link a link to target, replacing the link (or the
// folder that was moved away) atomically.
func pointStorageLink(link, target string) error {
	if info, err := os.Lstat(link); err == nil && info.Mode()&fs.ModeSymlink == 0 {
		return fmt.Errorf("%s still exists and is not a link", link)
	}
	tmp := link + ".link-new"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

// moveStoreDir moves from to `to`: a rename on one drive, else a copy after
// which the original is kept as "<from>.moved-<time>".
func moveStoreDir(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	if entries, err := os.ReadDir(to); err == nil {
		for _, entry := range entries {
			if entry.Name() != ".DS_Store" {
				return fmt.Errorf("%s is not empty", to)
			}
		}
		// The empty folder the editor checked; the data takes its place.
		if err := os.RemoveAll(to); err != nil {
			return err
		}
	}
	err := os.Rename(from, to)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyStoreTree(from, to); err != nil {
		_ = os.RemoveAll(to) // only the partial copy; the original is untouched
		return err
	}
	return os.Rename(from, fmt.Sprintf("%s.moved-%s", from, time.Now().UTC().Format("20060102T150405Z")))
}

func copyStoreTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(to, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dest)
		case entry.IsDir():
			return os.MkdirAll(dest, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyStoreFile(path, dest, info.Mode().Perm())
		default:
			return nil // sockets and pipes are not data
		}
	})
}

func copyStoreFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
