package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func feedRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "cli-bundle", "beta", "linux", "amd64")
	if err := os.MkdirAll(filepath.Join(dir, "1.2.3"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join(dir, "index.json"), `{"schema":"org.spacedatanetwork.update.index.v1"}`)
	write(filepath.Join(dir, "1.2.3", "manifest.json"), `{"schema":"org.spacedatanetwork.update.v1"}`)
	write(filepath.Join(dir, "1.2.3", "update.wasm"), "\x00asm\x01\x00\x00\x00")
	// Two files that must NEVER be served even though an operator's rsync could
	// easily drop them into the tree.
	write(filepath.Join(dir, "deploy-notes.txt"), "operator notes")
	write(filepath.Join(dir, "index.html"), "<html>same-origin content</html>")
	write(filepath.Join(root, "secret.json"), `{"not":"part of a feed path"}`)
	return root
}

func getFeed(t *testing.T, root, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	makeUpdateFeedHandler(root)(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestUpdateFeedServesTheProtocolFiles(t *testing.T) {
	root := feedRoot(t)
	for path, wantType := range map[string]string{
		"/api/v1/updates/cli-bundle/beta/linux/amd64/index.json":          "application/json; charset=utf-8",
		"/api/v1/updates/cli-bundle/beta/linux/amd64/1.2.3/manifest.json": "application/json; charset=utf-8",
		"/api/v1/updates/cli-bundle/beta/linux/amd64/1.2.3/update.wasm":   "application/wasm",
	} {
		rec := getFeed(t, root, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != wantType {
			t.Fatalf("GET %s content-type = %q, want %q", path, got, wantType)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("GET %s did not set nosniff", path)
		}
	}
}

// The allow-list is the point: a stray file in the operator's tree must not
// become fetchable from this node's own origin.
func TestUpdateFeedRefusesEverythingElse(t *testing.T) {
	root := feedRoot(t)
	for _, path := range []string{
		"/api/v1/updates/cli-bundle/beta/linux/amd64/deploy-notes.txt",
		"/api/v1/updates/cli-bundle/beta/linux/amd64/index.html",
		"/api/v1/updates/cli-bundle/beta/linux/amd64", // a directory
		"/api/v1/updates/",                             // the bare root
		"/api/v1/updates/../../../etc/passwd",          // traversal
		"/api/v1/updates/cli-bundle/../../secret.json", // traversal back to root
		"/api/v1/updates/a/b/c/d/e/f/g/too-deep.json",  // depth bound
		"/api/v1/updates/cli-bundle/beta/linux/amd64/nope.json",
	} {
		if rec := getFeed(t, root, path); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestUpdateFeedIsReadOnly(t *testing.T) {
	root := feedRoot(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/updates/cli-bundle/beta/linux/amd64/index.json", nil)
		makeUpdateFeedHandler(root)(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s = %d, want 405", method, rec.Code)
		}
	}
}

// index.json is the only mutable document in the tree; a hard cache on it would
// pin the fleet to whatever release was current when it was first fetched.
func TestUpdateFeedCachePolicy(t *testing.T) {
	root := feedRoot(t)
	if got := getFeed(t, root, "/api/v1/updates/cli-bundle/beta/linux/amd64/index.json").Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index cache-control = %q, want no-cache", got)
	}
	if got := getFeed(t, root, "/api/v1/updates/cli-bundle/beta/linux/amd64/1.2.3/update.wasm").Header().Get("Cache-Control"); got == "no-cache" {
		t.Fatal("versioned payloads should be cacheable")
	}
}

func TestUpdateFeedRelPathRejectsHiddenAndEmptySegments(t *testing.T) {
	for _, path := range []string{
		"/api/v1/updates/.git/config.json",
		"/api/v1/updates/cli-bundle/.hidden/index.json",
		// NOTE: "cli-bundle//index.json" is deliberately NOT here. path.Clean
		// collapses the empty segment, and http.ServeMux cleans the path before
		// dispatch anyway, so it never reaches this function in production.
		"/api/v1/updates/cli-bundle/beta/linux/amd64/index.json\x00.txt",
		"/somewhere/else/index.json",
	} {
		if _, ok := updateFeedRelPath(path); ok {
			t.Fatalf("updateFeedRelPath(%q) accepted", path)
		}
	}
}

// The digest guard on the manifest-signing door. It cannot refuse JSON bodies
// the way the module door does, so the header/query positions carry more weight.
func TestUpdateSigningRefusesDigestPositions(t *testing.T) {
	for _, target := range []string{
		"/api/v1/admin/updates/sign-manifest?content_hash=abc",
		"/api/v1/admin/updates/sign-manifest?sha256=abc",
		"/api/v1/admin/updates/sign-manifest?digest=abc",
	} {
		req := httptest.NewRequest(http.MethodPost, target, nil)
		if _, _, bad := digestBearingUpdateRequest(req); !bad {
			t.Fatalf("%s was not refused", target)
		}
	}
	for _, header := range []string{"X-Sdn-Content-Hash", "Digest", "X-Signed-Hash"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/updates/sign-manifest", nil)
		req.Header.Set(header, "deadbeef")
		if _, _, bad := digestBearingUpdateRequest(req); !bad {
			t.Fatalf("header %s was not refused", header)
		}
	}

	// A wasm body belongs at the module door, under a different domain.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/updates/sign-manifest", nil)
	req.Header.Set("Content-Type", "application/wasm")
	if _, _, bad := digestBearingUpdateRequest(req); !bad {
		t.Fatal("a wasm body was accepted at the manifest signing door")
	}

	// A plain JSON manifest submission must pass the guard — this door's whole
	// purpose is signing JSON.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/updates/sign-manifest", nil)
	req.Header.Set("Content-Type", "application/json")
	if _, _, bad := digestBearingUpdateRequest(req); bad {
		t.Fatal("a JSON manifest body was refused at the manifest signing door")
	}
}
