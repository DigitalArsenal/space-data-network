// Package capabilities provides host capability handlers that bridge SDN
// server services to flow runtime handler functions.
package capabilities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
)

var log = logging.Logger("flowrt-caps")

const ipfsPluginID = "io.spacedatanetwork.ipfs"

// IPFSConfig holds the connection settings for the Kubo RPC API.
type IPFSConfig struct {
	APIURL     string        // e.g. "http://127.0.0.1:5002"
	HTTPClient *http.Client  // optional; defaults to 30s timeout client
}

// NewIPFSHandlers returns flow handlers for IPFS operations.
// These are advertised as available nodes in the flow editor.
//
// Supported methods:
//   - pubsub_publish:  Publish a message to an IPFS pubsub topic
//   - pubsub_subscribe: (trigger-side, not a handler — see IPFSPubSubTrigger)
//   - ls:              List directory contents for a CID
//   - cat:             Read file content by CID
//   - add:             Add bytes to IPFS, returns CID
//   - pin_add:         Pin a CID
//   - pin_rm:          Unpin a CID
//   - pin_ls:          List pinned CIDs
//   - dag_get:         Get a DAG node by CID
//   - name_resolve:    Resolve an IPNS name to a CID
//   - name_publish:    Publish a CID to IPNS
//   - id:              Get the node's IPFS peer identity
//   - swarm_peers:     List connected swarm peers
func NewIPFSHandlers(cfg IPFSConfig) flowrt.HandlerMap {
	c := &ipfsClient{
		apiURL: strings.TrimRight(cfg.APIURL, "/"),
		http:   cfg.HTTPClient,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}

	handlers := flowrt.HandlerMap{
		ipfsPluginID + ":pubsub_publish": c.pubsubPublish,
		ipfsPluginID + ":ls":            c.ls,
		ipfsPluginID + ":cat":           c.cat,
		ipfsPluginID + ":add":           c.add,
		ipfsPluginID + ":pin_add":       c.pinAdd,
		ipfsPluginID + ":pin_rm":        c.pinRm,
		ipfsPluginID + ":pin_ls":        c.pinLs,
		ipfsPluginID + ":dag_get":       c.dagGet,
		ipfsPluginID + ":name_resolve":  c.nameResolve,
		ipfsPluginID + ":name_publish":  c.namePublish,
		ipfsPluginID + ":id":            c.nodeID,
		ipfsPluginID + ":swarm_peers":   c.swarmPeers,
	}
	return handlers
}

type ipfsClient struct {
	apiURL string
	http   *http.Client
}

// kuboPost calls a Kubo RPC endpoint via POST.
func (c *ipfsClient) kuboPost(ctx context.Context, path string, body io.Reader, contentType string) ([]byte, error) {
	url := c.apiURL + path
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Strip User-Agent to avoid Kubo 403
	req.Header.Set("User-Agent", "")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kubo %s: %w", path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kubo %s read body: %w", path, err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("kubo %s: HTTP %d: %s", path, resp.StatusCode, string(data))
	}
	return data, nil
}

