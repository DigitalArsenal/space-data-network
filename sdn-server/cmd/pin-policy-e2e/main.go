// pin-policy-e2e boots a node built from this checkout on a fresh data
// directory with its own Kubo and proves the node pin policy ($PIN):
//
//	a. a dataset publication of a seeded lane, an open publish (share) and the
//	   module and flow IPFS capabilities' add + pin_add leave Kubo's recursive
//	   and direct pin set holding only $EPM records;
//	b. GET /api/node/pins answers the default policy: NODE_EPM and ACCOUNT_EPM
//	   on and locked (NODE_EPM holding bytes), every other class off and empty;
//	c. POST enabling PUBLICATION for the lane's feed pins the lane's manifest,
//	   shard and index, and disabling it unpins them;
//	d. a change to NODE_EPM, a JSON body and a request without an operator
//	   session are refused.
//
//	pin-policy-e2e --binary <node>
package main

import (
	"bytes"
	"context"
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
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt/capabilities"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/typedfb/ingestresult"
)

const (
	schema     = "OMM.fbs"
	streamType = "application/vnd.sdn.flatbuffers.stream"
	provider   = "e2e-provider"
	lane       = "lane-a"
	batch      = "b-lane-a"
	peerID     = "pin-policy-e2e"
	pinsPath   = "/api/node/pins"
)

