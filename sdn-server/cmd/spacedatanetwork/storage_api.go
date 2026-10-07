package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/node"
)

// GET and PUT /api/v1/storage (admin): where this node keeps its data, how
// much of its drive it may use, and the drives it could use instead (owner
// 2026-10-07: "the remaining disk space allocated with an edit menu that
// allows the user to set the drive, directory, size limit as a total number
// and percentage of available disk space"). A new limit applies at once; a
// new folder applies at the next start, when the data moves there and
// storage.path becomes a link to it (storage_move.go). Path is where the
// data really is.

type storageDrive struct {
	Mount      string `json:"mount"`
	Name       string `json:"name"`
	TotalBytes int64  `json:"total_bytes"`
	FreeBytes  int64  `json:"free_bytes"`
	Writable   bool   `json:"writable"`
}

type storageState struct {
	Path        string         `json:"path"`
	PendingPath string         `json:"pending_path,omitempty"`
	MaxSize     string         `json:"max_size"`
	MaxBytes    int64          `json:"max_bytes"`
	UsedBytes   int64          `json:"used_bytes"`
	Drive       storageDrive   `json:"drive"`
	Drives      []storageDrive `json:"drives"`
	Editable    bool           `json:"editable"`
}

type storageUpdate struct {
	Path    *string `json:"path"`
	MaxSize *string `json:"max_size"`
}

type storageAPI struct {
	node       *node.Node
	configFile string
	mu         sync.Mutex
	pending    string
}

func (s *storageAPI) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		state := s.state()
		s.mu.Unlock()
		writeStorageJSON(w, http.StatusOK, state)
	case http.MethodPut:
		s.put(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		storageError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET or PUT.")
	}
}

// state is the live picture; callers hold s.mu.
func (s *storageAPI) state() storageState {
	path := realStoragePath(s.node.Config().Storage.Path)
	drives := listDrives()
	if drives == nil {
		drives = []storageDrive{}
	}
	state := storageState{Path: path, PendingPath: s.pending, MaxSize: s.node.StorageMaxSize(), Drives: drives, Editable: s.configFile != ""}
	if strings.TrimSpace(state.MaxSize) == "" {
		state.MaxSize = fmt.Sprintf("%d%%", config.DefaultStorageMaxSizePercent)
	}
	state.MaxBytes, _ = s.node.StorageLimitBytes()
	if store := s.node.Store(); store != nil {
		state.UsedBytes, _ = store.DiskUsageBytes()
	}
	state.Drive = driveFor(path, drives)
	return state
}

