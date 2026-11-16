package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type withdrawalPlan struct {
	Kind    string   `json:"kind"`
	Channel string   `json:"channel"`
	Version string   `json:"version"`
	Folders []string `json:"folders"`
	Indexes []string `json:"indexes"`
}

// WithdrawFeedVersion preflights the complete operation and resumes dated withdrawal plans.
func WithdrawFeedVersion(dir, kind, channel, version string) (string, error) {
	for _, component := range []string{kind, channel, version} {
		if !feedComponent(component) {
			return "", errors.New("kind, channel and version must be path components")
		}
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	plan := withdrawalPlan{Kind: kind, Channel: channel, Version: version}
	archive := ""
	alreadyWithdrawn := false
	// A durable plan survives interruption between moves or index replacements.
	siblings, err := os.ReadDir(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	for _, entry := range siblings {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), filepath.Base(absolute)+"-withdrawn-") {
			continue
		}
		candidate := filepath.Join(filepath.Dir(absolute), entry.Name())
		raw, err := os.ReadFile(filepath.Join(candidate, "withdrawal.json"))
		if err != nil {
			continue
		}
		var previous withdrawalPlan
		if json.Unmarshal(raw, &previous) == nil && previous.Kind == kind && previous.Channel == channel && previous.Version == version {
			if withdrawalFinished(absolute, candidate, previous) {
				alreadyWithdrawn = true
				continue
			}
			plan, archive = previous, candidate
			break
		}
	}
	if archive == "" {
		files, err := FeedSignalFiles(absolute, channel, version)
		if err != nil {
			return "", err
		}
		folders, indexes := map[string]bool{}, map[string]bool{}
		for _, file := range files {
			if file.Entry.Target.Kind != kind {
				continue
			}
			folders[filepath.Dir(file.Path)] = true
			indexes[filepath.Join(filepath.Dir(filepath.Dir(file.Path)), "index.json")] = true
		}
		// Also finish an earlier withdrawal that removed entries before moving files.
		base := filepath.Join(absolute, kind, channel)
		err = filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) && path == base {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(absolute, path)
			if err != nil {
				return err
			}
			parts := strings.Split(relative, string(filepath.Separator))
			if len(parts) == 5 && parts[4] == version {
				if entry.Type()&os.ModeSymlink != 0 {
					return errors.New("refusing a symlinked release directory")
				}
				if entry.IsDir() {
					folders[relative] = true
					return filepath.SkipDir
				}
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		for folder := range folders {
			plan.Folders = append(plan.Folders, folder)
		}
		for index := range indexes {
			plan.Indexes = append(plan.Indexes, index)
		}
		sort.Strings(plan.Folders)
		sort.Strings(plan.Indexes)
		if len(plan.Folders) == 0 && len(plan.Indexes) == 0 {
			if alreadyWithdrawn {
				return "", errors.New("version is already withdrawn")
			}
			return "", errors.New("version is absent or withdrawn")
		}
		archive = absolute + "-withdrawn-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	// Refuse symlinks and paths outside the selected target before changing the feed.
	validate := func(relative string, folder bool) error {
		parts := strings.Split(relative, string(filepath.Separator))
		if len(parts) != 5 || parts[0] != kind || parts[1] != channel {
			return errors.New("invalid withdrawal plan path")
		}
		for _, part := range parts {
			if !feedComponent(part) {
				return errors.New("invalid withdrawal path component")
			}
		}
		if folder && parts[4] != version || !folder && parts[4] != "index.json" {
			return errors.New("withdrawal plan target differs")
		}
		path := absolute
		for i, part := range parts {
			path = filepath.Join(path, part)
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) && folder && i == 4 {
				target := filepath.Join(archive, relative)
				resolved, resolveErr := filepath.EvalSymlinks(target)
				if resolveErr == nil && resolved == target {
					return nil
				}
			}
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("refusing a symlinked withdrawal path")
			}
		}
		return nil
	}
	for _, folder := range plan.Folders {
		if err := validate(folder, true); err != nil {
			return "", err
		}
		if _, err := os.Lstat(filepath.Join(absolute, folder)); err == nil {
			if _, targetErr := os.Lstat(filepath.Join(archive, folder)); !errors.Is(targetErr, os.ErrNotExist) {
				return "", fmt.Errorf("withdrawal destination already exists or is unreadable: %s", folder)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	rewritten := map[string][]byte{}
	for _, index := range plan.Indexes {
		if err := validate(index, false); err != nil {
			return "", err
		}
		raw, err := os.ReadFile(filepath.Join(absolute, index))
		if err != nil {
			return "", err
		}
		feed, err := ParseProviderFeed(raw)
		if err != nil {
			return "", err
		}
		kept := make([]ProviderFeedUpdate, 0, len(feed.Updates))
		for _, entry := range feed.Updates {
			if entry.Target.Kind != kind || entry.Channel != channel || entry.Version != version {
				kept = append(kept, entry)
			}
		}
		feed.Updates, feed.GeneratedAt = kept, time.Now().UTC().Format(time.RFC3339)
		raw, err = json.Marshal(feed)
		if err != nil {
			return "", err
		}
		rewritten[index] = raw
	}
	if err := os.MkdirAll(archive, 0o700); err != nil {
		return "", err
	}
	// Rename probes check permissions and the filesystem boundary without moving release files.
	probe := func(sourceDir, targetDir string) error {
		file, err := os.CreateTemp(sourceDir, ".withdraw-probe-")
		if err != nil {
			return err
		}
		source := file.Name()
		file.Close()
		defer os.Remove(source)
		target := filepath.Join(targetDir, filepath.Base(source))
		defer os.Remove(target)
		if err := os.Rename(source, target); err != nil {
			return fmt.Errorf("withdrawal destination must be writable on the same filesystem: %w", err)
		}
		return nil
	}
	for _, folder := range plan.Folders {
		targetDir := filepath.Dir(filepath.Join(archive, folder))
		if err := os.MkdirAll(targetDir, 0o700); err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(targetDir)
		if err != nil {
			return "", err
		}
		if resolved != targetDir {
			return "", errors.New("refusing a symlinked withdrawal destination")
		}
		source := filepath.Join(absolute, folder)
		if _, err := os.Stat(source); err == nil {
			if err := probe(source, targetDir); err != nil {
				return "", err
			}
		}
		if err := probe(filepath.Dir(filepath.Join(absolute, folder)), targetDir); err != nil {
			return "", err
		}
	}
	for _, index := range plan.Indexes {
		dir := filepath.Dir(filepath.Join(absolute, index))
		if err := probe(dir, dir); err != nil {
			return "", err
		}
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	journal, err := os.CreateTemp(archive, ".withdraw-plan-")
	if err != nil {
		return "", err
	}
	journalPath := journal.Name()
	defer os.Remove(journalPath)
	if _, err = journal.Write(raw); err == nil {
		err = journal.Sync()
	}
	closeErr := journal.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Rename(journalPath, filepath.Join(archive, "withdrawal.json")); err != nil {
		return "", err
	}
	for _, folder := range plan.Folders {
		source, target := filepath.Join(absolute, folder), filepath.Join(archive, folder)
		if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.Rename(source, target); err != nil {
			return archive, err
		}
	}
	for _, index := range plan.Indexes {
		path := filepath.Join(absolute, index)
		file, err := os.CreateTemp(filepath.Dir(path), ".withdraw-index-")
		if err != nil {
			return archive, err
		}
		temp := file.Name()
		defer os.Remove(temp)
		if _, err = file.Write(rewritten[index]); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return archive, err
		}
		if closeErr != nil {
			return archive, closeErr
		}
		if err := os.Chmod(temp, 0o644); err != nil {
			return archive, err
		}
		if err := os.Rename(temp, path); err != nil {
			return archive, err
		}
	}
	return archive, nil
}

// Archived folders with no remaining index entry describe a completed withdrawal.
// A completed withdrawal with newly served folders describes a republication.
func withdrawalFinished(root, archive string, plan withdrawalPlan) bool {
	if len(plan.Folders) == 0 {
		return false
	}
	allServed := true
	for _, folder := range plan.Folders {
		if filepath.IsAbs(folder) || strings.Contains(folder, "..") {
			return false
		}
		info, err := os.Lstat(filepath.Join(archive, folder))
		if err != nil || !info.IsDir() {
			return false
		}
		if info, err := os.Lstat(filepath.Join(root, folder)); err != nil || !info.IsDir() {
			allServed = false
		}
	}
	listed := false
	for _, index := range plan.Indexes {
		if filepath.IsAbs(index) || strings.Contains(index, "..") {
			return false
		}
		raw, err := os.ReadFile(filepath.Join(root, index))
		if err != nil {
			return false
		}
		feed, err := ParseProviderFeed(raw)
		if err != nil {
			return false
		}
		for _, entry := range feed.Updates {
			if entry.Target.Kind == plan.Kind && entry.Channel == plan.Channel && entry.Version == plan.Version {
				listed = true
			}
		}
	}
	return !listed || allServed
}
