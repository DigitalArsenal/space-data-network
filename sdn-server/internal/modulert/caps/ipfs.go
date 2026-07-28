// Package caps provides CapFactory implementations for the modulert capability registry.
// Each factory wires an SDN server subsystem to the space_data_module_host JSON hostcall bridge.
package caps

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	logging "github.com/ipfs/go-log/v2"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// log carries host-side capability decisions. Cap errors travel back to wasm as
// return values, so anything the HOST decides — a refusal, a resource floor —
// has to be logged here or it happens invisibly.
var log = logging.Logger("modulert-caps")

// NewIPFSCapFactory returns a CapFactory for the "ipfs" capability.
// It proxies IPFS operations to the Kubo RPC API at apiURL.
//
// Supported operations (all prefixed "ipfs."):
//
//	cat        — {"cid":"Qm..."}                 → raw bytes
//	add        — {"data":"base64..."}             → {"Hash":"Qm...","Size":N}
//	ls         — {"cid":"Qm..."}                 → Kubo ls response
//	pin_add    — {"cid":"Qm..."}                 → Kubo pin/add response
//	pin_rm     — {"cid":"Qm..."}                 → Kubo pin/rm response
//	pin_ls     — {"type":"all|direct|..."}        → Kubo pin/ls response
//	dag_get    — {"cid":"bafy..."}               → Kubo dag/get response
//	name_resolve — {"name":"/ipns/..."}          → {"Path":"/ipfs/..."}
//	name_publish — {"cid":"/ipfs/...", "lifetime":"24h", "ttl":"1m"} → Kubo response
//	id         — {}                               → Kubo id response
//	swarm_peers — {}                              → Kubo swarm/peers response
//	pubsub_publish — {"topic":"...", "data":"utf8 or base64"} → {}
func NewIPFSCapFactory(apiURL string, httpClient *http.Client) modulert.CapFactory {
	return func(_ *modulert.Module) modulert.CapHandler {
		c := &ipfsCapClient{
			apiURL: strings.TrimRight(apiURL, "/"),
			http:   httpClient,
		}
		if c.http == nil {
			c.http = &http.Client{Timeout: 30 * time.Second}
		}
		return c.handle
	}
}

type ipfsCapClient struct {
	apiURL string
	http   *http.Client
}

