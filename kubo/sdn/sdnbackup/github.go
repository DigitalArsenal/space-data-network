package sdnbackup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// SecretsGetter is the credential handshake an adapter uses (spec A.2): it
// resolves a secrets lane to {username, secret}. In production it wraps the
// capability-gated secrets host store (sdn/credstore via the secrets cap), so
// the credential never touches config or the wire; in tests it is a stub. An
// adapter never enumerates or exports creds — it only asks for its one lane.
type SecretsGetter interface {
	Get(ctx context.Context, lane string) (username, secret string, err error)
}

// HTTPDoer is the outbound HTTP surface an HTTPS adapter uses (the spec's http
// host cap, A.1/3.1). A real *http.Client satisfies it; a test supplies an
// httptest client so request shaping is verified without live creds.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// GitHubConfig configures the GitHub Contents-API adapter. No secret lives
// here — CredentialLane names the secrets lane the PAT is fetched from.
type GitHubConfig struct {
	Owner          string
	Repo           string
	Branch         string // default "main"
	BaseURL        string // default "https://api.github.com"
	CredentialLane string // default "github"
	CommitAuthor   string // commit-message provenance; default "sdn-backup"
	ProviderID     string // default "github"
}

// GitHubAdapter stores backup blobs as files in a GitHub repository via the
// REST v3 Contents API (spec B): object path = sdn-backup/<kind>/<hh>/<hash>,
// body = base64 of the blob's $MBL envelope. It rides plain http + secrets — no
// new host capability. Immutable-by-hash naming makes put idempotent; Put
// existence-checks first so a re-run is a no-op.
type GitHubAdapter struct {
	cfg     GitHubConfig
	secrets SecretsGetter
	doer    HTTPDoer
}

var _ Adapter = (*GitHubAdapter)(nil)

// NewGitHubAdapter builds a GitHub adapter. secrets and doer are required;
// owner/repo are required; branch/baseURL/lane default.
func NewGitHubAdapter(cfg GitHubConfig, secrets SecretsGetter, doer HTTPDoer) (*GitHubAdapter, error) {
	if strings.TrimSpace(cfg.Owner) == "" || strings.TrimSpace(cfg.Repo) == "" {
		return nil, fmt.Errorf("sdnbackup: github adapter requires owner and repo")
	}
	if secrets == nil {
		return nil, fmt.Errorf("sdnbackup: github adapter requires a SecretsGetter")
	}
	if doer == nil {
		return nil, fmt.Errorf("sdnbackup: github adapter requires an HTTPDoer")
	}
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.github.com"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.CredentialLane == "" {
		cfg.CredentialLane = "github"
	}
	if cfg.CommitAuthor == "" {
		cfg.CommitAuthor = "sdn-backup"
	}
	if cfg.ProviderID == "" {
		cfg.ProviderID = "github"
	}
	return &GitHubAdapter{cfg: cfg, secrets: secrets, doer: doer}, nil
}

func (g *GitHubAdapter) Describe(ctx context.Context) (AdapterDescriptor, error) {
	return AdapterDescriptor{
		ProviderID: g.cfg.ProviderID,
		Capabilities: AdapterCapabilities{
			Put: true, Get: true, Has: true, List: true, Delete: true,
			Versioning: true, NativeHash: true,
		},
		// GitHub Contents API caps ~100 MB, and the http host cap ceiling is
		// 100 MB (spec B / F-9); oversize units are a documented follow-up.
		MaxBlobSize:      100 << 20,
		CredentialLane:   CapabilityLane(g.cfg.CredentialLane),
		AddressingScheme: "content-hash/sdn-backup",
	}, nil
}

// CapabilityLane names the secrets capability grant for a lane, e.g.
// "secrets:github" (matches modulert.SecretsCapabilityPrefix).
func CapabilityLane(lane string) string {
	return "secrets:" + strings.TrimSpace(lane)
}

// --- request shaping (pure builders, unit-tested without a live server) ---

func (g *GitHubAdapter) contentsURL(objectKey string) string {
	segs := strings.Split(objectKey, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return fmt.Sprintf("%s/repos/%s/%s/contents/%s", g.cfg.BaseURL,
		url.PathEscape(g.cfg.Owner), url.PathEscape(g.cfg.Repo), strings.Join(segs, "/"))
}

func (g *GitHubAdapter) newRequest(ctx context.Context, method, rawURL, token string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "sdn-backup")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (g *GitHubAdapter) token(ctx context.Context) (string, error) {
	_, secret, err := g.secrets.Get(ctx, g.cfg.CredentialLane)
	if err != nil {
		return "", adapterErr(ErrAuthFailed, "secrets", "fetch lane %q: %v", g.cfg.CredentialLane, err)
	}
	if strings.TrimSpace(secret) == "" {
		return "", adapterErr(ErrAuthFailed, "secrets", "lane %q has no token configured", g.cfg.CredentialLane)
	}
	return secret, nil
}

// ghContentGet is the subset of the GitHub Contents API GET response we read.
type ghContentGet struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	SHA      string `json:"sha"`
	Size     int    `json:"size"`
}

