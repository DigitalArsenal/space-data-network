package frontend

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func setupTestManager(t *testing.T) (*Manager, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	// Write a test file
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>hello</h1>"), 0644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)
	mux := http.NewServeMux()
	m.RegisterRoutes(mux)
	return m, mux
}

func TestListFiles(t *testing.T) {
	_, mux := setupTestManager(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/frontend/files", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var files []FileEntry
	if err := json.Unmarshal(w.Body.Bytes(), &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != "index.html" {
		t.Errorf("expected index.html, got %s", files[0].Path)
	}
}

func TestGetFile(t *testing.T) {
	_, mux := setupTestManager(t)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/frontend/files/index.html", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["content"] != "<h1>hello</h1>" {
		t.Errorf("unexpected content: %v", result["content"])
	}
}

func TestPutFile(t *testing.T) {
	_, mux := setupTestManager(t)

	body := `{"content":"<h1>updated</h1>"}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/frontend/files/index.html", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify
	req2 := httptest.NewRequest(http.MethodGet, "/api/admin/frontend/files/index.html", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	var result map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &result)
	if result["content"] != "<h1>updated</h1>" {
		t.Errorf("put did not persist: got %v", result["content"])
	}
}

func TestDeleteFile(t *testing.T) {
	_, mux := setupTestManager(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/frontend/files/index.html", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify gone
	req2 := httptest.NewRequest(http.MethodGet, "/api/admin/frontend/files/index.html", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", w2.Code)
	}
}

func TestPathTraversalBlocked(t *testing.T) {
	m, _ := setupTestManager(t)

	// Test the handler directly to bypass ServeMux URL normalization
	mux := http.NewServeMux()
	m.RegisterRoutes(mux)

	// %2e%2e is URL-encoded ".." — ServeMux decodes this before routing
	// so we test that the handler rejects paths that resolve outside the dir.
	// Since Go's net/http normalizes paths, we test the inner logic directly:
	// a relative path like "foo/../../etc/passwd" cleans to "../etc/passwd".
	req := httptest.NewRequest(http.MethodGet, "/api/admin/frontend/files/foo/..%2f..%2fetc%2fpasswd", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Should either be 400 (bad path) or 404 (file not found), never 200 with external content
	if w.Code == http.StatusOK {
		t.Errorf("expected non-200 for path traversal attempt, got %d", w.Code)
	}
}

func TestUpload(t *testing.T) {
	_, mux := setupTestManager(t)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("files", "style.css")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(part, "body { color: red; }")
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/frontend/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify file exists
	req2 := httptest.NewRequest(http.MethodGet, "/api/admin/frontend/files/style.css", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("uploaded file not found: %d", w2.Code)
	}
}

func TestCreateSubdirectoryFile(t *testing.T) {
	_, mux := setupTestManager(t)

	body := `{"content":"/* main */\n"}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/frontend/files/css/main.css", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify in listing
	req2 := httptest.NewRequest(http.MethodGet, "/api/admin/frontend/files", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	var files []FileEntry
	json.Unmarshal(w2.Body.Bytes(), &files)

	found := false
	for _, f := range files {
		if f.Path == filepath.Join("css", "main.css") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("css/main.css not found in listing: %+v", files)
	}
}

func TestMoveFile(t *testing.T) {
	_, mux := setupTestManager(t)

	body := `{"from":"index.html","to":"pages/home.html"}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/frontend/move", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	oldReq := httptest.NewRequest(http.MethodGet, "/api/admin/frontend/files/index.html", nil)
	oldRec := httptest.NewRecorder()
	mux.ServeHTTP(oldRec, oldReq)
	if oldRec.Code != http.StatusNotFound {
		t.Fatalf("expected old path to be gone, got %d: %s", oldRec.Code, oldRec.Body.String())
	}

	newReq := httptest.NewRequest(http.MethodGet, "/api/admin/frontend/files/pages/home.html", nil)
	newRec := httptest.NewRecorder()
	mux.ServeHTTP(newRec, newReq)
	if newRec.Code != http.StatusOK {
		t.Fatalf("expected new path to exist, got %d: %s", newRec.Code, newRec.Body.String())
	}
}

// TestGitCloneArgsRejectsDashPrefixedBranch locks in the fix for git
// argument injection via the git-import branch field: a branch value that
// looks like a flag (e.g. "--upload-pack=...") must never reach git's argv
// as a positional argument.
func TestGitCloneArgsRejectsDashPrefixedBranch(t *testing.T) {
	if _, err := gitCloneArgs("https://example.com/repo.git", "--upload-pack=touch /tmp/pwned", "/tmp/dst"); err == nil {
		t.Fatal("expected error for dash-prefixed branch, got nil")
	}
}

// TestGitCloneArgsRejectsDashPrefixedURL is the same regression check for
// the URL field.
func TestGitCloneArgsRejectsDashPrefixedURL(t *testing.T) {
	if _, err := gitCloneArgs("--upload-pack=touch /tmp/pwned", "", "/tmp/dst"); err == nil {
		t.Fatal("expected error for dash-prefixed URL, got nil")
	}
}

// TestGitCloneArgsInsertsEndOfOptionsMarker locks in that legitimate
// requests still produce a well-formed argv, and that "--" always precedes
// the positional repoURL/dstDir arguments as defense in depth against any
// future loosening of the dash-prefix checks.
func TestGitCloneArgsInsertsEndOfOptionsMarker(t *testing.T) {
	args, err := gitCloneArgs("https://example.com/repo.git", "main", "/tmp/dst")
	if err != nil {
		t.Fatalf("gitCloneArgs: %v", err)
	}
	want := []string{"clone", "--depth=1", "--branch", "main", "--", "https://example.com/repo.git", "/tmp/dst"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}
}

// TestGitImportRejectsDashPrefixedBranch is the end-to-end regression check
// through the HTTP handler: the git-import endpoint must reject a
// flag-like branch with 400 rather than shelling out to git with it.
func TestGitImportRejectsDashPrefixedBranch(t *testing.T) {
	_, mux := setupTestManager(t)

	body := `{"url":"https://example.com/nonexistent-repo.git","branch":"--upload-pack=touch /tmp/pwned"}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/frontend/git-import", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestGitImportRejectsDashPrefixedURL is the end-to-end regression check for
// the URL field. A URL beginning with "-" also fails the earlier
// scheme-prefix check, so this doubles as coverage for that guard too.
func TestGitImportRejectsDashPrefixedURL(t *testing.T) {
	_, mux := setupTestManager(t)

	body := `{"url":"--upload-pack=touch /tmp/pwned"}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/frontend/git-import", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
