// dpm-manifest-e2e boots a node built from this checkout on a fresh data
// directory with its own Kubo, seeds one lane with more source batches than
// fit a $DPM under Kubo's 2 MiB block limit (one SOURCES and one BATCH_IDS
// entry per batch), publishes the lane, and proves the manifest publishes and
// reads back by CID:
//
//	a. the publish route answers a manifest CID (it answered Kubo's "produced
//	   block is over 2MiB" error before);
//	b. the manifest is over 2 MiB, its CID is a UnixFS root (dag-pb), the root
//	   block is under the limit, and Kubo pins it;
//	c. cat of the CID returns the manifest, which hashes to the published CID,
//	   and the node's own channel for the lane records the $PNM and the $DPM
//	   (signature and CID) verified;
//	d. the manifest names every seeded batch and the shard it signs for reads
//	   back by CID with the length and sha256 the manifest states.
//
//	dpm-manifest-e2e --binary <node>
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/ipfs/go-cid"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	schema   = "OMM.fbs"
	provider = "e2e-provider"
	lane     = "lane-big"
	peerID   = "dpm-manifest-e2e"
	// batches is chosen so the $DPM is over 2 MiB: each batch carries a long
	// source URL, as the terrain lane's per-file sources do.
	batches = 4500
	urlPad  = 600
	// blockLimit is Kubo's block size limit.
	blockLimit = 2 << 20
)

var day0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Printf("DPM-MANIFEST-E2E FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("DPM-MANIFEST-E2E PASS")
}

var failures []string

func check(name string, err error) {
	if err != nil {
		failures = append(failures, name)
		fmt.Printf("FAIL %s: %v\n", name, err)
		return
	}
	fmt.Printf("PASS %s\n", name)
}

func batchID(i int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("dpm-manifest-e2e-batch-%d", i)))
	return hex.EncodeToString(sum[:])
}

func sourceURL(i int) string {
	return fmt.Sprintf("https://example.invalid/e2e/%06d/%s", i, strings.Repeat("p", urlPad))
}

