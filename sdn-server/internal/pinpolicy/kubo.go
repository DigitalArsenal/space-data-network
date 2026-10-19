package pinpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrNoKubo is every Kubo call on a node without a Kubo RPC API.
var ErrNoKubo = errors.New("IPFS is not configured on this node")

const maxKuboErrorBody = 4 * 1024

// kuboClient is the node's one connector to Kubo's pin surface.
type kuboClient struct {
	apiURL string
	http   *http.Client
}

func newKuboClient(apiURL string) *kuboClient {
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if apiURL == "" {
		return nil
	}
	return &kuboClient{apiURL: apiURL, http: &http.Client{}}
}

// post issues one Kubo RPC command and returns its whole body.
func (k *kuboClient) post(ctx context.Context, command string, query url.Values, body io.Reader, contentType string) ([]byte, error) {
	if k == nil {
		return nil, ErrNoKubo
	}
	endpoint := k.apiURL + command
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("kubo %s: %w", command, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Kubo refuses browser-like user agents.
	req.Header.Set("User-Agent", "")
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kubo %s: %w", command, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kubo %s: read: %w", command, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := data
		if len(message) > maxKuboErrorBody {
			message = message[:maxKuboErrorBody]
		}
		return nil, fmt.Errorf("kubo %s: HTTP %d: %s", command, resp.StatusCode, strings.TrimSpace(string(message)))
	}
	if streamErr := strings.TrimSpace(resp.Trailer.Get("X-Stream-Error")); streamErr != "" {
		return nil, fmt.Errorf("kubo %s: %s", command, streamErr)
	}
	return data, nil
}

// blockPut stores data as one CIDv1 raw block (sha2-256).
func (k *kuboClient) blockPut(ctx context.Context, data []byte, pin bool) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("data", "block")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	query := url.Values{
		"cid-codec": {"raw"},
		"mhtype":    {"sha2-256"},
		"mhlen":     {"-1"},
		"pin":       {strconv.FormatBool(pin)},
	}
	resp, err := k.post(ctx, "/api/v0/block/put", query, &body, writer.FormDataContentType())
	if err != nil {
		return "", err
	}
	var result struct {
		Key string `json:"Key"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("kubo block/put: decode: %w", err)
	}
	if strings.TrimSpace(result.Key) == "" {
		return "", errors.New("kubo block/put: answer names no CID")
	}
	return strings.TrimSpace(result.Key), nil
}

// AddOptions shapes a UnixFS add.
type AddOptions struct {
	// CIDv1 adds as CIDv1, raw leaves, sha2-256 (the node's content
	// addressing); false keeps Kubo's defaults.
	CIDv1 bool
	// Chunker is Kubo's chunker ("size-262144"); empty is Kubo's default.
	Chunker string
	// OnlyHash computes the CID without storing or pinning anything.
	OnlyHash bool
	// NoPin adds without pinning even where the policy would pin.
	NoPin bool
}

// Added is the outcome of an add.
type Added struct {
	CID    string
	Size   int64
	Pinned bool
	// Raw is Kubo's answer for the root, for callers that proxy it.
	Raw []byte
}

func (k *kuboClient) add(ctx context.Context, name string, content io.Reader, opts AddOptions, pin bool) (Added, error) {
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		part, err := form.CreateFormFile("file", name)
		if err == nil {
			_, err = io.Copy(part, content)
		}
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	query := url.Values{
		"pin":                 {strconv.FormatBool(pin && !opts.OnlyHash)},
		"wrap-with-directory": {"false"},
		"progress":            {"false"},
	}
	if opts.CIDv1 {
		query.Set("cid-version", "1")
		query.Set("raw-leaves", "true")
		query.Set("hash", "sha2-256")
	}
	if opts.Chunker != "" {
		query.Set("chunker", opts.Chunker)
	}
	if opts.OnlyHash {
		query.Set("only-hash", "true")
	}
	resp, err := k.post(ctx, "/api/v0/add", query, reader, form.FormDataContentType())
	_ = reader.Close()
	if err != nil {
		return Added{}, err
	}
	var out Added
	for _, line := range bytes.Split(resp, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var result struct {
			Hash string `json:"Hash"`
			Size string `json:"Size"`
		}
		if err := json.Unmarshal(line, &result); err != nil {
			return Added{}, fmt.Errorf("kubo add: decode: %w", err)
		}
		if strings.TrimSpace(result.Hash) == "" {
			continue
		}
		out.CID = strings.TrimSpace(result.Hash)
		out.Size, _ = strconv.ParseInt(strings.TrimSpace(result.Size), 10, 64)
		out.Raw = append([]byte(nil), line...)
	}
	if out.CID == "" {
		return Added{}, errors.New("kubo add: answer names no CID")
	}
	out.Pinned = pin && !opts.OnlyHash
	return out, nil
}

func (k *kuboClient) addFile(ctx context.Context, path string, opts AddOptions, pin bool) (Added, error) {
	file, err := os.Open(path)
	if err != nil {
		return Added{}, err
	}
	defer file.Close()
	return k.add(ctx, filepath.Base(path), file, opts, pin)
}

// pinAdd pins cid recursively. offline pins only from blocks the node holds.
func (k *kuboClient) pinAdd(ctx context.Context, cid string, offline bool) error {
	query := url.Values{"arg": {cid}, "recursive": {"true"}}
	if offline {
		query.Set("offline", "true")
	}
	_, err := k.post(ctx, "/api/v0/pin/add", query, nil, "")
	return err
}

// pinRm removes a recursive pin; a CID that is not pinned is already done.
func (k *kuboClient) pinRm(ctx context.Context, cid string) error {
	_, err := k.post(ctx, "/api/v0/pin/rm", url.Values{"arg": {cid}, "recursive": {"true"}}, nil, "")
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "not pinned") {
		return nil
	}
	return err
}

// pinned reports a recursive pin on cid.
func (k *kuboClient) pinned(ctx context.Context, cid string) (bool, error) {
	_, err := k.post(ctx, "/api/v0/pin/ls", url.Values{"arg": {cid}, "type": {"recursive"}}, nil, "")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not pinned") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
