package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
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

type pinUnixFSFileOptions struct {
	Chunker string
}

const (
	maxKuboCommandErrorBodyBytes        = 4 * 1024
	maxAssetKuboAddResponseBytes        = 64 * 1024
	maxKuboIPNSNamePublishResponseBytes = 16 * 1024
	assetKuboRequestTimeout             = 30 * time.Second
)

// IPNS re-publication policy for the daemon's own name (the record accepted
// by the deployment check; acceptance: the re-publish interval must stay
// strictly inside the record lifetime, or the name decays back to
// unresolvable):
//   - ipnsRecordLifetime is the validity window signed into every IPNS record
//     — sent to kubod as an explicit `lifetime=` on every name/publish.
//   - ipnsKuboRepublishPeriod is kubod's background re-publish cadence (Kubo
//     config default on both production hosts). TestIPNSRepublishPolicy-
//     StaysInsideRecordLifetime fails the build if it ever reaches the
//     lifetime.
//   - Plus an immediate re-publish on EVERY catalog manifest publish (see
//     PublishDatasetPublicationManifestToIPFS), so the name tracks each
//     freshly exported catalog rather than only staying alive.
const (
	ipnsRecordLifetime      = 168 * time.Hour // 7 days
	ipnsKuboRepublishPeriod = 23 * time.Hour
)

// ipnsLifetimeParam renders ipnsRecordLifetime the exact way the name/publish
// command documents it ("168h"), keeping the wire value and the signed record
// window in one source of truth.
func ipnsLifetimeParam() string {
	return fmt.Sprintf("%dh", int64(ipnsRecordLifetime/time.Hour))
}

type assetKuboDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type assetKuboAddOptions struct {
	Pin      bool
	OnlyHash bool
}