func run(ctx context.Context) error {
	binary := flag.String("binary", "", "node binary to boot")
	flag.Parse()
	if *binary == "" {
		return errors.New("--binary is required")
	}
	root, err := os.MkdirTemp("", "dpm-manifest-e2e-store-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := seed(root); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	n, err := prepare(*binary, root)
	if err != nil {
		return err
	}
	defer n.cleanup()
	if err := n.start(ctx); err != nil {
		return err
	}
	k := kubo{base: n.kubo}

	var manifestCID string
	check("a the lane publishes", func() error {
		var err error
		manifestCID, err = publish(ctx, n.base)
		return err
	}())
	if manifestCID == "" {
		return fmt.Errorf("no manifest CID: %s", strings.Join(failures, "; "))
	}
	var manifest []byte
	check("b the manifest is over 2 MiB and stored as a pinned UnixFS file", func() error {
		var err error
		manifest, err = storedAsUnixFS(ctx, k, manifestCID)
		return err
	}())
	if manifest != nil {
		check("c the manifest reads back by CID; the node verified its signature and CID", verifyManifest(n.base, manifest, manifestCID))
		check("d the manifest names every batch; its shard reads back by CID", names(ctx, k, manifest))
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d check(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func seed(root string) error {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return err
	}
	store, err := storage.NewFlatSQLStore(root, validator)
	if err != nil {
		return err
	}
	for i := 0; i < batches; i++ {
		epoch := day0.Add(time.Duration(i) * time.Minute)
		record := sds.NewOMMBuilder().
			WithNoradCatID(uint32(40000 + i)).
			WithObjectName(fmt.Sprintf("DPM-E2E-%d", i)).
			WithEpoch(epoch.Format(time.RFC3339)).
			WithEpochTimestamp(float64(epoch.Unix())).
			WithMeanMotion(15.5).
			Build()
		tags := storage.SourceTags{ProviderID: provider, SourceName: lane, SourceURL: sourceURL(i), BatchID: batchID(i), ProducerPeerID: peerID}
		if _, err := store.StoreWithSourceTags(schema, record, peerID, nil, tags); err != nil {
			_ = store.Close()
			return err
		}
	}
	return store.Close()
}

func publish(ctx context.Context, base string) (string, error) {
	body, _ := json.Marshal(map[string]any{"schema": schema, "providerId": provider, "sourceName": lane, "fullCatalog": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/admin/dataset-updates/publish", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("publish: HTTP %d %s", resp.StatusCode, clip(data))
	}
	var result struct {
		RecordCount int    `json:"recordCount"`
		ManifestCID string `json:"manifestCid"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.ManifestCID == "" {
		return "", fmt.Errorf("publish answered no manifest CID: %s", clip(data))
	}
	if result.RecordCount != batches {
		return "", fmt.Errorf("publish covered %d records, want %d", result.RecordCount, batches)
	}
	fmt.Printf("     published %d records, manifest %s\n", result.RecordCount, result.ManifestCID)
	return result.ManifestCID, nil
}

func storedAsUnixFS(ctx context.Context, k kubo, manifestCID string) ([]byte, error) {
	parsed, err := cid.Decode(manifestCID)
	if err != nil {
		return nil, err
	}
	if parsed.Prefix().Codec != cid.DagProtobuf {
		return nil, fmt.Errorf("manifest CID %s is codec %#x, want dag-pb (UnixFS root)", manifestCID, parsed.Prefix().Codec)
	}
	data, status, err := k.post(ctx, "/api/v0/cat", url.Values{"arg": {manifestCID}})
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("cat %s: HTTP %d %v %s", manifestCID, status, err, clip(data))
	}
	if len(data) <= blockLimit {
		return nil, fmt.Errorf("manifest is %d bytes, not over the %d byte block limit: seed more batches", len(data), blockLimit)
	}
	stat, status, err := k.post(ctx, "/api/v0/block/stat", url.Values{"arg": {manifestCID}, "offline": {"true"}})
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("block/stat %s: HTTP %d %v %s", manifestCID, status, err, clip(stat))
	}
	var blockStat struct{ Size int }
	if err := json.Unmarshal(stat, &blockStat); err != nil || blockStat.Size <= 0 || blockStat.Size >= blockLimit {
		return nil, fmt.Errorf("root block of %s is %d bytes (%v), want under %d", manifestCID, blockStat.Size, err, blockLimit)
	}
	pins, err := k.pins(ctx)
	if err != nil {
		return nil, err
	}
	if pins[manifestCID] != "recursive" {
		return nil, fmt.Errorf("manifest %s is not pinned recursively (pin type %q)", manifestCID, pins[manifestCID])
	}
	fmt.Printf("     manifest %d bytes, root block %d bytes, pinned recursive\n", len(data), blockStat.Size)
	return data, nil
}

func verifyManifest(base string, manifest []byte, manifestCID string) error {
	got, err := storage.DatasetManifestCID(manifest)
	if err != nil {
		return err
	}
	if got != manifestCID {
		return fmt.Errorf("the bytes read back by %s hash to %s", manifestCID, got)
	}
	if err := sds.VerifyBareStructure("DPM.fbs", storage.BareDPMBytes(manifest)); err != nil {
		return fmt.Errorf("structure: %w", err)
	}
	// The node verified the manifest's provider signature with its own key and
	// matched its CID to the $PNM's before it recorded the lane's channel.
	resp, err := httpClient.Get(base + "/api/v1/channels/" + provider + "-OMM")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var channel struct {
		PNMVerified bool `json:"pnmVerified"`
		DPMVerified bool `json:"dpmVerified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&channel); err != nil {
		return fmt.Errorf("channel detail (HTTP %d): %w", resp.StatusCode, err)
	}
	if !channel.PNMVerified || !channel.DPMVerified {
		return fmt.Errorf("the node's channel for the lane is pnmVerified=%v dpmVerified=%v", channel.PNMVerified, channel.DPMVerified)
	}
	return nil
}

func names(ctx context.Context, k kubo, manifest []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed manifest: %v", r)
		}
	}()
	root, err := readDPM(manifest)
	if err != nil {
		return err
	}
	if got := root.SOURCESLength(); got != batches {
		return fmt.Errorf("manifest names %d source batches, want %d", got, batches)
	}
	if got := root.QUERY(nil).BATCH_IDSLength(); got != batches {
		return fmt.Errorf("manifest query names %d batch ids, want %d", got, batches)
	}
	var asset dpm.DPMAsset
	for i := 0; i < root.ASSETSLength(); i++ {
		if !root.ASSETS(&asset, i) || asset.ASSET_KIND().String() != "DATA_SHARD" {
			continue
		}
		data, status, err := k.post(ctx, "/api/v0/cat", url.Values{"arg": {string(asset.CID())}})
		if err != nil || status != http.StatusOK {
			return fmt.Errorf("cat shard %s: HTTP %d %v", asset.CID(), status, err)
		}
		sum := sha256.Sum256(data)
		if uint64(len(data)) != asset.BYTE_LENGTH() || hex.EncodeToString(sum[:]) != strings.ToLower(string(asset.BYTE_SHA256())) {
			return fmt.Errorf("shard %s is %d bytes sha256 %x, the manifest says %d bytes %s", asset.CID(), len(data), sum, asset.BYTE_LENGTH(), asset.BYTE_SHA256())
		}
		fmt.Printf("     %d batches and batch ids; shard %s reads back (%d bytes)\n", batches, asset.CID(), len(data))
		return nil
	}
	return errors.New("manifest has no data shard")
}

func readDPM(manifest []byte) (root *dpm.DPM, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed manifest: %v", r)
		}
	}()
	return dpm.GetRootAsDPM(storage.BareDPMBytes(manifest), 0), nil
}

// ---------------------------------------------------------------------------
// node

