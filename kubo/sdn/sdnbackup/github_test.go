package sdnbackup_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ipfs/kubo/sdn/sdnbackup"
)

// stubSecrets is a SecretsGetter that returns a fixed fake PAT — proving the
// adapter never needs a live credential to shape correct requests.
type stubSecrets struct{ token string }

func (s stubSecrets) Get(ctx context.Context, lane string) (string, string, error) {
	return "", s.token, nil
}

// fakeGitHub is a minimal in-memory GitHub Contents API: it stores PUT bodies
// keyed by path, serves them back on GET, 404s the unknown, and records every
// request so the test can assert request shaping.
type fakeGitHub struct {
	mu      sync.Mutex
	objects map[string]string // path -> base64 of the stored $MBL envelope
	reqs    []recordedReq
}

type recordedReq struct {
	Method string
	Path   string
	Auth   string
	Accept string
	APIVer string
	Body   []byte
}

func newFakeGitHub() *fakeGitHub { return &fakeGitHub{objects: map[string]string{}} }

func (f *fakeGitHub) contentsKey(path string) string {
	const marker = "/contents/"
	i := strings.Index(path, marker)
	if i < 0 {
		return ""
	}
	return path[i+len(marker):]
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, recordedReq{
		Method: r.Method,
		Path:   r.URL.Path,
		Auth:   r.Header.Get("Authorization"),
		Accept: r.Header.Get("Accept"),
		APIVer: r.Header.Get("X-GitHub-Api-Version"),
		Body:   body,
	})
	f.mu.Unlock()

	key := f.contentsKey(r.URL.Path)
	switch r.Method {
	case http.MethodGet:
		f.mu.Lock()
		content, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"content": content, "encoding": "base64", "sha": "sha-" + key, "size": len(content),
		})
	case http.MethodPut:
		var in struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(body, &in)
		f.mu.Lock()
		f.objects[key] = in.Content
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"content": map[string]interface{}{"sha": "sha-" + key, "path": key, "size": len(in.Content)},
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeGitHub) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.Method == method {
			n++
		}
	}
	return n
}

func (f *fakeGitHub) lastPut() (recordedReq, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if f.reqs[i].Method == http.MethodPut {
			return f.reqs[i], true
		}
	}
	return recordedReq{}, false
}

