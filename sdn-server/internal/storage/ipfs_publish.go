package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	car "github.com/ipld/go-car/v2"
	carstorage "github.com/ipld/go-car/v2/storage"
	mh "github.com/multiformats/go-multihash"

	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
)

// PublishedDatasetExport records the IPFS CIDs returned for exported bytes.
type PublishedDatasetExport struct {
	ShardCID    string
	IndexCID    string
	ManifestCID string
}

// PublishedShardGroupCAR records a CARv1 bundle that contains the DAG blocks
// for one or more already-published dataset shard artifacts.
type PublishedShardGroupCAR struct {
	CID       string
	Path      string
	SHA256    string
	ByteCount int64
	RootCIDs  []string
}

const maxKuboCommandErrorBodyBytes = 4 * 1024

// PublishDatasetExportToIPFS adds exported shard and index bytes through the
// node pin policy (pinned when the scope allows).
func PublishDatasetExportToIPFS(ctx context.Context, pins pinpolicy.Scope, export *DatasetExport) (*PublishedDatasetExport, error) {
	if export == nil {
		return nil, fmt.Errorf("dataset export is required")
	}
	shardCID, err := addUnixFSFile(ctx, pins, export.ShardPath, export.ShardCID, "")
	if err != nil {
		return nil, fmt.Errorf("add shard: %w", err)
	}
	indexCID, err := addUnixFSFile(ctx, pins, export.IndexPath, export.IndexCID, "")
	if err != nil {
		return nil, fmt.Errorf("add index: %w", err)
	}
	return &PublishedDatasetExport{
		ShardCID: shardCID,
		IndexCID: indexCID,
	}, nil
}

// PublishDatasetPublicationManifestToIPFS stores a signed dataset manifest as
// a raw block through the node pin policy.
func PublishDatasetPublicationManifestToIPFS(ctx context.Context, pins pinpolicy.Scope, manifest *DatasetPublicationManifest) (string, error) {
	if manifest == nil {
		return "", fmt.Errorf("dataset publication manifest is required")
	}
	manifestCID, err := putRawBlockFile(ctx, pins, manifest.Path, manifest.CID)
	if err != nil {
		return "", fmt.Errorf("put manifest: %w", err)
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

// FetchIPFSBlockByCIDToFile streams immutable bytes from a Kubo RPC API to a
// local file. The CID may be either a raw block CID or a chunked UnixFS file
// root CID.
func FetchIPFSBlockByCIDToFile(ctx context.Context, ipfsAPIURL, cidValue, outputPath string) error {
	if strings.TrimSpace(ipfsAPIURL) == "" {
		return fmt.Errorf("ipfs api url is required")
	}
	cidValue = strings.TrimSpace(cidValue)
	if cidValue == "" {
		return fmt.Errorf("cid is required")
	}
	outputPath = strings.TrimSpace(outputPath)
	if outputPath == "" {
		return fmt.Errorf("output path is required")
	}
	endpoint, err := url.JoinPath(strings.TrimRight(ipfsAPIURL, "/"), "/api/v0/cat")
	if err != nil {
		return fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse IPFS URL: %w", err)
	}
	query := reqURL.Query()
	query.Set("arg", cidValue)
	reqURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create IPFS request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("post IPFS cat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("IPFS cat failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return fmt.Errorf("create IPFS output directory: %w", err)
	}
	tempFile, err := os.CreateTemp(filepath.Dir(outputPath), ".ipfs-cid-*.tmp")
	if err != nil {
		return fmt.Errorf("create IPFS output file: %w", err)
	}
	tempPath := tempFile.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := io.Copy(tempFile, resp.Body); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("write IPFS CID bytes: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close IPFS output file: %w", err)
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		return fmt.Errorf("commit IPFS output file: %w", err)
	}
	committed = true
	return nil
}

// PublishShardGroupCARToIPFS exports the listed root DAGs from Kubo, writes one
// CARv1 with those roots, and adds the resulting CAR file back to IPFS through
// the node pin policy. The root DAGs must already exist in the local Kubo
// blockstore.
func PublishShardGroupCARToIPFS(ctx context.Context, ipfsAPIURL string, pins pinpolicy.Scope, outputDir string, rootCIDs []string) (*PublishedShardGroupCAR, error) {
	if strings.TrimSpace(ipfsAPIURL) == "" {
		return nil, fmt.Errorf("ipfs api url is required")
	}
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return nil, fmt.Errorf("car output dir is required")
	}
	roots, rootCIDStrings, err := normalizeCARRoots(rootCIDs)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("at least one CAR root CID is required")
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return nil, fmt.Errorf("create CAR output dir: %w", err)
	}

	tmp, err := os.CreateTemp(outputDir, "shard-group-*.car.tmp")
	if err != nil {
		return nil, fmt.Errorf("create CAR temp file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	carWriter, err := carstorage.NewWritable(tmp, roots, car.WriteAsCarV1(true))
	if err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("create CAR writer: %w", err)
	}
	for _, root := range rootCIDStrings {
		if err := appendExportedDAGToCAR(ctx, ipfsAPIURL, root, carWriter); err != nil {
			_ = tmp.Close()
			return nil, err
		}
	}
	if err := carWriter.Finalize(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("finalize CAR file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close CAR temp file: %w", err)
	}

	sha256Value, byteCount, err := sha256HexFile(tmpPath)
	if err != nil {
		return nil, err
	}
	finalPath := filepath.Join(outputDir, "shard-group-"+sha256Value[:16]+".car")
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return nil, fmt.Errorf("commit CAR file: %w", err)
	}
	committed = true

	carCID, err := addUnixFSFile(ctx, pins, finalPath, "", "size-1048576")
	if err != nil {
		return nil, fmt.Errorf("add CAR bundle: %w", err)
	}
	return &PublishedShardGroupCAR{
		CID:       carCID,
		Path:      finalPath,
		SHA256:    sha256Value,
		ByteCount: byteCount,
		RootCIDs:  append([]string(nil), rootCIDStrings...),
	}, nil
}