var day0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func omm(norad uint32, day int) []byte {
	epoch := day0.Add(time.Duration(day) * 24 * time.Hour)
	return sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(fmt.Sprintf("PIN-E2E-%d-D%d", norad, day)).
		WithEpoch(epoch.Format(time.RFC3339)).
		WithEpochTimestamp(float64(epoch.Unix())).
		WithMeanMotion(15.5).
		Build()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Printf("PIN-POLICY-E2E FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PIN-POLICY-E2E PASS")
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

// published is what the harness made the node add: the lane's publication
// CIDs, the shared record and the capability files.
type published struct {
	shard, index, manifest string
	share                  string
	capability             []string
}

func (p published) lane() []string { return []string{p.shard, p.index, p.manifest} }

func run(ctx context.Context) error {
	binary := flag.String("binary", "", "node binary to boot")
	flag.Parse()
	if *binary == "" {
		return errors.New("--binary is required")
	}
	root, err := os.MkdirTemp("", "pin-policy-e2e-store-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := seed(root); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	node, err := boot(ctx, *binary, root)
	if err != nil {
		return err
	}
	defer node.stop()
	c := client{base: node.base}
	k := kubo{base: node.kubo}
	feed := pinpolicy.PublicationFeed(schema, provider, lane)

	var pub published
	check("a publication, share and capability adds pin only $EPM records", func() error {
		var err error
		if pub, err = exercise(ctx, c, k); err != nil {
			return err
		}
		return onlyEPMPinned(ctx, k, pub)
	}())
	check("b GET /api/node/pins answers the default policy", defaultPolicy(ctx, c))
	if pub.manifest != "" {
		check("c PUBLICATION feed rule pins and unpins the lane", toggleLane(ctx, c, k, feed, pub))
	} else {
		check("c PUBLICATION feed rule pins and unpins the lane", errors.New("no publication to pin (a failed)"))
	}
	check("d refusals: locked rule, JSON body, no operator session", refusals(ctx, c))
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
	tags := storage.SourceTags{ProviderID: provider, SourceName: lane, BatchID: batch, ProducerPeerID: peerID}
	for i, norad := range []uint32{25544, 43013, 48274} {
		if _, err := store.StoreWithSourceTags(schema, omm(norad, i), peerID, nil, tags); err != nil {
			_ = store.Close()
			return err
		}
	}
	return store.Close()
}

// ---------------------------------------------------------------------------
// a

var (
	shardRE    = regexp.MustCompile(`"shardCid"\s*:\s*"([^"]+)"`)
	indexRE    = regexp.MustCompile(`"indexCid"\s*:\s*"([^"]+)"`)
	manifestRE = regexp.MustCompile(`"manifestCid"\s*:\s*"([^"]+)"`)
	cidRE      = regexp.MustCompile(`"cid"\s*:\s*"([^"]+)"`)
	pinnedRE   = regexp.MustCompile(`"pinned"\s*:\s*(true|false)`)
	hashRE     = regexp.MustCompile(`"Hash"\s*:\s*"([^"]+)"`)
)

func exercise(ctx context.Context, c client, k kubo) (published, error) {
	var out published

	// The lane's dataset publication, named by the IngestResult a typed
	// ingest answers.
	b := flatbuffers.NewBuilder(256)
	schemaOff, providerOff := b.CreateString(schema), b.CreateString(provider)
	laneOff, batchOff := b.CreateString(lane), b.CreateString(batch)
	ingestresult.IngestResultStart(b)
	ingestresult.IngestResultAddSchema(b, schemaOff)
	ingestresult.IngestResultAddProviderId(b, providerOff)
	ingestresult.IngestResultAddSourceName(b, laneOff)
	ingestresult.IngestResultAddBatchId(b, batchOff)
	ingestresult.IngestResultAddInserted(b, 3)
	ingestresult.FinishIngestResultBuffer(b, ingestresult.IngestResultEnd(b))
	a, err := c.do(ctx, http.MethodPost, "/api/v1/admin/dataset-updates/publish", "application/octet-stream", b.FinishedBytes(), false)
	if err != nil {
		return out, err
	}
	if a.status != http.StatusAccepted {
		return out, fmt.Errorf("publish: HTTP %d %s", a.status, clip(a.body))
	}
	out.shard, out.index, out.manifest = first(shardRE, a.body), first(indexRE, a.body), first(manifestRE, a.body)
	if out.shard == "" || out.index == "" || out.manifest == "" {
		return out, fmt.Errorf("publish named no shard/index/manifest: %s", clip(a.body))
	}
	fmt.Printf("     publication %s: manifest %s shard %s index %s\n", pinpolicy.PublicationFeed(schema, provider, lane), out.manifest, out.shard, out.index)

	// A shared record (the publish route puts IPFS-schema records in the
	// blockstore).
	a, err = c.do(ctx, http.MethodPost, "/api/v1/data/publish/"+schema, "application/octet-stream", omm(99001, 7), false)
	if err != nil {
		return out, err
	}
	if a.status != http.StatusCreated {
		return out, fmt.Errorf("share: HTTP %d %s", a.status, clip(a.body))
	}
	out.share = first(cidRE, a.body)
	if out.share == "" || first(pinnedRE, a.body) != "false" {
		return out, fmt.Errorf("share answered %s, want a CID with pinned false", clip(a.body))
	}
	fmt.Printf("     share %s stored in the blockstore, pinned=false\n", out.share)

	// The module and flow IPFS capabilities, driven through their host
	// handlers against the node's own Kubo (a capability call from a running
	// module or flow needs a signed artifact; these are the handlers such a
	// call reaches). The policy is the default one: FLOW_ASSET off.
	pins, err := pinpolicy.New(k.base, nil)
	if err != nil {
		return out, err
	}
	module := caps.NewIPFSCapFactory(k.base, nil, pins)(nil)
	resp, err := module("ipfs.add", []byte(`{"data":"pin-policy-e2e star catalog"}`))
	if err != nil {
		return out, err
	}
	moduleCID := first(hashRE, resp)
	if moduleCID == "" {
		return out, fmt.Errorf("module ipfs.add answered %s", clip(resp))
	}
	if resp, err = module("ipfs.pin_add", []byte(`{"cid":"`+moduleCID+`"}`)); err != nil {
		return out, err
	}
	if !bytes.Contains(resp, []byte(`"Pinned":false`)) || !bytes.Contains(resp, []byte(`"ok":true`)) {
		return out, fmt.Errorf("module ipfs.pin_add answered %s, want ok with Pinned false", clip(resp))
	}
	flow := capabilities.NewIPFSHandlers(capabilities.IPFSConfig{APIURL: k.base, Pins: pins})
	args := &flowrt.InvocationArgs{FlowID: "pin-policy-e2e-flow", Frames: []flowrt.FrameData{{PortID: "input", Bytes: []byte("pin-policy-e2e error model")}}}
	result, err := flow["io.spacedatanetwork.ipfs:add"](ctx, args)
	if err != nil || result.StatusCode != 0 || len(result.Outputs) == 0 {
		return out, fmt.Errorf("flow add: %v %+v", err, result)
	}
	flowCID := first(hashRE, result.Outputs[0].Bytes)
	args.Frames = []flowrt.FrameData{{PortID: "input", Bytes: []byte(`{"cid":"` + flowCID + `"}`)}}
	result, err = flow["io.spacedatanetwork.ipfs:pin_add"](ctx, args)
	if err != nil || result.StatusCode != 0 || len(result.Outputs) == 0 || !bytes.Contains(result.Outputs[0].Bytes, []byte(`"Pinned":false`)) {
		return out, fmt.Errorf("flow pin_add: %v %+v", err, result)
	}
	out.capability = []string{moduleCID, flowCID}
	fmt.Printf("     module add %s, flow add %s: added, pin_add Pinned=false\n", moduleCID, flowCID)
	for _, cid := range append(append(out.lane(), out.share), out.capability...) {
		if ok, err := k.has(ctx, cid); err != nil || !ok {
			return out, fmt.Errorf("%s is not in the node's blockstore (%v)", cid, err)
		}
	}
	fmt.Println("     every added CID is held in the node's blockstore")
	return out, nil
}

func onlyEPMPinned(ctx context.Context, k kubo, pub published) error {
	// Background lanes (tips, retention) settle within a few seconds.
	time.Sleep(3 * time.Second)
	pinned, err := k.pins(ctx)
	if err != nil {
		return err
	}
	if len(pinned) == 0 {
		return errors.New("Kubo pins nothing: the node's own $EPM is missing")
	}
	for _, cid := range sortedKeys(pinned) {
		data, err := k.block(ctx, cid)
		if err != nil {
			return fmt.Errorf("pinned %s: %w", cid, err)
		}
		if !isEPM(data) {
			return fmt.Errorf("pinned %s (%s) is not an $EPM record (%d bytes)", cid, pinned[cid], len(data))
		}
	}
	for _, cid := range append(append(pub.lane(), pub.share), pub.capability...) {
		if _, ok := pinned[cid]; ok {
			return fmt.Errorf("%s is pinned", cid)
		}
	}
	fmt.Printf("     Kubo pins %d root(s), every one an $EPM record: %s\n", len(pinned), strings.Join(sortedKeys(pinned), ", "))
	return nil
}

func isEPM(data []byte) bool {
	return (len(data) >= 8 && string(data[4:8]) == "$EPM") || (len(data) >= 12 && string(data[8:12]) == "$EPM")
}

// ---------------------------------------------------------------------------
// b, c, d

func readPolicy(a answer) ([]pinpolicy.RuleChange, error) {
	if a.status != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d %s", a.status, clip(a.body))
	}
	if ct := a.hdr.Get("Content-Type"); ct != streamType || a.hdr.Get("X-SDN-Schema") != "PIN.fbs" {
		return nil, fmt.Errorf("Content-Type %q X-SDN-Schema %q", ct, a.hdr.Get("X-SDN-Schema"))
	}
	rules, _, err := pinpolicy.Decode(a.body)
	return rules, err
}

func findRule(rules []pinpolicy.RuleChange, class pinpolicy.Class, feed string) (pinpolicy.RuleChange, bool) {
	for _, rule := range rules {
		if rule.Class == class && rule.Feed == feed {
			return rule, true
		}
	}
	return pinpolicy.RuleChange{}, false
}

func defaultPolicy(ctx context.Context, c client) error {
	a, err := c.do(ctx, http.MethodGet, pinsPath, "", nil, false)
	if err != nil {
		return err
	}
	rules, err := readPolicy(a)
	if err != nil {
		return err
	}
	for class := pinpolicy.NodeEPM; class <= pinpolicy.ProfilePhoto; class++ {
		rule, ok := findRule(rules, class, "")
		if !ok {
			return fmt.Errorf("no %s class rule", class)
		}
		if class.Locked() {
			if !rule.Enabled || !rule.Locked {
				return fmt.Errorf("%s enabled=%t locked=%t, want both", class, rule.Enabled, rule.Locked)
			}
			if class == pinpolicy.NodeEPM && (rule.PinnedBytes == 0 || rule.PinnedRoots == 0) {
				return fmt.Errorf("NODE_EPM holds %d roots / %d bytes, want > 0", rule.PinnedRoots, rule.PinnedBytes)
			}
			fmt.Printf("     %-13s on, locked, %d root(s) %d bytes\n", class, rule.PinnedRoots, rule.PinnedBytes)
			continue
		}
		if rule.Enabled || rule.Locked || rule.PinnedRoots != 0 || rule.PinnedBytes != 0 {
			return fmt.Errorf("%s enabled=%t locked=%t roots=%d bytes=%d, want off and empty", class, rule.Enabled, rule.Locked, rule.PinnedRoots, rule.PinnedBytes)
		}
	}
	if len(rules) != int(pinpolicy.ProfilePhoto)+1 {
		return fmt.Errorf("%d rules, want one per class", len(rules))
	}
	fmt.Println("     every other class off, 0 roots, 0 bytes")
	return nil
}

func toggleLane(ctx context.Context, c client, k kubo, feed string, pub published) error {
	set := func(enabled bool) (pinpolicy.RuleChange, error) {
		frame := pinpolicy.EncodeChanges(pinpolicy.RuleChange{Class: pinpolicy.Publication, Feed: feed, Enabled: enabled})
		a, err := c.do(ctx, http.MethodPost, pinsPath, streamType, frame, false)
		if err != nil {
			return pinpolicy.RuleChange{}, err
		}
		rules, err := readPolicy(a)
		if err != nil {
			return pinpolicy.RuleChange{}, err
		}
		rule, ok := findRule(rules, pinpolicy.Publication, feed)
		if !ok {
			return rule, fmt.Errorf("the answer has no PUBLICATION %s rule", feed)
		}
		return rule, nil
	}
	rule, err := set(true)
	if err != nil {
		return err
	}
	if !rule.Enabled || rule.PinnedBytes == 0 || rule.PinnedRoots < 3 {
		return fmt.Errorf("enabled rule: enabled=%t roots=%d bytes=%d, want on, >= 3 roots, > 0 bytes", rule.Enabled, rule.PinnedRoots, rule.PinnedBytes)
	}
	pinned, err := k.pins(ctx)
	if err != nil {
		return err
	}
	for _, cid := range pub.lane() {
		if pinned[cid] != "recursive" {
			return fmt.Errorf("after enable %s is %q in Kubo, want recursive", cid, pinned[cid])
		}
	}
	fmt.Printf("     enable %s: %d root(s) %d bytes; manifest, shard and index pinned\n", feed, rule.PinnedRoots, rule.PinnedBytes)
	if rule, err = set(false); err != nil {
		return err
	}
	if rule.Enabled || rule.PinnedRoots != 0 || rule.PinnedBytes != 0 {
		return fmt.Errorf("disabled rule: enabled=%t roots=%d bytes=%d, want off and empty", rule.Enabled, rule.PinnedRoots, rule.PinnedBytes)
	}
	if pinned, err = k.pins(ctx); err != nil {
		return err
	}
	for _, cid := range pub.lane() {
		if kind, ok := pinned[cid]; ok {
			return fmt.Errorf("after disable %s is still pinned (%s)", cid, kind)
		}
	}
	for _, cid := range pub.lane() {
		if ok, err := k.has(ctx, cid); err != nil || !ok {
			return fmt.Errorf("after disable %s left the blockstore (%v): unpin must not collect", cid, err)
		}
	}
	fmt.Printf("     disable %s: 0 roots, 0 bytes; unpinned, bytes still held\n", feed)
	return nil
}

func refusals(ctx context.Context, c client) error {
	locked := pinpolicy.EncodeChanges(pinpolicy.RuleChange{Class: pinpolicy.NodeEPM, Enabled: false})
	a, err := c.do(ctx, http.MethodPost, pinsPath, streamType, locked, false)
	if err != nil {
		return err
	}
	if a.status != http.StatusBadRequest {
		return fmt.Errorf("NODE_EPM change: HTTP %d, want 400", a.status)
	}
	fmt.Println("     POST NODE_EPM off: 400")
	a, err = c.do(ctx, http.MethodPost, pinsPath, "application/json", []byte(`{"rules":[{"class":"PUBLICATION","enabled":true}]}`), false)
	if err != nil {
		return err
	}
	if a.status != http.StatusUnsupportedMediaType {
		return fmt.Errorf("JSON body: HTTP %d, want 415", a.status)
	}
	fmt.Println("     POST JSON body: 415")
	a, err = c.do(ctx, http.MethodPost, pinsPath, streamType, []byte("not a frame"), false)
	if err != nil {
		return err
	}
	if a.status != http.StatusBadRequest {
		return fmt.Errorf("non-$PIN frame: HTTP %d, want 400", a.status)
	}
	fmt.Println("     POST a body that is not one $PIN frame: 400")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		var body []byte
		if method == http.MethodPost {
			body = pinpolicy.EncodeChanges(pinpolicy.RuleChange{Class: pinpolicy.Share, Enabled: true})
		}
		a, err = c.do(ctx, method, pinsPath, streamType, body, true)
		if err != nil {
			return err
		}
		if a.status != http.StatusUnauthorized && a.status != http.StatusForbidden {
			return fmt.Errorf("%s without an operator session: HTTP %d, want 401/403", method, a.status)
		}
		fmt.Printf("     %s without an operator session: %d\n", method, a.status)
	}
	// The refused change left the policy as it was.
	a, err = c.do(ctx, http.MethodGet, pinsPath, "", nil, false)
	if err != nil {
		return err
	}
	rules, err := readPolicy(a)
	if err != nil {
		return err
	}
	if rule, _ := findRule(rules, pinpolicy.Share, ""); rule.Enabled {
		return errors.New("an anonymous POST turned SHARE on")
	}
	return nil
}

