package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
)

// The node's own $EPM in the local blockstore.
//
// Every $PNM this node emits carries its own $EPM CID, and the vCard / epmcid
// verification chain resolves that CID. An identity record that is only ever
// computed resolves nowhere; this file stores it, through the node pin policy
// (NODE_EPM, always pinned), so the advertised CID is fetchable off-box.
const (
	blockstorePublishTimeout = 30 * time.Second
	datasetDAGPinTimeout     = 5 * time.Minute
)

// ProvideOnce announces a CID the local blockstore holds to the routing system
// through Kubo's own `provide once` (the sweep provider queues it for its
// burst workers). It succeeds once Kubo reports the CID queued.
func ProvideOnce(ctx context.Context, apiURL, cidValue string) error {
	apiURL = strings.TrimSpace(apiURL)
	cidValue = strings.TrimSpace(cidValue)
	if apiURL == "" || cidValue == "" {
		return fmt.Errorf("ipfs api url and cid are required")
	}
	endpoint, err := url.JoinPath(strings.TrimRight(apiURL, "/"), "/api/v0/provide/once")
	if err != nil {
		return fmt.Errorf("build IPFS URL: %w", err)
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse IPFS URL: %w", err)
	}
	reqURL.RawQuery = url.Values{"arg": {cidValue}, "enc": {"json"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create IPFS provide request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("post IPFS provide: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("read IPFS provide response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("IPFS provide failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if streamErr := strings.TrimSpace(resp.Trailer.Get("X-Stream-Error")); streamErr != "" {
		return fmt.Errorf("IPFS provide: %s", streamErr)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	for {
		var event struct {
			Queued string `json:"Queued"`
		}
		if err := decoder.Decode(&event); err != nil {
			break
		}
		if strings.TrimSpace(event.Queued) == cidValue {
			return nil
		}
	}
	return fmt.Errorf("IPFS provide did not queue %s: %s", cidValue, strings.TrimSpace(string(body)))
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
	if n == nil || n.epmService == nil {
		return
	}
	pins := n.pins.Scope(pinpolicy.NodeEPM, "")
	if !pins.Configured() {
		return
	}
	epmBytes := n.epmService.GetNodeEPM()
	if len(epmBytes) == 0 {
		return
	}
	ctx := n.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	putCtx, cancel := context.WithTimeout(ctx, blockstorePublishTimeout)
	defer cancel()
	cidValue, err := pins.PutBlock(putCtx, epmBytes)
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
