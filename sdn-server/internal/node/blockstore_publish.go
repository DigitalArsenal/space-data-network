package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WHY THIS FILE EXISTS (2026-07-29, ops-browser-content-source-gap).
//
// A record is only anonymously fetchable if some blockstore with a
// browser-reachable endpoint actually HOLDS its bytes. Two announcement
// surfaces were making claims nothing could satisfy:
//
//  1. Every $PNM this node emits carries its own $EPM CID, and the vCard /
//     epmcid verification chain resolves that CID. It resolved NOWHERE:
//     `block stat --offline` for the announced EPM CID missed in BOTH cluster
//     blockstores, because the EPM was only ever computed, never stored.
//  2. A materialized dataset publication left the shard + index DAG in the
//     local blockstore UNPINNED (TipQueue pins the manifest CID alone). The
//     catalog a browser fetches was therefore GC-eligible and outside any
//     pinned-only reprovide set — durable only by luck.
//
// Both fixes are connector actions against the already-configured Kubo RPC API
// (admin.ipfs_api_url): put bytes, pin CIDs. No application logic lives here,
// and an unset admin.ipfs_api_url disables both paths.
const (
	blockstorePublishTimeout = 30 * time.Second
	datasetDAGPinTimeout     = 5 * time.Minute
)

// putRawBlockToLocalBlockstore stores data as a pinned CIDv1 raw block
// (sha2-256) through the local Kubo RPC API and returns the CID Kubo reports.
// This is the "already have the bytes in memory" counterpart to storage's
// pin helpers, which all upload a local *file*.
func putRawBlockToLocalBlockstore(ctx context.Context, apiURL string, data []byte) (string, error) {
	apiURL = strings.TrimSpace(apiURL)
	if apiURL == "" {
		return "", fmt.Errorf("ipfs api url is required")
	}
	if len(data) == 0 {
		return "", fmt.Errorf("block bytes are required")
	}
	endpoint, err := url.JoinPath(strings.TrimRight(apiURL, "/"), "/api/v0/block/put")
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

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("data", "block")
	if err != nil {
		return "", fmt.Errorf("create IPFS multipart field: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("write IPFS multipart field: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close IPFS multipart body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), &body)
	if err != nil {
		return "", fmt.Errorf("create IPFS block/put request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("post IPFS block/put: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("read IPFS block/put response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("IPFS block/put failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	return parseBlockPutCID(responseBody)
}

// PutPinnedRawBlock is the exported connector: bytes in, the pinned CIDv1 raw
// block's identifier out. It is the same call publishLocalNodeEPMToBlockstore
// makes for the node's own $EPM, exposed so the account-EPM lane
// (internal/api/account_epm_store.go) can pin an account's identity the same
// way — through main.go, which owns the binding, not through a new dependency.
func PutPinnedRawBlock(ctx context.Context, apiURL string, data []byte) (string, error) {
	return putRawBlockToLocalBlockstore(ctx, apiURL, data)
}

func parseBlockPutCID(body []byte) (string, error) {
	var result struct {
		Key string `json:"Key"`
		CID string `json:"Cid"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode IPFS block/put response: %w", err)
	}
	if cidValue := strings.TrimSpace(result.Key); cidValue != "" {
		return cidValue, nil
	}
	if cidValue := strings.TrimSpace(result.CID); cidValue != "" {
		return cidValue, nil
	}
	return "", fmt.Errorf("IPFS block/put response missing CID")
}

// blockstoreConnector resolves the one thing both paths need — a configured
// Kubo RPC API and a live parent context — and reports NOT-CONFIGURED as a
// clean false rather than a nil dereference. Partially-built Nodes are real:
// indexLocalNodeEPM runs from constructors and from tests that never call
// Start(), so `n.config`/`n.ctx` can legitimately be nil here.
func (n *Node) blockstoreConnector() (string, context.Context, bool) {
	if n == nil || n.config == nil {
		return "", nil, false
	}
	apiURL := strings.TrimSpace(n.config.Admin.IPFSAPIURL)
	if apiURL == "" {
		return "", nil, false
	}
	ctx := n.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return apiURL, ctx, true
}

// lastPublishedEPMCID guards the log line, not the work: re-putting an
// identical block is idempotent in Kubo, so a repeat costs one cheap RPC.
var lastPublishedEPMCID struct {
	sync.Mutex
	cid string
}

// publishLocalNodeEPMToBlockstore stores this node's own $EPM record in the
// local blockstore so the epmcid every $PNM and vCard advertises actually
// resolves for an anonymous off-box fetcher. Best effort by construction: an
// identity record that cannot be stored must never block indexing, publishing,
// or boot.
func (n *Node) publishLocalNodeEPMToBlockstore() {
	apiURL, ctx, ok := n.blockstoreConnector()
	if !ok || n.epmService == nil {
		return
	}
	epmBytes := n.epmService.GetNodeEPM()
	if len(epmBytes) == 0 {
		return
	}
	putCtx, cancel := context.WithTimeout(ctx, blockstorePublishTimeout)
	defer cancel()
	cidValue, err := putRawBlockToLocalBlockstore(putCtx, apiURL, epmBytes)
	if err != nil {
		log.Warnf("Could not publish local node EPM to the local blockstore: %v", err)
		return
	}
	lastPublishedEPMCID.Lock()
	changed := lastPublishedEPMCID.cid != cidValue
	lastPublishedEPMCID.cid = cidValue
	lastPublishedEPMCID.Unlock()
	if changed {
		log.Infof("Published local node EPM to the local blockstore: %s (%d bytes, pinned)", cidValue, len(epmBytes))
	}
}

// pinMaterializedDatasetDAG pins the manifest, shard and index CIDs a
// just-materialized dataset publication referenced. The bytes are already in
// the local blockstore (materialization fetched them through this same Kubo
// API); pinning is what makes them survive GC and stay in a pinned-strategy
// reprovide set, i.e. what makes this node a durable PROVIDER of the catalog
// rather than an incidental cache of it.
//
// Bounded and non-blocking: the caller hands off to n.wg and returns, so a slow
// or unreachable pin RPC can never stall the ingest feed.
func (n *Node) pinMaterializedDatasetDAG(cids ...string) {
	apiURL, ctx, ok := n.blockstoreConnector()
	if !ok {
		return
	}
	wanted := make([]string, 0, len(cids))
	seen := make(map[string]bool, len(cids))
	for _, cidValue := range cids {
		cidValue = strings.TrimSpace(cidValue)
		if cidValue == "" || seen[cidValue] {
			continue
		}
		seen[cidValue] = true
		wanted = append(wanted, cidValue)
	}
	if len(wanted) == 0 {
		return
	}
	pinner := newIPFSTipPinner(apiURL)
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		pinCtx, cancel := context.WithTimeout(ctx, datasetDAGPinTimeout)
		defer cancel()
		for _, cidValue := range wanted {
			if err := pinner.Pin(pinCtx, cidValue, 0); err != nil {
				log.Warnf("Could not pin materialized dataset CID %s: %v", cidValue, err)
				continue
			}
			log.Debugf("Pinned materialized dataset CID %s", cidValue)
		}
	}()
}