// PublishIPNSName publishes path (e.g. /ipfs/<cid>) under a Kubo key through
// the Kubo RPC name/publish command and returns the IPNS name and the value
// Kubo recorded. The record is published even when Kubo is offline
// (allow-offline): it is stored locally and re-provided when Kubo is online.
// Zero lifetime/ttl leave Kubo's defaults.
func PublishIPNSName(ctx context.Context, ipfsAPIURL, key, path string, lifetime, ttl time.Duration) (string, string, error) {
	key = strings.TrimSpace(key)
	path = strings.TrimSpace(path)
	if strings.TrimSpace(ipfsAPIURL) == "" {
		return "", "", fmt.Errorf("ipfs api url is required")
	}
	if key == "" {
		return "", "", fmt.Errorf("ipns key is required")
	}
	if !strings.HasPrefix(path, "/ipfs/") {
		return "", "", fmt.Errorf("ipns value must be an /ipfs/ path, got %q", path)
	}
	if _, err := cid.Decode(strings.TrimPrefix(path, "/ipfs/")); err != nil {
		return "", "", fmt.Errorf("ipns value: %w", err)
	}
	endpoint, err := url.JoinPath(strings.TrimRight(ipfsAPIURL, "/"), "/api/v0/name/publish")
	if err != nil {
		return "", "", fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return "", "", fmt.Errorf("parse IPFS URL: %w", err)
	}
	query := reqURL.Query()
	query.Set("arg", path)
	query.Set("key", key)
	query.Set("resolve", "false")
	query.Set("allow-offline", "true")
	if lifetime > 0 {
		query.Set("lifetime", lifetime.String())
	}
	if ttl > 0 {
		query.Set("ttl", ttl.String())
	}
	reqURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return "", "", fmt.Errorf("create IPFS name/publish request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("post IPFS name/publish: %w", err)
	}
	defer resp.Body.Close()
	body, truncated, err := readBoundedKuboBody(resp.Body, maxKuboCommandErrorBodyBytes)
	if err != nil {
		return "", "", fmt.Errorf("read IPFS name/publish response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if truncated {
			message += " [truncated]"
		}
		return "", "", fmt.Errorf("IPFS name/publish failed with status %d: %s", resp.StatusCode, message)
	}
	var result struct {
		Name  string `json:"Name"`
		Value string `json:"Value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", fmt.Errorf("decode IPFS name/publish response: %w", err)
	}
	if strings.TrimSpace(result.Name) == "" {
		return "", "", fmt.Errorf("IPFS name/publish response names no IPNS name")
	}
	return strings.TrimSpace(result.Name), strings.TrimSpace(result.Value), nil
}

// RemoveStaleShardGroupCARFiles removes local CAR bundle files in outputDir
// except for keepPaths. Kubo pins are managed separately.
func RemoveStaleShardGroupCARFiles(outputDir string, keepPaths ...string) error {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return nil
	}
	entries, err := os.ReadDir(outputDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read CAR output dir: %w", err)
	}
	keepAbs := map[string]bool{}
	for _, keepPath := range keepPaths {
		if strings.TrimSpace(keepPath) == "" {
			continue
		}
		if abs, err := filepath.Abs(keepPath); err == nil {
			keepAbs[abs] = true
		}
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "shard-group-") || filepath.Ext(name) != ".car" {
			continue
		}
		path := filepath.Join(outputDir, name)
		if len(keepAbs) > 0 {
			if abs, err := filepath.Abs(path); err == nil && keepAbs[abs] {
				continue
			}
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale CAR file %s: %w", path, err)
		}
	}
	return nil
}

// AddAssetGLB adds a GLB as a deterministic 256 KiB-chunked CIDv1 UnixFS
// file through the node pin policy.
func AddAssetGLB(ctx context.Context, pins pinpolicy.Scope, path string) (string, error) {
	return addAssetGLB(ctx, pins, path, false)
}

// CalculateAssetGLBCID calculates the deterministic asset CID without storing
// or pinning the file.
func CalculateAssetGLBCID(ctx context.Context, pins pinpolicy.Scope, path string) (string, error) {
	return addAssetGLB(ctx, pins, path, true)
}

func addAssetGLB(ctx context.Context, pins pinpolicy.Scope, path string, onlyHash bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	added, err := pins.AddFile(ctx, path, pinpolicy.AddOptions{CIDv1: true, Chunker: "size-262144", OnlyHash: onlyHash})
	if err != nil {
		return "", err
	}
	parsed, err := cid.Decode(added.CID)
	if err != nil {
		return "", fmt.Errorf("invalid cid returned by IPFS add: %w", err)
	}
	if parsed.Version() != 1 {
		return "", fmt.Errorf("IPFS add returned CIDv%d, want CIDv1", parsed.Version())
	}
	return parsed.String(), nil
}

// addUnixFSFile adds a local file as CIDv1 UnixFS. A non-empty
// expectedRawCID must be the file's raw-block CID.
func addUnixFSFile(ctx context.Context, pins pinpolicy.Scope, path, expectedRawCID, chunker string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if expectedRawCID != "" {
		localCID, err := cidV1RawSHA256File(path)
		if err != nil {
			return "", fmt.Errorf("compute local raw CID: %w", err)
		}
		if localCID != expectedRawCID {
			return "", fmt.Errorf("local raw CID %s does not match expected CID %s", localCID, expectedRawCID)
		}
	}
	added, err := pins.AddFile(ctx, path, pinpolicy.AddOptions{CIDv1: true, Chunker: chunker})
	if err != nil {
		return "", err
	}
	return added.CID, nil
}

func readBoundedKuboBody(body io.Reader, limit int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) <= limit {
		return data, false, nil
	}
	return data[:limit], true, nil
}

// putRawBlockFile stores a file as one raw block. A non-empty expectedCID
// must be the file's raw-block CID.
func putRawBlockFile(ctx context.Context, pins pinpolicy.Scope, path, expectedCID string) (string, error) {
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
	cidValue, err := pins.PutBlock(ctx, data)
	if err != nil {
		return "", err
	}
	if cidValue != localCID {
		return "", fmt.Errorf("IPFS CID %s does not match local CID %s", cidValue, localCID)
	}
	return cidValue, nil
}

func normalizeCARRoots(rootCIDs []string) ([]cid.Cid, []string, error) {
	seen := map[string]bool{}
	roots := make([]cid.Cid, 0, len(rootCIDs))
	rootCIDStrings := make([]string, 0, len(rootCIDs))
	for _, value := range rootCIDs {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		decoded, err := cid.Decode(value)
		if err != nil {
			return nil, nil, fmt.Errorf("decode CAR root CID %q: %w", value, err)
		}
		seen[value] = true
		roots = append(roots, decoded)
		rootCIDStrings = append(rootCIDStrings, value)
	}
	return roots, rootCIDStrings, nil
}

func appendExportedDAGToCAR(ctx context.Context, ipfsAPIURL, rootCID string, writer carstorage.WritableCar) error {
	resp, err := exportIPFSDAG(ctx, ipfsAPIURL, rootCID)
	if err != nil {
		return err
	}
	defer resp.Close()

	reader, err := car.NewBlockReader(resp)
	if err != nil {
		return fmt.Errorf("read exported DAG CAR for %s: %w", rootCID, err)
	}
	for {
		block, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read exported DAG block for %s: %w", rootCID, err)
		}
		if err := writer.Put(ctx, block.Cid().KeyString(), block.RawData()); err != nil {
			return fmt.Errorf("write CAR block %s: %w", block.Cid(), err)
		}
	}
	return nil
}

func exportIPFSDAG(ctx context.Context, ipfsAPIURL, rootCID string) (io.ReadCloser, error) {
	endpoint, err := url.JoinPath(strings.TrimRight(ipfsAPIURL, "/"), "/api/v0/dag/export")
	if err != nil {
		return nil, fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse IPFS URL: %w", err)
	}
	query := reqURL.Query()
	query.Set("arg", strings.TrimSpace(rootCID))
	query.Set("progress", "false")
	reqURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create IPFS dag/export request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post IPFS dag/export: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("IPFS dag/export failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

func sha256HexFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	hasher := sha256.New()
	byteCount, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), byteCount, nil
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