func (s *storageAPI) put(w http.ResponseWriter, r *http.Request) {
	var req storageUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		storageError(w, http.StatusBadRequest, "invalid_request", "The request could not be read.")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configFile == "" {
		storageError(w, http.StatusConflict, "no_config_file", "This node runs without a config file, so its storage settings cannot be saved.")
		return
	}
	running := realStoragePath(s.node.Config().Storage.Path)
	target := running
	if s.pending != "" {
		target = s.pending
	}
	if req.Path != nil {
		target = filepath.Clean(strings.TrimSpace(*req.Path))
		if !filepath.IsAbs(target) {
			storageError(w, http.StatusBadRequest, "invalid_path", "Enter the folder's full path.")
			return
		}
	}
	drives := listDrives()
	drive := driveFor(target, drives)
	if drive.TotalBytes <= 0 {
		storageError(w, http.StatusBadRequest, "invalid_path", "That folder is not on a drive this node can use.")
		return
	}
	updates := map[string]string{}
	spec := ""
	if req.MaxSize != nil {
		spec = strings.TrimSpace(*req.MaxSize)
		maxBytes, err := config.StorageConfig{MaxSize: spec}.ResolveMaxSizeBytes(existingParent(target))
		if err != nil || maxBytes <= 0 {
			storageError(w, http.StatusBadRequest, "invalid_limit", "Enter a limit such as 200GB or 40%.")
			return
		}
		if maxBytes > drive.TotalBytes {
			storageError(w, http.StatusBadRequest, "invalid_limit", "The limit is larger than the drive.")
			return
		}
		updates["max_size"] = spec
	}
	moving := !samePath(target, running)
	if moving {
		if current := driveFor(running, drives); current.Mount != drive.Mount {
			if used, _ := s.node.Store().DiskUsageBytes(); used > drive.FreeBytes {
				storageError(w, http.StatusBadRequest, "no_space", "That drive does not have room for this node's data.")
				return
			}
		}
		if nested(target, running) {
			storageError(w, http.StatusBadRequest, "invalid_path", "Choose a folder outside the node's current data folder.")
			return
		}
		if !samePath(target, filepath.Clean(s.node.Config().Storage.Path)) {
			if err := prepareStorageTarget(target); err != nil {
				storageError(w, http.StatusBadRequest, "invalid_path", err.Error())
				return
			}
		}
		updates["move_to"] = target
	} else if s.pending != "" {
		// Choosing the folder the data is in now cancels a pending move.
		updates["move_to"] = ""
	}
	if len(updates) > 0 {
		if err := config.SetStorageKeys(s.configFile, updates); err != nil {
			storageError(w, http.StatusInternalServerError, "not_saved", "The settings could not be saved: "+err.Error())
			return
		}
	}
	if moving {
		s.pending = target
	} else {
		s.pending = ""
	}
	if req.MaxSize != nil {
		if err := s.node.SetStorageMaxSize(spec); err != nil {
			storageError(w, http.StatusInternalServerError, "not_applied", "The limit is saved and applies at the next start: "+err.Error())
			return
		}
	}
	writeStorageJSON(w, http.StatusOK, s.state())
}

// driveFor is the drive holding path: a listed one when it is, else the
// filesystem itself as statfs reports it.
func driveFor(path string, drives []storageDrive) storageDrive {
	mount := mountOf(path)
	for _, drive := range drives {
		if drive.Mount == mount {
			return drive
		}
	}
	total, free, ok := statDrive(path)
	if !ok {
		return storageDrive{}
	}
	name := filepath.Base(mount)
	if mount == "" || mount == "/" {
		name = "System drive"
	}
	return storageDrive{Mount: mount, Name: name, TotalBytes: total, FreeBytes: free, Writable: true}
}

// prepareStorageTarget makes sure the node can move its data to target: a new
// or empty folder it can write to.
func prepareStorageTarget(target string) error {
	info, err := os.Stat(target)
	switch {
	case err == nil:
		if !info.IsDir() {
			return errors.New("That path is a file, not a folder.")
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			return errors.New("This node cannot read that folder.")
		}
		for _, entry := range entries {
			if entry.Name() != ".DS_Store" {
				return errors.New("Choose an empty folder; the node moves its data into it.")
			}
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(target, 0o700); err != nil {
			return errors.New("This node cannot create that folder.")
		}
	default:
		return errors.New("This node cannot reach that folder.")
	}
	probe, err := os.CreateTemp(target, ".sdn-write-check-*")
	if err != nil {
		return errors.New("This node cannot write to that folder.")
	}
	name := probe.Name()
	probe.Close()
	return os.Remove(name)
}

// realStoragePath is where the data folder really is (storage.path, or what
// it links to after a move).
func realStoragePath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(real)
	}
	return filepath.Clean(path)
}

// nested reports whether one folder is inside the other.
func nested(a, b string) bool {
	inside := func(child, parent string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return inside(a, b) || inside(b, a)
}

// existingParent is path, or its nearest parent that exists.
func existingParent(path string) string {
	for p := filepath.Clean(path); ; {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

func writeStorageJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func storageError(w http.ResponseWriter, status int, code, message string) {
	writeStorageJSON(w, status, map[string]string{"code": code, "message": message})
}
