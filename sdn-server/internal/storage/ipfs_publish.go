package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

	shardCID, err := pinRawBlock(ctx, ipfsAPIURL, export.ShardPath, export.ShardCID)
	if err != nil {
		return nil, fmt.Errorf("pin shard: %w", err)
	}
	indexCID, err := pinRawBlock(ctx, ipfsAPIURL, export.IndexPath, export.IndexCID)
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