func (c *ipfsCapClient) handle(operation string, payload []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var p map[string]interface{}
	if len(payload) > 0 {
		json.Unmarshal(payload, &p) //nolint:errcheck
	}
	str := func(key string) string {
		if p == nil {
			return ""
		}
		if v, ok := p[key]; ok {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}

	switch operation {
	case "ipfs.cat":
		cid := str("cid")
		if cid == "" {
			return errCapJSON("missing cid"), nil
		}
		data, err := c.kuboPost(ctx, "/api/v0/cat?arg="+cid, nil, "")
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		// Return raw bytes wrapped in ok JSON with base64
		return okCapRaw(data), nil

	case "ipfs.add":
		var content []byte
		if raw := str("base64"); raw != "" {
			decoded, err := base64.StdEncoding.DecodeString(raw)
			if err != nil {
				return errCapJSON("invalid base64 payload"), nil
			}
			content = decoded
		} else if raw := str("content"); raw != "" {
			decoded, err := base64.StdEncoding.DecodeString(raw)
			if err != nil {
				return errCapJSON("invalid content payload"), nil
			}
			content = decoded
		} else if raw := str("data"); raw != "" {
			content = []byte(raw)
		}
		if len(content) == 0 {
			return errCapJSON("missing base64 or data"), nil
		}
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		part, _ := w.CreateFormFile("file", "data")
		part.Write(content)
		w.Close()
		resp, err := c.kuboPost(ctx, "/api/v0/add?pin=true", &buf, w.FormDataContentType())
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		return okCapJSON(json.RawMessage(resp)), nil

	case "ipfs.ls":
		cid := str("cid")
		if cid == "" {
			return errCapJSON("missing cid"), nil
		}
		resp, err := c.kuboPost(ctx, "/api/v0/ls?arg="+cid, nil, "")
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		return okCapJSON(json.RawMessage(resp)), nil

	case "ipfs.pin_add":
		cid := str("cid")
		if cid == "" {
			return errCapJSON("missing cid"), nil
		}
		resp, err := c.kuboPost(ctx, "/api/v0/pin/add?arg="+cid, nil, "")
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		return okCapJSON(json.RawMessage(resp)), nil

	case "ipfs.pin_rm":
		cid := str("cid")
		if cid == "" {
			return errCapJSON("missing cid"), nil
		}
		resp, err := c.kuboPost(ctx, "/api/v0/pin/rm?arg="+cid, nil, "")
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		return okCapJSON(json.RawMessage(resp)), nil

	case "ipfs.pin_ls":
		pinType := str("type")
		path := "/api/v0/pin/ls"
		if pinType != "" {
			path += "?type=" + pinType
		}
		resp, err := c.kuboPost(ctx, path, nil, "")
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		return okCapJSON(json.RawMessage(resp)), nil

	case "ipfs.dag_get":
		cid := str("cid")
		if cid == "" {
			return errCapJSON("missing cid"), nil
		}
		resp, err := c.kuboPost(ctx, "/api/v0/dag/get?arg="+cid, nil, "")
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		return okCapJSON(json.RawMessage(resp)), nil

	case "ipfs.name_resolve":
		name := str("name")
		if name == "" {
			return errCapJSON("missing name"), nil
		}
		resp, err := c.kuboPost(ctx, "/api/v0/name/resolve?arg="+name, nil, "")
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		return okCapJSON(json.RawMessage(resp)), nil

	case "ipfs.name_publish":
		cid := str("cid")
		if cid == "" {
			return errCapJSON("missing cid"), nil
		}
		path := "/api/v0/name/publish?arg=" + cid
		if lt := str("lifetime"); lt != "" {
			path += "&lifetime=" + lt
		}
		if ttl := str("ttl"); ttl != "" {
			path += "&ttl=" + ttl
		}
		resp, err := c.kuboPost(ctx, path, nil, "")
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		return okCapJSON(json.RawMessage(resp)), nil

	case "ipfs.id":
		resp, err := c.kuboPost(ctx, "/api/v0/id", nil, "")
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		return okCapJSON(json.RawMessage(resp)), nil

	case "ipfs.swarm_peers":
		resp, err := c.kuboPost(ctx, "/api/v0/swarm/peers", nil, "")
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		return okCapJSON(json.RawMessage(resp)), nil

	case "ipfs.pubsub_publish":
		topic := str("topic")
		if topic == "" {
			return errCapJSON("missing topic"), nil
		}
		data := str("data")
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		part, _ := w.CreateFormFile("file", "data")
		io.WriteString(part, data)
		w.Close()
		resp, err := c.kuboPost(ctx, "/api/v0/pubsub/pub?arg="+topic, &buf, w.FormDataContentType())
		if err != nil {
			return errCapJSON(err.Error()), nil
		}
		return okCapJSON(json.RawMessage(resp)), nil

	default:
		return errCapJSON(fmt.Sprintf("unknown ipfs operation: %s", operation)), nil
	}
}

func (c *ipfsCapClient) kuboPost(ctx context.Context, path string, body io.Reader, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Strip User-Agent: Kubo rejects browser-like UA with 403.
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

// okCapRaw wraps raw bytes in a JSON envelope with base64 encoding.
func okCapRaw(data []byte) []byte {
	// Encode as base64 bytes object
	encoded := make([]byte, 0, len(data))
	encoded = append(encoded, data...)
	r, _ := json.Marshal(map[string]interface{}{
		"ok":     true,
		"result": map[string]interface{}{"__type": "bytes", "base64": encodeBase64Cap(encoded)},
	})
	return r
}

func okCapJSON(result interface{}) []byte {
	r, _ := json.Marshal(map[string]interface{}{"ok": true, "result": result})
	return r
}

func errCapJSON(msg string) []byte {
	r, _ := json.Marshal(map[string]interface{}{
		"ok":    false,
		"error": map[string]string{"message": msg},
	})
	return r
}

// refuseCapJSON is errCapJSON for a refusal the HOST decided: a capability the
// module does not hold, or a resource floor the host is enforcing. It logs.
//
// WHY THIS EXISTS. A cap error is a RETURN VALUE to wasm, not a host event, so
// a host that declines work leaves no trace of having declined it — and if the
// flow swallows the error (a module bug, but one the host cannot prevent) the
// run reports success and stores nothing. That is not theoretical: host-02 sat
// under the 5 GB ingest disk floor for hours, refused every batch, logged
// nothing, and reported `ok` on every run. Three separate investigations read
// "fetch 200, store empty, no error anywhere" and concluded the flow had
// silently trapped.
//
// A module's own mistakes (missing field, bad base64) stay silent here: the
// module is told, and that is the right audience. What must always be visible
// is the host saying no.
func refuseCapJSON(op, msg string) []byte {
	log.Warnf("capability refused: %s: %s", op, msg)
	return errCapJSON(msg)
}

// encodeBase64Cap encodes data using standard base64 (no stdlib import needed).
func encodeBase64Cap(data []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	result := make([]byte, ((len(data)+2)/3)*4)
	di, si := 0, 0
	n := (len(data) / 3) * 3
	for si < n {
		val := uint(data[si])<<16 | uint(data[si+1])<<8 | uint(data[si+2])
		result[di] = alphabet[val>>18&0x3F]
		result[di+1] = alphabet[val>>12&0x3F]
		result[di+2] = alphabet[val>>6&0x3F]
		result[di+3] = alphabet[val&0x3F]
		si += 3
		di += 4
	}
	remain := len(data) - si
	if remain == 2 {
		val := uint(data[si])<<16 | uint(data[si+1])<<8
		result[di] = alphabet[val>>18&0x3F]
		result[di+1] = alphabet[val>>12&0x3F]
		result[di+2] = alphabet[val>>6&0x3F]
		result[di+3] = '='
	} else if remain == 1 {
		val := uint(data[si]) << 16
		result[di] = alphabet[val>>18&0x3F]
		result[di+1] = alphabet[val>>12&0x3F]
		result[di+2] = '='
		result[di+3] = '='
	}
	return string(result)
}