// ghContentPut is the subset of the create/update response we read.
type ghContentPut struct {
	Content struct {
		SHA  string `json:"sha"`
		Path string `json:"path"`
		Size int    `json:"size"`
	} `json:"content"`
}

func (g *GitHubAdapter) Put(ctx context.Context, blob BackupBlob) (PutAck, error) {
	key, err := ObjectKey(blob.Kind, blob.ContentHash)
	if err != nil {
		return PutAck{}, err
	}
	token, err := g.token(ctx)
	if err != nil {
		return PutAck{}, err
	}
	// Idempotent: immutable-by-hash naming means a present key is a byte-identical
	// blob; skip the write and ack.
	if sha, present, err := g.head(ctx, key, token); err != nil {
		return PutAck{}, err
	} else if present {
		return PutAck{ContentHash: blob.ContentHash, ProviderKey: key, ProviderVersionID: sha, AlreadyPresent: true}, nil
	}

	env, err := BlobToMBL(blob)
	if err != nil {
		return PutAck{}, err
	}
	body, err := json.Marshal(map[string]string{
		"message": fmt.Sprintf("sdn-backup %s %s", blob.Kind, blob.ContentHash),
		"content": base64.StdEncoding.EncodeToString(env),
		"branch":  g.cfg.Branch,
	})
	if err != nil {
		return PutAck{}, err
	}
	req, err := g.newRequest(ctx, http.MethodPut, g.contentsURL(key), token, body)
	if err != nil {
		return PutAck{}, err
	}
	resp, err := g.doer.Do(req)
	if err != nil {
		return PutAck{}, adapterErr(ErrProvider, "put", "%v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		var out ghContentPut
		_ = json.Unmarshal(respBody, &out)
		return PutAck{ContentHash: blob.ContentHash, ProviderKey: key, ProviderVersionID: out.Content.SHA, SizeStored: len(env)}, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return PutAck{}, adapterErr(ErrAuthFailed, "put", "github %d: %s", resp.StatusCode, snippet(respBody))
	case http.StatusUnprocessableEntity:
		// Typically "sha wasn't supplied" for an existing path — a concurrent
		// writer landed the same immutable blob; treat as present.
		return PutAck{ContentHash: blob.ContentHash, ProviderKey: key, AlreadyPresent: true}, nil
	default:
		return PutAck{}, adapterErr(ErrProvider, "put", "github %d: %s", resp.StatusCode, snippet(respBody))
	}
}

func (g *GitHubAdapter) Get(ctx context.Context, ref BlobRef) (BackupBlob, error) {
	key, err := g.keyForRef(ref)
	if err != nil {
		return BackupBlob{}, err
	}
	token, err := g.token(ctx)
	if err != nil {
		return BackupBlob{}, err
	}
	rawURL := g.contentsURL(key) + "?ref=" + url.QueryEscape(g.cfg.Branch)
	req, err := g.newRequest(ctx, http.MethodGet, rawURL, token, nil)
	if err != nil {
		return BackupBlob{}, err
	}
	resp, err := g.doer.Do(req)
	if err != nil {
		return BackupBlob{}, adapterErr(ErrProvider, "get", "%v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		var out ghContentGet
		if err := json.Unmarshal(respBody, &out); err != nil {
			return BackupBlob{}, adapterErr(ErrProvider, "get", "decode response: %v", err)
		}
		if out.Encoding != "base64" || out.Content == "" {
			// Files over ~1 MB return empty content here and need the blob API.
			return BackupBlob{}, adapterErr(ErrUnsupported, "get", "content not inline (encoding=%q, size=%d); large-object fetch is a follow-up", out.Encoding, out.Size)
		}
		env, err := base64.StdEncoding.DecodeString(stripWhitespace(out.Content))
		if err != nil {
			return BackupBlob{}, adapterErr(ErrProvider, "get", "decode base64 content: %v", err)
		}
		return BlobFromMBL(env)
	case http.StatusNotFound:
		return BackupBlob{}, adapterErr(ErrNotFound, "get", "github 404 for %s", key)
	case http.StatusUnauthorized, http.StatusForbidden:
		return BackupBlob{}, adapterErr(ErrAuthFailed, "get", "github %d", resp.StatusCode)
	default:
		return BackupBlob{}, adapterErr(ErrProvider, "get", "github %d: %s", resp.StatusCode, snippet(respBody))
	}
}

func (g *GitHubAdapter) Has(ctx context.Context, ref BlobRef) (Presence, error) {
	key, err := g.keyForRef(ref)
	if err != nil {
		return Presence{}, err
	}
	token, err := g.token(ctx)
	if err != nil {
		return Presence{}, err
	}
	sha, present, err := g.head(ctx, key, token)
	if err != nil {
		return Presence{}, err
	}
	return Presence{ContentHash: ref.ContentHash, Present: present, ProviderVersionID: sha}, nil
}

// head reports whether an object key exists (and its git blob sha). It uses a
// Contents GET; a cheaper Git-Trees existence probe is a documented follow-up.
func (g *GitHubAdapter) head(ctx context.Context, key, token string) (sha string, present bool, err error) {
	rawURL := g.contentsURL(key) + "?ref=" + url.QueryEscape(g.cfg.Branch)
	req, err := g.newRequest(ctx, http.MethodGet, rawURL, token, nil)
	if err != nil {
		return "", false, err
	}
	resp, err := g.doer.Do(req)
	if err != nil {
		return "", false, adapterErr(ErrProvider, "has", "%v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		var out ghContentGet
		_ = json.Unmarshal(respBody, &out)
		return out.SHA, true, nil
	case http.StatusNotFound:
		return "", false, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", false, adapterErr(ErrAuthFailed, "has", "github %d", resp.StatusCode)
	default:
		return "", false, adapterErr(ErrProvider, "has", "github %d: %s", resp.StatusCode, snippet(respBody))
	}
}

func (g *GitHubAdapter) Delete(ctx context.Context, ref BlobRef) (DeleteAck, error) {
	key, err := g.keyForRef(ref)
	if err != nil {
		return DeleteAck{}, err
	}
	token, err := g.token(ctx)
	if err != nil {
		return DeleteAck{}, err
	}
	sha, present, err := g.head(ctx, key, token)
	if err != nil {
		return DeleteAck{}, err
	}
	if !present {
		return DeleteAck{ContentHash: ref.ContentHash, Deleted: false}, nil
	}
	body, err := json.Marshal(map[string]string{
		"message": fmt.Sprintf("sdn-backup delete %s", ref.ContentHash),
		"sha":     sha,
		"branch":  g.cfg.Branch,
	})
	if err != nil {
		return DeleteAck{}, err
	}
	req, err := g.newRequest(ctx, http.MethodDelete, g.contentsURL(key), token, body)
	if err != nil {
		return DeleteAck{}, err
	}
	resp, err := g.doer.Do(req)
	if err != nil {
		return DeleteAck{}, adapterErr(ErrProvider, "delete", "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return DeleteAck{ContentHash: ref.ContentHash, Deleted: true}, nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	return DeleteAck{}, adapterErr(ErrProvider, "delete", "github %d: %s", resp.StatusCode, snippet(respBody))
}

// ghTree is the subset of the Git Trees API recursive response used for List.
type ghTree struct {
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
		Size int    `json:"size"`
	} `json:"tree"`
	Truncated bool `json:"truncated"`
}

func (g *GitHubAdapter) List(ctx context.Context, q ListQuery) (ListPage, error) {
	token, err := g.token(ctx)
	if err != nil {
		return ListPage{}, err
	}
	rawURL := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1", g.cfg.BaseURL,
		url.PathEscape(g.cfg.Owner), url.PathEscape(g.cfg.Repo), url.PathEscape(g.cfg.Branch))
	req, err := g.newRequest(ctx, http.MethodGet, rawURL, token, nil)
	if err != nil {
		return ListPage{}, err
	}
	resp, err := g.doer.Do(req)
	if err != nil {
		return ListPage{}, adapterErr(ErrProvider, "list", "%v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return ListPage{}, nil // empty repo/branch
	}
	if resp.StatusCode != http.StatusOK {
		return ListPage{}, adapterErr(ErrProvider, "list", "github %d: %s", resp.StatusCode, snippet(respBody))
	}
	var tree ghTree
	if err := json.Unmarshal(respBody, &tree); err != nil {
		return ListPage{}, adapterErr(ErrProvider, "list", "decode tree: %v", err)
	}
	var page ListPage
	for _, e := range tree.Tree {
		if e.Type != "blob" {
			continue
		}
		kind, hash, ok := parseObjectKey(e.Path)
		if !ok {
			continue
		}
		if q.KindFilter != "" && kind != q.KindFilter {
			continue
		}
		if q.Prefix != "" && !strings.HasPrefix(e.Path, q.Prefix) {
			continue
		}
		page.Entries = append(page.Entries, ListEntry{
			ContentHash:       hash,
			Kind:              kind,
			Size:              e.Size,
			ProviderKey:       e.Path,
			ProviderVersionID: e.SHA,
		})
	}
	// The Trees API can truncate very large repos; paging via subtree fetch is a
	// documented follow-up.
	return page, nil
}

func (g *GitHubAdapter) keyForRef(ref BlobRef) (string, error) {
	if ref.Kind == "" {
		return "", adapterErr(ErrUnsupported, "key", "github get/has/delete needs a kind hint for %s (scan-by-hash is not implemented for HTTPS providers)", ref.ContentHash)
	}
	return ObjectKey(ref.Kind, ref.ContentHash)
}

func stripWhitespace(s string) string {
	return strings.NewReplacer("\n", "", "\r", "", " ", "", "\t", "").Replace(s)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
