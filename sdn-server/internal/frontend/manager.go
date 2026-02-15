// Package frontend provides admin API endpoints for managing the public-facing
// frontend directory of an SDN node. It supports listing, reading, writing, and
// deleting files, uploading via multipart form, and importing a git repository.
package frontend

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxUploadSize is the maximum total upload size (50 MB).
const MaxUploadSize = 50 << 20

// Manager manages the frontend directory for an SDN node.
type Manager struct {
	dir string
}

// NewManager creates a frontend manager rooted at dir.
func NewManager(dir string) *Manager {
	return &Manager{dir: dir}
}

// Dir returns the managed frontend directory path.
func (m *Manager) Dir() string { return m.dir }

// FileEntry describes a file in the frontend directory.
type FileEntry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime string `json:"mod_time"`
}

// RegisterRoutes registers the frontend management API on the given mux.
// All routes are under /api/admin/frontend/ and must be gated by admin auth
// in the caller.
func (m *Manager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/frontend/files", m.handleFiles)
	mux.HandleFunc("/api/admin/frontend/files/", m.handleFilePath)
	mux.HandleFunc("/api/admin/frontend/upload", m.handleUpload)
	mux.HandleFunc("/api/admin/frontend/git-import", m.handleGitImport)
}

// handleFiles lists all files in the frontend directory.
func (m *Manager) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var entries []FileEntry
	err := filepath.Walk(m.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(m.dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip hidden directories (e.g. .git)
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") {
			return filepath.SkipDir
		}
		entries = append(entries, FileEntry{
			Path:    rel,
			Size:    info.Size(),
			IsDir:   info.IsDir(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339),
		})
		return nil
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list files: %v", err), http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []FileEntry{}
	}
	sort.Slice(entries, func(i, j int) bool {
		// Directories first, then alphabetical
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Path < entries[j].Path
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// handleFilePath handles GET/PUT/DELETE for individual files.
func (m *Manager) handleFilePath(w http.ResponseWriter, r *http.Request) {
	relPath := strings.TrimPrefix(r.URL.Path, "/api/admin/frontend/files/")
	if relPath == "" {
		http.Error(w, "file path required", http.StatusBadRequest)
		return
	}

	// Prevent path traversal
	clean := filepath.Clean(relPath)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	fullPath := filepath.Join(m.dir, filepath.FromSlash(clean))

	// Ensure the resolved path is still within the frontend directory
	absDir, _ := filepath.Abs(m.dir)
	absPath, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absPath, absDir+string(os.PathSeparator)) && absPath != absDir {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		m.getFile(w, r, fullPath, clean)
	case http.MethodPut:
		m.putFile(w, r, fullPath, clean)
	case http.MethodDelete:
		m.deleteFile(w, r, fullPath, clean)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *Manager) getFile(w http.ResponseWriter, _ *http.Request, fullPath, relPath string) {
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "cannot read directory", http.StatusBadRequest)
		return
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"path":    relPath,
		"size":    info.Size(),
		"content": string(content),
	})
}

func (m *Manager) putFile(w http.ResponseWriter, r *http.Request, fullPath, relPath string) {
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxUploadSize)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// Ensure parent directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		http.Error(w, fmt.Sprintf("failed to create directory: %v", err), http.StatusInternalServerError)
		return
	}

	if err := os.WriteFile(fullPath, []byte(body.Content), 0644); err != nil {
		http.Error(w, fmt.Sprintf("failed to write file: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"path":   relPath,
	})
}

func (m *Manager) deleteFile(w http.ResponseWriter, _ *http.Request, fullPath, relPath string) {
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if err := os.RemoveAll(fullPath); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "deleted",
		"path":   relPath,
	})
}

// handleUpload handles multipart file uploads to the frontend directory.
func (m *Manager) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(MaxUploadSize); err != nil {
		http.Error(w, fmt.Sprintf("upload too large or invalid: %v", err), http.StatusBadRequest)
		return
	}

	// Optional subdirectory
	subdir := strings.TrimSpace(r.FormValue("path"))
	targetDir := m.dir
	if subdir != "" {
		clean := filepath.Clean(subdir)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		targetDir = filepath.Join(m.dir, filepath.FromSlash(clean))
	}

	var uploaded []string
	for _, headers := range r.MultipartForm.File {
		for _, header := range headers {
			if err := saveUploadedFile(header, targetDir); err != nil {
				http.Error(w, fmt.Sprintf("failed to save %s: %v", header.Filename, err), http.StatusInternalServerError)
				return
			}
			rel, _ := filepath.Rel(m.dir, filepath.Join(targetDir, header.Filename))
			uploaded = append(uploaded, rel)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"files":  uploaded,
	})
}

func saveUploadedFile(header *multipart.FileHeader, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	// Sanitize filename
	name := filepath.Base(header.Filename)
	if name == "." || name == ".." {
		return fmt.Errorf("invalid filename %q", header.Filename)
	}

	src, err := header.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(filepath.Join(targetDir, name))
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

// handleGitImport clones a git repository into the frontend directory.
func (m *Manager) handleGitImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		URL    string `json:"url"`
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.URL) == "" {
		http.Error(w, "git URL required", http.StatusBadRequest)
		return
	}

	// Validate URL looks like a git repo (basic check)
	repoURL := strings.TrimSpace(body.URL)
	if !strings.HasPrefix(repoURL, "https://") && !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "git@") {
		http.Error(w, "URL must be https://, http://, or git@ format", http.StatusBadRequest)
		return
	}

	// Clone into a temporary directory first, then swap
	tmpDir, err := os.MkdirTemp(filepath.Dir(m.dir), ".frontend-import-*")
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create temp dir: %v", err), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmpDir) // clean up on failure

	args := []string{"clone", "--depth=1"}
	if branch := strings.TrimSpace(body.Branch); branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, repoURL, tmpDir)

	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf("git clone failed: %v\n%s", err, string(output)), http.StatusBadRequest)
		return
	}

	// Verify the clone has an index.html (warn if not, but proceed)
	hasIndex := false
	if _, err := os.Stat(filepath.Join(tmpDir, "index.html")); err == nil {
		hasIndex = true
	}

	// Remove old frontend contents (except .git if present in tmpDir)
	oldEntries, _ := os.ReadDir(m.dir)
	for _, entry := range oldEntries {
		os.RemoveAll(filepath.Join(m.dir, entry.Name()))
	}

	// Move cloned contents into frontend dir
	newEntries, _ := os.ReadDir(tmpDir)
	for _, entry := range newEntries {
		src := filepath.Join(tmpDir, entry.Name())
		dst := filepath.Join(m.dir, entry.Name())
		if err := os.Rename(src, dst); err != nil {
			// Cross-device rename fallback: copy
			if cpErr := copyRecursive(src, dst); cpErr != nil {
				http.Error(w, fmt.Sprintf("failed to move %s: %v", entry.Name(), cpErr), http.StatusInternalServerError)
				return
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "ok",
		"url":       repoURL,
		"has_index": hasIndex,
	})
}

// copyRecursive copies src to dst recursively.
func copyRecursive(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyRecursive(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