type node struct {
	binary string
	base   string
	kubo   string
	config string
	home   string
	cmd    *exec.Cmd
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// prepare writes the node's home and config: isolated, with the auth wall on
// and the loopback console admitted as the operator.
func prepare(binary, root string) (*node, error) {
	home, err := os.MkdirTemp("", "dpm-manifest-e2e-home-")
	if err != nil {
		return nil, err
	}
	if userHome, err := os.UserHomeDir(); err == nil {
		aot := filepath.Join(userHome, "Library", "Caches", "flatsql-aot")
		if _, err := os.Stat(aot); err == nil {
			_ = os.MkdirAll(filepath.Join(home, "Library", "Caches"), 0o755)
			_ = os.Symlink(aot, filepath.Join(home, "Library", "Caches", "flatsql-aot"))
		}
	}
	ports := make([]int, 3)
	for i := range ports {
		if ports[i], err = freePort(); err != nil {
			return nil, err
		}
	}
	port, kuboAPI, kuboGateway := ports[0], ports[1], ports[2]
	config := filepath.Join(home, "config.yaml")
	yaml := strings.Join([]string{
		"mode: full",
		"network:", "  listen:", "    - /ip4/127.0.0.1/tcp/0", "  bootstrap: []", "  edge_relays: []", "  enable_relay: false",
		"storage:", fmt.Sprintf("  path: %q", root), "  max_size: 1GB",
		"peers:", "  strict_mode: false", "  enable_dht: false", "  enable_mdns: false",
		"admin:", "  enabled: true", fmt.Sprintf("  listen_addr: \"127.0.0.1:%d\"", port), "  require_auth: true", "  tls_mode: disabled",
		"  local_console: true", "  remote_requires_sealed: false",
		fmt.Sprintf("  ipfs_api_url: \"http://127.0.0.1:%d\"", kuboAPI), fmt.Sprintf("  ipfs_gateway_url: \"http://127.0.0.1:%d\"", kuboGateway),
		"publishing:", "  enabled: true", "  ipfs_schemas:", "    - " + schema,
		"flows:", "  mounts: []",
		"setup:", fmt.Sprintf("  data_path: %q", root), "",
	}, "\n")
	if err := os.WriteFile(config, []byte(yaml), 0o644); err != nil {
		return nil, err
	}
	return &node{
		binary: binary,
		base:   fmt.Sprintf("http://127.0.0.1:%d", port),
		kubo:   fmt.Sprintf("http://127.0.0.1:%d", kuboAPI),
		config: config,
		home:   home,
	}, nil
}

// start boots the node and waits for health 200.
func (n *node) start(ctx context.Context) error {
	logFile, err := os.OpenFile(filepath.Join(n.home, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(n.binary, "daemon", "--allow-multi-daemon", "--config", n.config)
	cmd.Dir, cmd.Stdout, cmd.Stderr = n.home, logFile, logFile
	cmd.Env = append(os.Environ(), "HOME="+n.home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	n.cmd = cmd
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t0 := time.Now()
	for time.Since(t0) < 5*time.Minute {
		select {
		case err := <-exited:
			tail, _ := os.ReadFile(filepath.Join(n.home, "daemon.log"))
			n.cmd = nil
			return fmt.Errorf("node exited before health (%v): %s", err, lastLines(tail, 8))
		case <-ctx.Done():
			n.stop()
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		resp, err := http.Get(n.base + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				fmt.Printf("PASS boot pid=%d %s (Kubo %s) after %.1f s\n", cmd.Process.Pid, n.base, n.kubo, time.Since(t0).Seconds())
				return nil
			}
		}
	}
	n.stop()
	return errors.New("no health 200 within 5 min")
}

// stop ends exactly the process group this harness started.
func (n *node) stop() {
	if n.cmd == nil || n.cmd.Process == nil {
		return
	}
	pgid := n.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	for i := 0; i < 120; i++ {
		if syscall.Kill(-pgid, 0) != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	n.cmd = nil
}

func (n *node) cleanup() {
	n.stop()
	if os.Getenv("DPM_MANIFEST_E2E_KEEP") == "" {
		_ = os.RemoveAll(n.home)
	} else {
		fmt.Printf("     kept %s\n", n.home)
	}
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

var httpClient = &http.Client{Timeout: 5 * time.Minute}

// kubo reads the node's Kubo RPC API directly.
type kubo struct{ base string }

func (k kubo) post(ctx context.Context, command string, query url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.base+command+"?"+query.Encode(), nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

// pins is Kubo's recursive and direct pin set: CID -> pin type.
func (k kubo) pins(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for _, kind := range []string{"recursive", "direct"} {
		data, status, err := k.post(ctx, "/api/v0/pin/ls", url.Values{"type": {kind}})
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("pin/ls %s: HTTP %d %s", kind, status, clip(data))
		}
		var listing struct {
			Keys map[string]struct{ Type string }
		}
		if err := json.Unmarshal(data, &listing); err != nil {
			return nil, fmt.Errorf("pin/ls %s: %w", kind, err)
		}
		for c, entry := range listing.Keys {
			out[c] = entry.Type
		}
	}
	return out, nil
}

func clip(body []byte) string {
	if len(body) > 300 {
		body = body[:300]
	}
	return strings.TrimSpace(string(body))
}