// ---------------------------------------------------------------------------
// node

type node struct {
	base string
	kubo string
	cmd  *exec.Cmd
	home string
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// boot starts the node isolated with the auth wall on and the loopback
// console admitted as the operator: a plain loopback call is the operator, a
// call carrying a proxy header is anonymous.
func boot(ctx context.Context, binary, root string) (*node, error) {
	home, err := os.MkdirTemp("", "pin-policy-e2e-home-")
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
	logFile, err := os.Create(filepath.Join(home, "daemon.log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, "daemon", "--allow-multi-daemon", "--config", config)
	cmd.Dir, cmd.Stdout, cmd.Stderr = home, logFile, logFile
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	n := &node{base: fmt.Sprintf("http://127.0.0.1:%d", port), kubo: fmt.Sprintf("http://127.0.0.1:%d", kuboAPI), cmd: cmd, home: home}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t0 := time.Now()
	for time.Since(t0) < 5*time.Minute {
		select {
		case err := <-exited:
			tail, _ := os.ReadFile(filepath.Join(home, "daemon.log"))
			n.cmd = nil
			n.stop()
			return nil, fmt.Errorf("node exited before health (%v): %s", err, lastLines(tail, 8))
		case <-ctx.Done():
			n.stop()
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		resp, err := http.Get(n.base + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				fmt.Printf("PASS boot pid=%d %s (Kubo %s) after %.1f s\n", cmd.Process.Pid, n.base, n.kubo, time.Since(t0).Seconds())
				return n, nil
			}
		}
	}
	n.stop()
	return nil, errors.New("no health 200 within 5 min")
}

// stop ends exactly the process group this harness started.
func (n *node) stop() {
	if n.cmd != nil && n.cmd.Process != nil {
		pgid := n.cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		for i := 0; i < 60; i++ {
			if syscall.Kill(-pgid, 0) != nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	if os.Getenv("PIN_POLICY_E2E_KEEP") == "" {
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

// ---------------------------------------------------------------------------
// wire

type client struct{ base string }

type answer struct {
	status int
	hdr    http.Header
	body   []byte
}

var httpClient = &http.Client{Timeout: 5 * time.Minute}

func (c client) do(ctx context.Context, method, path, contentType string, body []byte, anonymous bool) (answer, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return answer{}, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if anonymous {
		// A proxy hop refuses the loopback console session.
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, resp.Header, data}, err
}

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
		for cid, entry := range listing.Keys {
			out[cid] = entry.Type
		}
	}
	return out, nil
}

// has reports a CID's root block in the blockstore, never fetching it.
func (k kubo) has(ctx context.Context, cid string) (bool, error) {
	_, status, err := k.post(ctx, "/api/v0/block/stat", url.Values{"arg": {cid}, "offline": {"true"}})
	if err != nil {
		return false, err
	}
	return status == http.StatusOK, nil
}

func (k kubo) block(ctx context.Context, cid string) ([]byte, error) {
	data, status, err := k.post(ctx, "/api/v0/block/get", url.Values{"arg": {cid}, "offline": {"true"}})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("block/get: HTTP %d %s", status, clip(data))
	}
	return data, nil
}

func first(re *regexp.Regexp, body []byte) string {
	if m := re.FindSubmatch(body); m != nil {
		return string(m[1])
	}
	return ""
}

func clip(body []byte) string {
	if len(body) > 300 {
		body = body[:300]
	}
	return strings.TrimSpace(string(body))
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
