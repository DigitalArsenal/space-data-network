// pin-policy-e2e boots a node built from this checkout on a fresh data
// directory with its own Kubo and proves the node pin policy ($PIN) and its
// default (owner 2026-10-19: the node's $EPMs, what it publishes and the files
// its flows add are pinned; every other class is off):
//
//	a. with no operator rule, a dataset publication of a seeded lane is
//	   pinned (manifest, shard and index), and the module and flow IPFS
//	   capabilities' add and pin_add pin; an open publish (share) is not
//	   pinned; every other pinned root is an $EPM record or counted under
//	   PUBLICATION;
//	b. GET /api/node/pins answers the default: NODE_EPM and ACCOUNT_EPM on and
//	   locked, PUBLICATION and FLOW_ASSET on, every other class off and empty;
//	c. turning the PUBLICATION class off unpins the lane (its bytes stay in
//	   the blockstore), and the node persists that one operator rule only;
//	d. a change to NODE_EPM, a JSON body and a request without an operator
//	   session are refused;
//	e. after a restart the persisted operator rule still wins: the lane stays
//	   unpinned;
//	f. a restart with no operator rule (the state a node rolled from an older
//	   default is in) pins at boot what the node ledgered unpinned.
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
	// policyFile is where the store persists the operator's rules.
	policyFile = "pin-policy.pin"
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
	node, err := prepare(*binary, root)
	if err != nil {
		return err
	}
	defer node.cleanup()
	if err := node.start(ctx); err != nil {
		return err
	}
	c := client{base: node.base}
	k := kubo{base: node.kubo}

	var pub published
	check("a with no operator rule a publication and capability adds pin; a share does not", func() error {
		var err error
		if pub, err = exercise(ctx, c, k); err != nil {
			return err
		}
		return pinnedByDefault(ctx, c, k, pub)
	}())
	check("b GET /api/node/pins answers the default policy", defaultPolicy(ctx, c))
	laneReady := pub.manifest != ""
	if laneReady {
		check("c PUBLICATION off unpins the lane and persists one operator rule", publicationOff(ctx, c, k, root, pub))
	} else {
		check("c PUBLICATION off unpins the lane and persists one operator rule", errors.New("no publication (a failed)"))
	}
	check("d refusals: locked rule, JSON body, no operator session", refusals(ctx, c))

	node.stop()
	if err := node.start(ctx); err != nil {
		return err
	}
	if laneReady {
		check("e after a restart the operator's PUBLICATION off still wins", operatorRuleWins(ctx, c, k, pub))
	}

	node.stop()
	if err := os.Remove(filepath.Join(root, policyFile)); err != nil {
		return fmt.Errorf("drop the operator rule: %w", err)
	}
	if err := node.start(ctx); err != nil {
		return err
	}
	if laneReady {
		check("f with no operator rule the node pins at boot what it ledgered unpinned", bootReconcile(ctx, c, k, pub))
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
	// blockstore). SHARE stays off by default.
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
	// call reaches). The policy is a fresh one with no operator rule.
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
	if !bytes.Contains(resp, []byte(`"Pinned":true`)) || !bytes.Contains(resp, []byte(`"ok":true`)) {
		return out, fmt.Errorf("module ipfs.pin_add answered %s, want ok with Pinned true", clip(resp))
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
	if err != nil || result.StatusCode != 0 || len(result.Outputs) == 0 || !bytes.Contains(result.Outputs[0].Bytes, []byte(`"Pinned":true`)) {
		return out, fmt.Errorf("flow pin_add: %v %+v", err, result)
	}
	out.capability = []string{moduleCID, flowCID}
	fmt.Printf("     module add %s, flow add %s: pin_add Pinned=true\n", moduleCID, flowCID)
	for _, cid := range append(append(out.lane(), out.share), out.capability...) {
		if ok, err := k.has(ctx, cid); err != nil || !ok {
			return out, fmt.Errorf("%s is not in the node's blockstore (%v)", cid, err)
		}
	}
	fmt.Println("     every added CID is held in the node's blockstore")
	return out, nil
}

// pinnedByDefault checks Kubo's pin set after a: the lane and the capability
// files pinned, the share not, and nothing else but $EPM records and what
// the node counts under PUBLICATION.
func pinnedByDefault(ctx context.Context, c client, k kubo, pub published) error {
	// Background lanes (tips, retention) settle within a few seconds.
	time.Sleep(3 * time.Second)
	pinned, err := k.pins(ctx)
	if err != nil {
		return err
	}
	for _, cid := range append(pub.lane(), pub.capability...) {
		if pinned[cid] != "recursive" {
			return fmt.Errorf("%s is %q in Kubo, want recursive", cid, pinned[cid])
		}
	}
	if kind, ok := pinned[pub.share]; ok {
		return fmt.Errorf("the share %s is pinned (%s)", pub.share, kind)
	}
	capability := map[string]bool{}
	for _, cid := range pub.capability {
		capability[cid] = true
	}
	epms, others := 0, 0
	for _, cid := range sortedKeys(pinned) {
		if capability[cid] {
			continue
		}
		data, err := k.block(ctx, cid)
		if err != nil {
			return fmt.Errorf("pinned %s: %w", cid, err)
		}
		if isEPM(data) {
			epms++
		} else {
			others++
		}
	}
	if epms == 0 {
		return errors.New("Kubo pins no $EPM record: the node's own $EPM is missing")
	}
	a, err := c.do(ctx, http.MethodGet, pinsPath, "", nil, false)
	if err != nil {
		return err
	}
	rules, err := readPolicy(a)
	if err != nil {
		return err
	}
	publication, _ := findRule(rules, pinpolicy.Publication, "")
	if uint64(others) != publication.PinnedRoots {
		return fmt.Errorf("Kubo pins %d root(s) that are not $EPM records or capability files, PUBLICATION counts %d", others, publication.PinnedRoots)
	}
	fmt.Printf("     Kubo pins the lane's manifest, shard and index, both capability files, %d $EPM record(s) and %d PUBLICATION root(s) in all; the share is not pinned\n", epms, others)
	return nil
}

func isEPM(data []byte) bool {
	return (len(data) >= 8 && string(data[4:8]) == "$EPM") || (len(data) >= 12 && string(data[8:12]) == "$EPM")
}

// ---------------------------------------------------------------------------
// b to f

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

func getPolicy(ctx context.Context, c client) ([]pinpolicy.RuleChange, error) {
	a, err := c.do(ctx, http.MethodGet, pinsPath, "", nil, false)
	if err != nil {
		return nil, err
	}
	return readPolicy(a)
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
	rules, err := getPolicy(ctx, c)
	if err != nil {
		return err
	}
	for class := pinpolicy.NodeEPM; class <= pinpolicy.ProfilePhoto; class++ {
		rule, ok := findRule(rules, class, "")
		if !ok {
			return fmt.Errorf("no %s class rule", class)
		}
		switch class {
		case pinpolicy.NodeEPM, pinpolicy.AccountEPM:
			if !rule.Enabled || !rule.Locked {
				return fmt.Errorf("%s enabled=%t locked=%t, want both", class, rule.Enabled, rule.Locked)
			}
			if class == pinpolicy.NodeEPM && (rule.PinnedBytes == 0 || rule.PinnedRoots == 0) {
				return fmt.Errorf("NODE_EPM holds %d roots / %d bytes, want > 0", rule.PinnedRoots, rule.PinnedBytes)
			}
			fmt.Printf("     %-13s on, locked, %d root(s) %d bytes\n", class, rule.PinnedRoots, rule.PinnedBytes)
		case pinpolicy.Publication:
			if !rule.Enabled || rule.Locked || rule.PinnedRoots < 3 || rule.PinnedBytes == 0 {
				return fmt.Errorf("PUBLICATION enabled=%t locked=%t roots=%d bytes=%d, want on, switchable, >= 3 roots, > 0 bytes", rule.Enabled, rule.Locked, rule.PinnedRoots, rule.PinnedBytes)
			}
			fmt.Printf("     %-13s on, switchable, %d root(s) %d bytes\n", class, rule.PinnedRoots, rule.PinnedBytes)
		case pinpolicy.FlowAsset:
			if !rule.Enabled || rule.Locked {
				return fmt.Errorf("FLOW_ASSET enabled=%t locked=%t, want on and switchable", rule.Enabled, rule.Locked)
			}
			fmt.Printf("     %-13s on, switchable\n", class)
		default:
			if rule.Enabled || rule.Locked || rule.PinnedRoots != 0 || rule.PinnedBytes != 0 {
				return fmt.Errorf("%s enabled=%t locked=%t roots=%d bytes=%d, want off and empty", class, rule.Enabled, rule.Locked, rule.PinnedRoots, rule.PinnedBytes)
			}
		}
	}
	if len(rules) != int(pinpolicy.ProfilePhoto)+1 {
		return fmt.Errorf("%d rules, want one per class", len(rules))
	}
	fmt.Println("     every other class off, 0 roots, 0 bytes")
	return nil
}

// lanePinned reports how many of the lane's CIDs Kubo pins recursively.
func lanePinned(ctx context.Context, k kubo, pub published) (int, error) {
	pinned, err := k.pins(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, cid := range pub.lane() {
		if pinned[cid] == "recursive" {
			n++
		}
	}
	return n, nil
}

func publicationOff(ctx context.Context, c client, k kubo, root string, pub published) error {
	frame := pinpolicy.EncodeChanges(pinpolicy.RuleChange{Class: pinpolicy.Publication, Enabled: false})
	a, err := c.do(ctx, http.MethodPost, pinsPath, streamType, frame, false)
	if err != nil {
		return err
	}
	rules, err := readPolicy(a)
	if err != nil {
		return err
	}
	rule, _ := findRule(rules, pinpolicy.Publication, "")
	if rule.Enabled || rule.PinnedRoots != 0 || rule.PinnedBytes != 0 {
		return fmt.Errorf("PUBLICATION after off: enabled=%t roots=%d bytes=%d, want off and empty", rule.Enabled, rule.PinnedRoots, rule.PinnedBytes)
	}
	if n, err := lanePinned(ctx, k, pub); err != nil || n != 0 {
		return fmt.Errorf("after off %d of the lane's CIDs are still pinned (%v)", n, err)
	}
	for _, cid := range pub.lane() {
		if ok, err := k.has(ctx, cid); err != nil || !ok {
			return fmt.Errorf("after off %s left the blockstore (%v): unpin must not collect", cid, err)
		}
	}
	fmt.Println("     PUBLICATION off: 0 roots, 0 bytes; the lane unpinned, its bytes still held")
	stored, err := os.ReadFile(filepath.Join(root, policyFile))
	if err != nil {
		return fmt.Errorf("read the persisted policy: %w", err)
	}
	persisted, _, err := pinpolicy.Decode(stored)
	if err != nil {
		return fmt.Errorf("decode the persisted policy: %w", err)
	}
	if len(persisted) != 1 || persisted[0].Class != pinpolicy.Publication || persisted[0].Feed != "" || persisted[0].Enabled {
		return fmt.Errorf("the persisted policy holds %+v, want the one operator rule PUBLICATION off", persisted)
	}
	fmt.Printf("     %s holds the one operator rule (PUBLICATION off) and no defaults\n", policyFile)
	return nil
}

func operatorRuleWins(ctx context.Context, c client, k kubo, pub published) error {
	// The boot reconcile runs in the background; give it the time f gives it.
	time.Sleep(5 * time.Second)
	rules, err := getPolicy(ctx, c)
	if err != nil {
		return err
	}
	if rule, _ := findRule(rules, pinpolicy.Publication, ""); rule.Enabled || rule.PinnedRoots != 0 {
		return fmt.Errorf("PUBLICATION after restart: enabled=%t roots=%d, want the operator's off", rule.Enabled, rule.PinnedRoots)
	}
	if rule, _ := findRule(rules, pinpolicy.FlowAsset, ""); !rule.Enabled {
		return errors.New("FLOW_ASSET is off after restart: one operator rule froze another class")
	}
	if n, err := lanePinned(ctx, k, pub); err != nil || n != 0 {
		return fmt.Errorf("after restart %d of the lane's CIDs are pinned (%v)", n, err)
	}
	fmt.Println("     PUBLICATION still off, FLOW_ASSET still on; the lane stays unpinned")
	return nil
}

func bootReconcile(ctx context.Context, c client, k kubo, pub published) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		n, err := lanePinned(ctx, k, pub)
		if err != nil {
			return err
		}
		if n == len(pub.lane()) {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d of the lane's %d CIDs pinned 60 s after boot", n, len(pub.lane()))
		}
		time.Sleep(time.Second)
	}
	rules, err := getPolicy(ctx, c)
	if err != nil {
		return err
	}
	rule, _ := findRule(rules, pinpolicy.Publication, "")
	if !rule.Enabled || rule.PinnedRoots < 3 || rule.PinnedBytes == 0 {
		return fmt.Errorf("PUBLICATION after boot: enabled=%t roots=%d bytes=%d, want on, >= 3 roots, > 0 bytes", rule.Enabled, rule.PinnedRoots, rule.PinnedBytes)
	}
	fmt.Printf("     no operator rule: PUBLICATION on by default, the boot pinned the lane's manifest, shard and index (%d root(s) %d bytes)\n", rule.PinnedRoots, rule.PinnedBytes)
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
	rules, err := getPolicy(ctx, c)
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

// node is one node home and config, booted as often as the harness needs on
// the same store, Kubo repo and identity.
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
// and the loopback console admitted as the operator (a plain loopback call
// is the operator, a call carrying a proxy header is anonymous).
func prepare(binary, root string) (*node, error) {
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
