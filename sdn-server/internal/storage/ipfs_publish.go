package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// PublishedDatasetExport records the IPFS CIDs returned for exported bytes.
type PublishedDatasetExport struct {
	ShardCID    string
	IndexCID    string
	ManifestCID string
}

// PublishDatasetExportToIPFS pins exported shard and index bytes through a Kubo RPC API.
func PublishDatasetExportToIPFS(ctx context.Context, ipfsAPIURL string, export *DatasetExport) (*PublishedDatasetExport, error) {
	if export == nil {
		return nil, fmt.Errorf("dataset export is required")
	}
	if strings.TrimSpace(ipfsAPIURL) == "" {
		return nil, fmt.Errorf("ipfs api url is required")
	}

	shardCID, err := pinUnixFSFile(ctx, ipfsAPIURL, export.ShardPath, export.ShardCID)
	if err != nil {
		return nil, fmt.Errorf("pin shard: %w", err)
	}
	indexCID, err := pinUnixFSFile(ctx, ipfsAPIURL, export.IndexPath, export.IndexCID)
	if err != nil {
		return nil, fmt.Errorf("pin index: %w", err)
	}
	return &PublishedDatasetExport{
		ShardCID: shardCID,
		IndexCID: indexCID,
	}, nil
}

// PublishDatasetPublicationManifestToIPFS pins a signed dataset manifest through a Kubo RPC API.
func PublishDatasetPublicationManifestToIPFS(ctx context.Context, ipfsAPIURL string, manifest *DatasetPublicationManifest) (string, error) {
	if manifest == nil {
		return "", fmt.Errorf("dataset publication manifest is required")
	}
	manifestCID, err := pinRawBlock(ctx, ipfsAPIURL, manifest.Path, manifest.CID)
	if err != nil {
		return "", fmt.Errorf("pin manifest: %w", err)
	}
	return manifestCID, nil
}

// FetchIPFSBlockByCID fetches immutable bytes from a Kubo RPC API. The CID may
// be either a raw block CID or a chunked UnixFS file root CID.
func FetchIPFSBlockByCID(ctx context.Context, ipfsAPIURL, cidValue string) ([]byte, error) {
	if strings.TrimSpace(ipfsAPIURL) == "" {
		return nil, fmt.Errorf("ipfs api url is required")
	}
	cidValue = strings.TrimSpace(cidValue)
	if cidValue == "" {
		return nil, fmt.Errorf("cid is required")
	}
	endpoint, err := url.JoinPath(strings.TrimRight(ipfsAPIURL, "/"), "/api/v0/cat")
	if err != nil {
		return nil, fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse IPFS URL: %w", err)
	}
	query := reqURL.Query()
	query.Set("arg", cidValue)
	reqURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create IPFS request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post IPFS cat: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read IPFS CID bytes: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("IPFS cat failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func pinUnixFSFile(ctx context.Context, ipfsAPIURL, path, expectedRawCID string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	localCID, err := cidV1RawSHA256File(path)
	if err != nil {
		return "", fmt.Errorf("compute local raw CID: %w", err)
	}
	if expectedRawCID != "" && localCID != expectedRawCID {
		return "", fmt.Errorf("local raw CID %s does not match expected CID %s", localCID, expectedRawCID)
	}

	endpoint, err := url.JoinPath(strings.TrimRight(ipfsAPIURL, "/"), "/api/v0/add")
	if err != nil {
		return "", fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse IPFS URL: %w", err)
	}
	query := reqURL.Query()
	query.Set("pin", "true")
	query.Set("cid-version", "1")
	query.Set("raw-leaves", "true")
	query.Set("hash", "sha2-256")
	query.Set("wrap-with-directory", "false")
	query.Set("progress", "false")
	reqURL.RawQuery = query.Encode()

	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	go func() {
		var writeErr error
		defer func() {
			if writeErr != nil {
				_ = writer.CloseWithError(writeErr)
				return
			}
			if err := multipartWriter.Close(); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
			_ = writer.Close()
		}()
		part, err := multipartWriter.CreateFormFile("file", filepath.Base(path))
		if err != nil {
			writeErr = fmt.Errorf("create IPFS multipart field: %w", err)
			return
		}
		file, err := os.Open(path)
		if err != nil {
			writeErr = fmt.Errorf("open %s: %w", path, err)
			return
		}
		defer file.Close()
		if _, err := io.Copy(part, file); err != nil {
			writeErr = fmt.Errorf("write IPFS multipart field: %w", err)
			return
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), reader)
	if err != nil {
		_ = reader.Close()
		return "", fmt.Errorf("create IPFS request: %w", err)
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("post IPFS add: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read IPFS response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("IPFS add failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	var cidValue string
	for {
		var result struct {
			Hash string `json:"Hash"`
			CID  string `json:"Cid"`
		}
		if err := decoder.Decode(&result); err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("decode IPFS add response: %w", err)
		}
		if value := strings.TrimSpace(result.Hash); value != "" {
			cidValue = value
		} else if value := strings.TrimSpace(result.CID); value != "" {
			cidValue = value
		}
	}
	if cidValue == "" {
		return "", fmt.Errorf("IPFS add response missing CID")
	}
	return cidValue, nil
}

func pinRawBlock(ctx context.Context, ipfsAPIURL, path, expectedCID string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	localCID, err := cidV1RawSHA256(data)
	if err != nil {
		return "", fmt.Errorf("compute local CID: %w", err)
	}
	if expectedCID != "" && localCID != expectedCID {
		return "", fmt.Errorf("local CID %s does not match expected CID %s", localCID, expectedCID)
	}

	endpoint, err := url.JoinPath(strings.TrimRight(ipfsAPIURL, "/"), "/api/v0/block/put")
	if err != nil {
		return "", fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse IPFS URL: %w", err)
	}
	query := reqURL.Query()
	query.Set("format", "raw")
	query.Set("mhtype", "sha2-256")
	query.Set("mhlen", "-1")
	query.Set("pin", "true")
	reqURL.RawQuery = query.Encode()

	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	part, err := writer.CreateFormFile("data", filepath.Base(path))
	if err != nil {
		return "", fmt.Errorf("create IPFS multipart field: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("write IPFS multipart field: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close IPFS multipart body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), &multipartBody)
	if err != nil {
		return "", fmt.Errorf("create IPFS request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("post IPFS block: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read IPFS response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("IPFS block put failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Key string `json:"Key"`
		CID string `json:"Cid"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode IPFS response: %w", err)
	}
	cidValue := strings.TrimSpace(result.Key)
	if cidValue == "" {
		cidValue = strings.TrimSpace(result.CID)
	}
	if cidValue == "" {
		return "", fmt.Errorf("IPFS response missing CID")
	}
	if cidValue != localCID {
		return "", fmt.Errorf("IPFS CID %s does not match local CID %s", cidValue, localCID)
	}
	return cidValue, nil
}

func cidV1RawSHA256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	hash, err := mh.Encode(hasher.Sum(nil), mh.SHA2_256)
	if err != nil {
		return "", err
	}
	return cid.NewCidV1(cid.Raw, hash).String(), nil
}