// TestGitHubAdapterRequestShaping proves the HTTPS adapter constructs correct
// requests (method, URL/object key, auth header, base64 body) and round-trips a
// blob — all against a fake server, with only a stub credential.
func TestGitHubAdapterRequestShaping(t *testing.T) {
	ctx := context.Background()
	fake := newFakeGitHub()
	server := httptest.NewServer(fake)
	defer server.Close()

	adapter, err := sdnbackup.NewGitHubAdapter(sdnbackup.GitHubConfig{
		Owner:   "me",
		Repo:    "sdn-backup",
		Branch:  "main",
		BaseURL: server.URL,
	}, stubSecrets{token: "ghp_faketoken"}, server.Client())
	if err != nil {
		t.Fatalf("new github adapter: %v", err)
	}

	payload := []byte("\x00asm\x01\x00\x00\x00; a module to back up to github")
	blob := sdnbackup.BackupBlob{
		ContentHash: sdnbackup.HashBytes(payload),
		Kind:        sdnbackup.KindModuleWASM,
		Meta:        sdnbackup.Meta{PluginID: "com.orbpro.mod-a", Name: "Module A", Version: "1.0.0", Enabled: true},
		Bytes:       payload,
	}

	// --- Put: existence GET (404) then create PUT. ---
	ack, err := adapter.Put(ctx, blob)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if ack.AlreadyPresent {
		t.Fatal("first put reported already present")
	}

	wantKey, _ := sdnbackup.ObjectKey(sdnbackup.KindModuleWASM, blob.ContentHash)
	if ack.ProviderKey != wantKey {
		t.Fatalf("provider key = %q, want %q", ack.ProviderKey, wantKey)
	}

	put, ok := fake.lastPut()
	if !ok {
		t.Fatal("no PUT recorded")
	}
	wantPath := "/repos/me/sdn-backup/contents/" + wantKey
	if put.Path != wantPath {
		t.Fatalf("PUT path = %q, want %q", put.Path, wantPath)
	}
	if put.Auth != "token ghp_faketoken" {
		t.Fatalf("Authorization = %q, want %q", put.Auth, "token ghp_faketoken")
	}
	if put.Accept != "application/vnd.github+json" {
		t.Fatalf("Accept = %q", put.Accept)
	}
	if put.APIVer != "2022-11-28" {
		t.Fatalf("X-GitHub-Api-Version = %q", put.APIVer)
	}
	// The PUT body's base64 content must decode to the blob's $MBL envelope, and
	// that envelope must decode back to the exact blob.
	var putBody struct {
		Content string `json:"content"`
		Branch  string `json:"branch"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(put.Body, &putBody); err != nil {
		t.Fatalf("decode put body: %v", err)
	}
	if putBody.Branch != "main" {
		t.Fatalf("PUT branch = %q, want main", putBody.Branch)
	}
	env, err := base64.StdEncoding.DecodeString(putBody.Content)
	if err != nil {
		t.Fatalf("PUT content not base64: %v", err)
	}
	decoded, err := sdnbackup.BlobFromMBL(env)
	if err != nil {
		t.Fatalf("PUT content is not a backup blob $MBL: %v", err)
	}
	if decoded.ContentHash != blob.ContentHash || string(decoded.Bytes) != string(payload) {
		t.Fatalf("PUT body blob mismatch: got hash %s", decoded.ContentHash)
	}
	if decoded.Meta.PluginID != "com.orbpro.mod-a" {
		t.Fatalf("PUT body meta lost: %+v", decoded.Meta)
	}

	// --- Has: present. ---
	pres, err := adapter.Has(ctx, blob.Ref())
	if err != nil {
		t.Fatalf("has: %v", err)
	}
	if !pres.Present {
		t.Fatal("has reported absent after put")
	}

	// --- Get: round-trip the bytes. ---
	got, err := adapter.Get(ctx, blob.Ref())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got.Bytes) != string(payload) {
		t.Fatal("get bytes differ from original")
	}
	if h := sdnbackup.HashBytes(got.Bytes); h != blob.ContentHash {
		t.Fatalf("get bytes hash %s != %s", h, blob.ContentHash)
	}

	// --- Idempotent second put: existence GET only, no new PUT. ---
	putsBefore := fake.count(http.MethodPut)
	ack2, err := adapter.Put(ctx, blob)
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if !ack2.AlreadyPresent {
		t.Fatal("idempotent second put not reported as already present")
	}
	if fake.count(http.MethodPut) != putsBefore {
		t.Fatal("idempotent second put issued a new PUT (should be existence-check only)")
	}
}

// TestGitHubAdapterAuthFailure proves a missing credential surfaces a typed
// auth_failed error, not a silent success.
func TestGitHubAdapterAuthFailure(t *testing.T) {
	ctx := context.Background()
	fake := newFakeGitHub()
	server := httptest.NewServer(fake)
	defer server.Close()

	adapter, err := sdnbackup.NewGitHubAdapter(sdnbackup.GitHubConfig{
		Owner: "me", Repo: "sdn-backup", BaseURL: server.URL,
	}, stubSecrets{token: ""}, server.Client())
	if err != nil {
		t.Fatalf("new github adapter: %v", err)
	}
	payload := []byte("payload")
	_, err = adapter.Put(ctx, sdnbackup.BackupBlob{
		ContentHash: sdnbackup.HashBytes(payload), Kind: sdnbackup.KindModuleWASM, Bytes: payload,
	})
	if sdnbackup.CodeOf(err) != sdnbackup.ErrAuthFailed {
		t.Fatalf("error code = %q, want auth_failed (err=%v)", sdnbackup.CodeOf(err), err)
	}
}