// getFrameArg extracts a named argument from the first input frame's JSON.
func getFrameArg(args *flowrt.InvocationArgs, key string) string {
	if len(args.Frames) == 0 || len(args.Frames[0].Bytes) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(args.Frames[0].Bytes, &m); err != nil {
		return ""
	}
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func jsonOutput(data []byte) *flowrt.InvocationResult {
	return &flowrt.InvocationResult{
		StatusCode: 0,
		Outputs: []flowrt.FrameOutput{
			{PortID: "output", Bytes: data},
		},
	}
}

func errorResult(code int32, msg string) *flowrt.InvocationResult {
	errJSON, _ := json.Marshal(map[string]string{"error": msg})
	return &flowrt.InvocationResult{
		StatusCode: code,
		Outputs: []flowrt.FrameOutput{
			{PortID: "error", Bytes: errJSON},
		},
	}
}

// ---------------------------------------------------------------------------
// Handler implementations
// ---------------------------------------------------------------------------

// pubsubPublish publishes a message to an IPFS pubsub topic.
// Input frame JSON: {"topic": "...", "data": "base64 or utf8 string"}
func (c *ipfsClient) pubsubPublish(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	topic := getFrameArg(args, "topic")
	if topic == "" {
		return errorResult(-1, "missing topic"), nil
	}

	// The message payload is the raw bytes of the second frame, or "data" field
	var msgBody io.Reader
	if len(args.Frames) > 1 && len(args.Frames[1].Bytes) > 0 {
		msgBody = bytes.NewReader(args.Frames[1].Bytes)
	} else {
		data := getFrameArg(args, "data")
		msgBody = strings.NewReader(data)
	}

	// Kubo pubsub publish uses multipart form
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", "data")
	io.Copy(part, msgBody)
	w.Close()

	resp, err := c.kuboPost(ctx, "/api/v0/pubsub/pub?arg="+topic, &buf, w.FormDataContentType())
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return jsonOutput(resp), nil
}

// ls lists directory contents for a CID.
// Input: {"cid": "Qm..."}
func (c *ipfsClient) ls(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	cid := getFrameArg(args, "cid")
	if cid == "" {
		return errorResult(-1, "missing cid"), nil
	}
	resp, err := c.kuboPost(ctx, "/api/v0/ls?arg="+cid, nil, "")
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return jsonOutput(resp), nil
}

// cat reads file content by CID.
// Input: {"cid": "Qm..."}
func (c *ipfsClient) cat(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	cid := getFrameArg(args, "cid")
	if cid == "" {
		return errorResult(-1, "missing cid"), nil
	}
	resp, err := c.kuboPost(ctx, "/api/v0/cat?arg="+cid, nil, "")
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return &flowrt.InvocationResult{
		StatusCode: 0,
		Outputs: []flowrt.FrameOutput{
			{PortID: "output", Bytes: resp},
		},
	}, nil
}

// add adds bytes to IPFS and returns the CID.
// Input: first frame's raw bytes are the file content
func (c *ipfsClient) add(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	if len(args.Frames) == 0 || len(args.Frames[0].Bytes) == 0 {
		return errorResult(-1, "missing input data"), nil
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", "data")
	part.Write(args.Frames[0].Bytes)
	w.Close()

	resp, err := c.kuboPost(ctx, "/api/v0/add?pin=true", &buf, w.FormDataContentType())
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return jsonOutput(resp), nil
}

// pinAdd pins a CID.
// Input: {"cid": "Qm..."}
func (c *ipfsClient) pinAdd(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	cid := getFrameArg(args, "cid")
	if cid == "" {
		return errorResult(-1, "missing cid"), nil
	}
	resp, err := c.kuboPost(ctx, "/api/v0/pin/add?arg="+cid, nil, "")
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return jsonOutput(resp), nil
}

// pinRm unpins a CID.
// Input: {"cid": "Qm..."}
func (c *ipfsClient) pinRm(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	cid := getFrameArg(args, "cid")
	if cid == "" {
		return errorResult(-1, "missing cid"), nil
	}
	resp, err := c.kuboPost(ctx, "/api/v0/pin/rm?arg="+cid, nil, "")
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return jsonOutput(resp), nil
}

// pinLs lists pinned CIDs.
// Input: {"type": "all|direct|indirect|recursive"} (optional)
func (c *ipfsClient) pinLs(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	pinType := getFrameArg(args, "type")
	path := "/api/v0/pin/ls"
	if pinType != "" {
		path += "?type=" + pinType
	}
	resp, err := c.kuboPost(ctx, path, nil, "")
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return jsonOutput(resp), nil
}

// dagGet retrieves a DAG node by CID.
// Input: {"cid": "bafy..."}
func (c *ipfsClient) dagGet(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	cid := getFrameArg(args, "cid")
	if cid == "" {
		return errorResult(-1, "missing cid"), nil
	}
	resp, err := c.kuboPost(ctx, "/api/v0/dag/get?arg="+cid, nil, "")
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return jsonOutput(resp), nil
}

// nameResolve resolves an IPNS name to a CID.
// Input: {"name": "/ipns/..."}
func (c *ipfsClient) nameResolve(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	name := getFrameArg(args, "name")
	if name == "" {
		return errorResult(-1, "missing name"), nil
	}
	resp, err := c.kuboPost(ctx, "/api/v0/name/resolve?arg="+name, nil, "")
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return jsonOutput(resp), nil
}

// namePublish publishes a CID under IPNS.
// Input: {"cid": "Qm...", "key": "self" (optional)}
func (c *ipfsClient) namePublish(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	cid := getFrameArg(args, "cid")
	if cid == "" {
		return errorResult(-1, "missing cid"), nil
	}
	key := getFrameArg(args, "key")
	path := "/api/v0/name/publish?arg=" + cid
	if key != "" {
		path += "&key=" + key
	}
	resp, err := c.kuboPost(ctx, path, nil, "")
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return jsonOutput(resp), nil
}

// nodeID returns the IPFS node's peer identity.
func (c *ipfsClient) nodeID(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	resp, err := c.kuboPost(ctx, "/api/v0/id", nil, "")
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return jsonOutput(resp), nil
}

// swarmPeers lists connected swarm peers.
func (c *ipfsClient) swarmPeers(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	resp, err := c.kuboPost(ctx, "/api/v0/swarm/peers", nil, "")
	if err != nil {
		return errorResult(-1, err.Error()), nil
	}
	return jsonOutput(resp), nil
}

// ---------------------------------------------------------------------------
// IPFS PubSub Trigger — subscribes to a topic and enqueues trigger frames
// ---------------------------------------------------------------------------

// IPFSPubSubTrigger subscribes to an IPFS pubsub topic and enqueues trigger
// frames into the flow runtime when messages arrive.
type IPFSPubSubTrigger struct {
	client   *ipfsClient
	runtime  *flowrt.FlowRuntime
	handlers flowrt.HandlerMap
	topic    string
	trigIdx  uint32
	cancel   context.CancelFunc
}

// StartIPFSPubSubTrigger starts a long-poll subscription to an IPFS pubsub topic.
func StartIPFSPubSubTrigger(cfg IPFSConfig, rt *flowrt.FlowRuntime, handlers flowrt.HandlerMap, topic string, triggerIndex uint32) *IPFSPubSubTrigger {
	c := &ipfsClient{
		apiURL: strings.TrimRight(cfg.APIURL, "/"),
		http:   &http.Client{Timeout: 0}, // no timeout for long-poll
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &IPFSPubSubTrigger{
		client:   c,
		runtime:  rt,
		handlers: handlers,
		topic:    topic,
		trigIdx:  triggerIndex,
		cancel:   cancel,
	}
	go t.run(ctx)
	return t
}

func (t *IPFSPubSubTrigger) run(ctx context.Context) {
	log.Infof("IPFS pubsub trigger started for topic %q (trigger %d)", t.topic, t.trigIdx)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Long-poll: Kubo /api/v0/pubsub/sub returns newline-delimited JSON
		url := t.client.apiURL + "/api/v0/pubsub/sub?arg=" + t.topic
		req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
		if err != nil {
			log.Warnf("IPFS pubsub sub request error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		req.Header.Set("User-Agent", "")

		resp, err := t.client.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warnf("IPFS pubsub sub error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		dec := json.NewDecoder(resp.Body)
		for {
			var msg json.RawMessage
			if err := dec.Decode(&msg); err != nil {
				resp.Body.Close()
				break
			}

			// Enqueue as trigger frame and drain
			t.runtime.EnqueueTrigger(t.trigIdx)
			t.runtime.DrainOnce(ctx, t.handlers)
		}
	}
}

// Stop stops the pubsub subscription.
func (t *IPFSPubSubTrigger) Stop() {
	t.cancel()
}