func newAssetKuboHTTPClient() *http.Client {
	return &http.Client{Timeout: assetKuboRequestTimeout}
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

// PublishDatasetPublicationManifestToIPFS pins a signed dataset manifest
// through a Kubo RPC API, then re-publishes the daemon's IPNS name at the
// freshly pinned manifest CID. This function is the single choke point every
// catalog manifest publish passes through, so the name re-publish happens on
// every catalog publish — not just on a timer. A re-publish failure is logged
// at error level but does NOT fail the manifest publish: the content keeps
// serving, and the deployment check (celestrak-browser-provider-probe Layer 4
// — NAME) is the tripwire for a stale name.
func PublishDatasetPublicationManifestToIPFS(ctx context.Context, ipfsAPIURL string, manifest *DatasetPublicationManifest) (string, error) {
	if manifest == nil {
		return "", fmt.Errorf("dataset publication manifest is required")
	}
	manifestCID, err := pinRawBlock(ctx, ipfsAPIURL, manifest.Path, manifest.CID)
	if err != nil {
		return "", fmt.Errorf("pin manifest: %w", err)
	}
	if err := PublishIPNSName(ctx, ipfsAPIURL, manifestCID); err != nil {
		log.Errorf("IPNS name re-publish to %s failed after pinning manifest %s (content keeps serving; the stale name is the deployment check's finding): %v", ipfsAPIURL, manifestCID, err)
	}
	return manifestCID, nil
}

// PublishIPNSName publishes the daemon's own IPNS name to the given CID
// through Kubo's name/publish command. No `key` is sent: kubod publishes
// under its own identity key, which IS the IPNS name the deployment check
// verifies (host-01's ipfs.service name). The record is signed with an
// explicit lifetime (ipnsRecordLifetime) and is allowed to publish offline —
// the block is already pinned locally, so the signed record stays valid and
// resolvable without a re-announce.
func PublishIPNSName(ctx context.Context, ipfsAPIURL, cidValue string) error {
	return publishIPNSNameWithClient(ctx, newAssetKuboHTTPClient(), ipfsAPIURL, cidValue)
}

func publishIPNSNameWithClient(ctx context.Context, client assetKuboDoer, apiURL, cidValue string) error {
	if client == nil {
		return fmt.Errorf("asset Kubo client is required")
	}
	if strings.TrimSpace(apiURL) == "" {
		return fmt.Errorf("ipfs api url is required")
	}
	cidValue = strings.TrimSpace(cidValue)
	if cidValue == "" {
		return fmt.Errorf("cid is required")
	}
	parsedCID, err := cid.Decode(cidValue)
	if err != nil {
		return fmt.Errorf("invalid cid: %w", err)
	}

	endpoint, err := url.JoinPath(strings.TrimRight(apiURL, "/"), "/api/v0/name/publish")
	if err != nil {
		return fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse IPFS URL: %w", err)
	}
	query := reqURL.Query()
	query.Set("arg", "/ipfs/"+parsedCID.String())
	query.Set("lifetime", ipnsLifetimeParam())
	query.Set("allow-offline", "true")
	// No "key": kubod publishes under its own identity key — the daemon's
	// own IPNS name. Delete any key inherited from a configured API URL so
	// the command can never be steered onto another identity.
	query.Del("key")
	reqURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create IPFS request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post IPFS name publish: %w", err)
	}
	defer resp.Body.Close()
	body, truncated, err := readBoundedKuboBody(resp.Body, maxKuboIPNSNamePublishResponseBytes)
	if err != nil {
		return fmt.Errorf("read IPFS name publish response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if truncated {
			message += " [truncated]"
		}
		return fmt.Errorf("IPFS name publish failed with status %d: %s", resp.StatusCode, message)
	}
	if truncated {
		return fmt.Errorf("IPFS name publish response exceeds %d bytes", maxKuboIPNSNamePublishResponseBytes)
	}
	var result struct {
		Value string `json:"Value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode IPFS name publish response: %w", err)
	}
	expected := "/ipfs/" + parsedCID.String()
	if result.Value != expected {
		return fmt.Errorf("IPFS name publish returned value %q, want %q", result.Value, expected)
	}
	return nil
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
// CARv1 with those roots, and pins the resulting CAR file back to IPFS. The
// root DAGs must already exist in the local Kubo blockstore.
func PublishShardGroupCARToIPFS(ctx context.Context, ipfsAPIURL, outputDir string, rootCIDs []string) (*PublishedShardGroupCAR, error) {
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

	carCID, err := pinUnixFSFileWithOptions(ctx, ipfsAPIURL, finalPath, "", pinUnixFSFileOptions{
		Chunker: "size-1048576",
	})
	if err != nil {
		return nil, fmt.Errorf("pin CAR bundle: %w", err)
	}
	return &PublishedShardGroupCAR{
		CID:       carCID,
		Path:      finalPath,
		SHA256:    sha256Value,
		ByteCount: byteCount,
		RootCIDs:  append([]string(nil), rootCIDStrings...),
	}, nil
}

// UnpinIPFSCID removes a direct recursive pin from Kubo. Missing pins are
// treated as already-retired so cleanup can be retried safely.
func UnpinIPFSCID(ctx context.Context, ipfsAPIURL, cidValue string) error {
	cidValue = strings.TrimSpace(cidValue)
	if strings.TrimSpace(ipfsAPIURL) == "" {
		return fmt.Errorf("ipfs api url is required")
	}
	if cidValue == "" {
		return fmt.Errorf("cid is required")
	}
	endpoint, err := url.JoinPath(strings.TrimRight(ipfsAPIURL, "/"), "/api/v0/pin/rm")
	if err != nil {
		return fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse IPFS URL: %w", err)
	}
	query := reqURL.Query()
	query.Set("arg", cidValue)
	query.Set("recursive", "true")
	reqURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create IPFS pin/rm request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("post IPFS pin/rm: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	message := strings.TrimSpace(string(body))
	if strings.Contains(strings.ToLower(message), "not pinned") {
		return nil
	}
	return fmt.Errorf("IPFS pin/rm failed with status %d: %s", resp.StatusCode, message)
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

func pinUnixFSFile(ctx context.Context, ipfsAPIURL, path, expectedRawCID string) (string, error) {
	return pinUnixFSFileWithOptions(ctx, ipfsAPIURL, path, expectedRawCID, pinUnixFSFileOptions{})
}

// PinAssetGLB pins a GLB as a deterministic 256 KiB-chunked UnixFS file.
func PinAssetGLB(ctx context.Context, apiURL, path string) (string, error) {
	return addAssetGLBWithClient(ctx, newAssetKuboHTTPClient(), apiURL, path, assetKuboAddOptions{Pin: true})
}

// CalculateAssetGLBCID calculates the deterministic asset CID without storing
// or pinning the file in Kubo.
func CalculateAssetGLBCID(ctx context.Context, apiURL, path string) (string, error) {
	return addAssetGLBWithClient(ctx, newAssetKuboHTTPClient(), apiURL, path, assetKuboAddOptions{OnlyHash: true})
}

func addAssetGLBWithClient(ctx context.Context, client assetKuboDoer, apiURL, path string, options assetKuboAddOptions) (string, error) {
	if client == nil {
		return "", fmt.Errorf("asset Kubo client is required")
	}
	if strings.TrimSpace(apiURL) == "" {
		return "", fmt.Errorf("ipfs api url is required")
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if options.Pin == options.OnlyHash {
		return "", fmt.Errorf("exactly one asset Kubo add mode is required")
	}

	endpoint, err := url.JoinPath(strings.TrimRight(apiURL, "/"), "/api/v0/add")
	if err != nil {
		return "", fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse IPFS URL: %w", err)
	}
	query := make(url.Values)
	query.Set("cid-version", "1")
	query.Set("raw-leaves", "true")
	query.Set("hash", "sha2-256")
	query.Set("wrap-with-directory", "false")
	query.Set("progress", "false")
	query.Set("chunker", "size-262144")
	if options.Pin {
		query.Set("pin", "true")
		query.Del("only-hash")
	} else {
		query.Set("pin", "false")
		query.Set("only-hash", "true")
	}
	reqURL.RawQuery = query.Encode()

	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	writerDone := make(chan error, 1)
	go func() {
		writeErr := writeAssetGLBMultipart(path, multipartWriter)
		if writeErr == nil {
			writeErr = multipartWriter.Close()
		}
		if writeErr != nil {
			_ = writer.CloseWithError(writeErr)
		} else {
			_ = writer.Close()
		}
		writerDone <- writeErr
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-writerDone
		return "", fmt.Errorf("create IPFS request: %w", err)
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-writerDone
		return "", fmt.Errorf("post IPFS add: %w", err)
	}
	_ = reader.CloseWithError(fmt.Errorf("IPFS add response arrived before multipart shutdown"))
	writerErr := <-writerDone
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 && writerErr != nil {
		return "", fmt.Errorf("write IPFS multipart request: %w", writerErr)
	}
	body, truncated, err := readBoundedKuboBody(resp.Body, maxAssetKuboAddResponseBytes)
	if err != nil {
		return "", fmt.Errorf("read IPFS add response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if truncated {
			message += " [truncated]"
		}
		return "", fmt.Errorf("IPFS add failed with status %d: %s", resp.StatusCode, message)
	}
	if truncated {
		return "", fmt.Errorf("IPFS add response exceeds %d bytes", maxAssetKuboAddResponseBytes)
	}
	return parseAssetKuboAddCID(body)
}

func writeAssetGLBMultipart(path string, multipartWriter *multipart.Writer) error {
	part, err := multipartWriter.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return fmt.Errorf("create IPFS multipart field: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	if _, err := io.Copy(part, file); err != nil {
		return fmt.Errorf("write IPFS multipart field: %w", err)
	}
	return nil
}

// UnpinAssetCID removes an asset pin through Kubo's pin/rm command.
func UnpinAssetCID(ctx context.Context, apiURL, cidValue string) error {
	cidValue = strings.TrimSpace(cidValue)
	if cidValue == "" {
		return fmt.Errorf("cid is required")
	}
	parsedCID, err := cid.Decode(cidValue)
	if err != nil {
		return fmt.Errorf("invalid cid: %w", err)
	}
	return postKuboCommand(ctx, apiURL, "/api/v0/pin/rm", url.Values{"arg": {parsedCID.String()}})
}

func postKuboCommand(ctx context.Context, apiURL, commandPath string, values url.Values) error {
	return postKuboCommandWithClient(ctx, newAssetKuboHTTPClient(), apiURL, commandPath, values)
}

func postKuboCommandWithClient(ctx context.Context, client assetKuboDoer, apiURL, commandPath string, values url.Values) error {
	if client == nil {
		return fmt.Errorf("asset Kubo client is required")
	}
	if strings.TrimSpace(apiURL) == "" {
		return fmt.Errorf("ipfs api url is required")
	}
	endpoint, err := url.JoinPath(strings.TrimRight(apiURL, "/"), commandPath)
	if err != nil {
		return fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse IPFS URL: %w", err)
	}
	query := reqURL.Query()
	for key, valueList := range values {
		query.Del(key)
		for _, value := range valueList {
			query.Add(key, value)
		}
	}
	reqURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create IPFS command request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post IPFS command: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxKuboCommandErrorBodyBytes+1)); err != nil {
			return fmt.Errorf("read IPFS command response: %w", err)
		}
		return nil
	}
	body, truncated, err := readBoundedKuboBody(resp.Body, maxKuboCommandErrorBodyBytes)
	if err != nil {
		return fmt.Errorf("read IPFS command response: %w", err)
	}
	message := strings.TrimSpace(string(body))
	if !truncated && strings.Contains(strings.ToLower(message), "not pinned") {
		return nil
	}
	if truncated {
		message += " [truncated]"
	}
	return fmt.Errorf("IPFS command %s failed with status %d: %s", commandPath, resp.StatusCode, message)
}

func pinUnixFSFileWithOptions(ctx context.Context, ipfsAPIURL, path, expectedRawCID string, options pinUnixFSFileOptions) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	var localCID string
	if expectedRawCID != "" {
		var err error
		localCID, err = cidV1RawSHA256File(path)
		if err != nil {
			return "", fmt.Errorf("compute local raw CID: %w", err)
		}
		if localCID != expectedRawCID {
			return "", fmt.Errorf("local raw CID %s does not match expected CID %s", localCID, expectedRawCID)
		}
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
	if chunker := strings.TrimSpace(options.Chunker); chunker != "" {
		query.Set("chunker", chunker)
	}
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

func parseAssetKuboAddCID(body []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var result struct {
		Hash string `json:"Hash"`
		CID  string `json:"Cid"`
	}
	if err := decoder.Decode(&result); err != nil {
		return "", fmt.Errorf("decode IPFS add response: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("decode IPFS add response: multiple JSON objects")
		}
		return "", fmt.Errorf("decode IPFS add trailing response: %w", err)
	}

	hashValue := strings.TrimSpace(result.Hash)
	cidValue := strings.TrimSpace(result.CID)
	if hashValue == "" && cidValue == "" {
		return "", fmt.Errorf("IPFS add response missing CID")
	}
	var canonical string
	for _, value := range []string{hashValue, cidValue} {
		if value == "" {
			continue
		}
		parsed, err := cid.Decode(value)
		if err != nil {
			return "", fmt.Errorf("invalid cid returned by IPFS add: %w", err)
		}
		if parsed.Version() != 1 {
			return "", fmt.Errorf("IPFS add returned CIDv%d, want CIDv1", parsed.Version())
		}
		if canonical == "" {
			canonical = parsed.String()
			continue
		}
		if canonical != parsed.String() {
			return "", fmt.Errorf("IPFS add response contains conflicting CIDs")
		}
	}
	return canonical, nil
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
	if expectedCID != "" && cidValue != localCID {
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
