package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

const publicationStreamType = "application/vnd.sdn.flatbuffers.stream"

// TestPublicationRealDaemons compares the receiving daemon's record stream
// with the producing daemon's record stream, through their HTTP APIs.
func TestPublicationRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for two isolated real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "producer", "", "", "keep-all")
	a.start(t)
	t.Run("delivery_replay_restart_and_catchup", func(t *testing.T) {
		b := newPublicationDaemon(t, exe, "receiver", a.address, a.id, "keep-all")
		b.start(t)
		// Both protocols share Kubo's host. Waiting for the connected peer and
		// its gossip mesh avoids confusing a first-message loss with import.
		time.Sleep(3 * time.Second)
		a.ingestOMM(t, "delivery", "first", 0, 2048)
		t.Log("producer batch ingest accepted 2048 synthetic records")
		want := a.scan(t, "OMM.fbs", "delivery")
		if len(want) != 2048 {
			t.Fatalf("producer holds %d records, want 2048", len(want))
		}
		pub := a.publish(t, "OMM.fbs", "delivery", "first")
		b.awaitRecords(t, "OMM.fbs", "delivery", want, 6*time.Minute)
		b.requirePublication(t, "delivery", pub)
		a.publishMode(t, "OMM.fbs", "delivery", "first", true)
		time.Sleep(3 * time.Second)
		b.requireRecords(t, "OMM.fbs", "delivery", want)
		b.stop(t)
		for i := 0; i < 3; i++ {
			batch := fmt.Sprintf("offline-%d", i)
			a.ingestOMM(t, "delivery", batch, 3000+i*16, 16)
			a.publish(t, "OMM.fbs", "delivery", batch)
		}
		want = a.scan(t, "OMM.fbs", "delivery")
		b.start(t)
		t.Log("automatic catch-up: 15-second initial delay, five-minute cadence; no sync command")
		b.awaitRecords(t, "OMM.fbs", "delivery", want, 6*time.Minute)
		a.stop(t)
		b.stop(t)
		a.start(t)
		b.start(t)
		b.requireRecords(t, "OMM.fbs", "delivery", want)
		a.ingestOMM(t, "delivery", "after-restart", 4000, 16)
		a.publish(t, "OMM.fbs", "delivery", "after-restart")
		b.awaitRecords(t, "OMM.fbs", "delivery", a.scan(t, "OMM.fbs", "delivery"), 6*time.Minute)
		t.Log("delivery, replay, automatic catch-up, and restart assertions passed")
	})
	for policyIndex, policy := range []string{"untrusted", "deny", "unlisted"} {
		t.Run("refusal_"+policy, func(t *testing.T) {
			trust := a.id
			if policy == "untrusted" {
				trust = ""
			}
			b := newPublicationDaemon(t, exe, "refusal-"+policy, a.address, trust, "keep-all")
			if policy != "untrusted" {
				b.importPolicy(t, policy)
			}
			b.start(t)
			time.Sleep(3 * time.Second)
			source := "refusal-" + policy
			a.ingestOMM(t, source, "first", 5000+policyIndex*100, 16)
			a.publish(t, "OMM.fbs", source, "first")
			// Includes the startup catch-up as well as live announcements.
			time.Sleep(20 * time.Second)
			b.requireRecords(t, "OMM.fbs", source, map[string]int{})
			if policy == "untrusted" {
				raw := b.request(t, "GET", "/api/v1/admin/peers/"+a.id, "", nil, http.StatusOK)
				var status struct {
					Trust peers.TrustLevel `json:"trust_level"`
				}
				if err := json.Unmarshal(raw, &status); err != nil {
					t.Fatal(err)
				}
				if status.Trust >= peers.Trusted {
					t.Fatal("refusal receiver trusts the producer")
				}
				t.Logf("receiver peer status: trust=%s, below required trusted", status.Trust)
				return
			}
			log := readFileString(b.logPath)
			reason := "non-trusted"
			if policy == "deny" {
				reason = "denied by peers.trusted_import.deny"
			}
			if policy == "unlisted" {
				reason = "not listed in peers.trusted_import.allow"
			}
			if !strings.Contains(log, reason) {
				t.Fatalf("refusal has no reason %q in %s", reason, b.logPath)
			}
		})
	}
}

func TestPublicationRetentionRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for retention between real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "retention-producer", "", "", "keep-all")
	a.start(t)
	for ruleIndex, rule := range []string{"keep-all", "replace-current", "archive-all"} {
		t.Run(rule, func(t *testing.T) {
			b := newPublicationDaemon(t, exe, "retention-"+rule, a.address, a.id, rule)
			b.start(t)
			time.Sleep(3 * time.Second)
			a.ingestOMM(t, rule, "first", 10000+ruleIndex*30000, 16)
			first := a.publish(t, "OMM.fbs", rule, "first")
			b.awaitRecords(t, "OMM.fbs", rule, a.scan(t, "OMM.fbs", rule), 6*time.Minute)
			a.ingestOMM(t, rule, "second", 20000+ruleIndex*30000, 16)
			second := a.publish(t, "OMM.fbs", rule, "second")
			want := a.scan(t, "OMM.fbs", rule)
			if rule == "replace-current" {
				want = a.scanBatch(t, "OMM.fbs", rule, "second")
			}
			b.awaitRecords(t, "OMM.fbs", rule, want, 6*time.Minute)
			// Retention runs after import; wait for the pin ledger to settle.
			deadline := time.Now().Add(30 * time.Second)
			for {
				pins := b.pins(t)
				good := true
				for _, p := range []publicationResult{first, second} {
					for _, cid := range []string{p.ShardCID, p.IndexCID, p.ManifestCID} {
						if pins[cid] != (rule == "archive-all") {
							good = false
						}
					}
				}
				if good {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s publication pin set does not match retention", rule)
				}
				time.Sleep(time.Second)
			}
		})
	}
}

func (d *publicationDaemon) kuboRPC(t *testing.T, op string, args url.Values) []byte {
	t.Helper()
	endpoint := d.kubo + "/api/v0/" + op + "?" + args.Encode()
	client := http.Client{Timeout: 2 * time.Minute}
	response, err := client.Post(endpoint, "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Kubo %s: HTTP %d: %.512q", op, response.StatusCode, raw)
	}
	return raw
}

func (d *publicationDaemon) pins(t *testing.T) map[string]bool {
	var reply struct {
		Keys map[string]struct{ Type string }
	}
	if err := json.Unmarshal(d.kuboRPC(t, "pin/ls", url.Values{"type": {"all"}}), &reply); err != nil {
		t.Fatal(err)
	}
	result := map[string]bool{}
	for cid, pin := range reply.Keys {
		result[cid] = pin.Type == "recursive" || pin.Type == "direct"
	}
	return result
}

type publicationResult struct {
	RecordCount int    `json:"recordCount"`
	ShardCID    string `json:"shardCid"`
	IndexCID    string `json:"indexCid"`
	ManifestCID string `json:"manifestCid"`
	PNMCID      string `json:"pnmCid"`
}

type publicationDaemon struct {
	exe, root, cfg, store, base, kubo, logPath, id, address string
	env                                                     []string
	cmd                                                     *exec.Cmd
	done                                                    chan error
	log                                                     *os.File
	mu                                                      sync.Mutex
}

func publicationBinary(t *testing.T) string {
	t.Helper()
	if binary := os.Getenv("SDN_E2E_PUBLICATION_BINARY"); binary != "" {
		return binary
	}
	root := publicationTemp(t, "build")
	exe := filepath.Join(root, "spacedatanetwork")
	module, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(module, "..", "scripts", "go-with-wasmedge.sh"), "build", "-o", exe, "./cmd/spacedatanetwork")
	cmd.Dir = module
	logPath := filepath.Join(root, "build.log")
	out, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = out
	cmd.Stderr = out
	err = cmd.Run()
	out.Close()
	if err != nil {
		t.Fatalf("build daemon: %v: %s", err, tailString(readFileString(logPath), 20))
	}
	return exe
}

func publicationTemp(t *testing.T, name string) string {
	t.Helper()
	parent := os.Getenv("SDN_E2E_PUBLICATION_STORES")
	if parent == "" {
		return t.TempDir()
	}
	root, err := os.MkdirTemp(parent, name+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root
}

func newPublicationDaemon(t *testing.T, exe, name, bootstrap, trust, retention string) *publicationDaemon {
	t.Helper()
	root := publicationTemp(t, name)
	d := &publicationDaemon{exe: exe, root: root, cfg: filepath.Join(root, "config.yaml"), store: filepath.Join(root, "store")}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	admin, kubo, gateway := freeLoopbackPort(t), freeLoopbackPort(t), freeLoopbackPort(t)
	d.base = fmt.Sprintf("http://127.0.0.1:%d", admin)
	d.kubo = fmt.Sprintf("http://127.0.0.1:%d", kubo)
	writeRealDaemonConfig(t, d.cfg, d.store, admin, kubo, gateway, 180)
	raw, err := os.ReadFile(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(raw)
	// A fixed loopback listener preserves B's bootstrap address across restarts.
	cfg = strings.Replace(cfg, "/ip4/127.0.0.1/tcp/0", fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", freeLoopbackPort(t)), 1)
	if bootstrap != "" {
		begin := strings.Index(cfg, "  bootstrap:\n") + len("  bootstrap:\n")
		end := strings.Index(cfg[begin:], "\n") + begin
		cfg = cfg[:begin] + fmt.Sprintf("    - %q", bootstrap) + cfg[end:]
	}
	peers := "peers:\n  trusted_import:\n    new_lanes: deny\n    allow:\n      - provider_id: publication-e2e\n"
	if trust != "" {
		peers += fmt.Sprintf("  trusted_peers: [%q]\n", trust)
	}
	cfg = strings.Replace(cfg, "peers:\n", peers, 1)
	cfg = strings.Replace(cfg, "  max_size: 1GB", "  max_size: 32GB", 1)
	cfg = strings.Replace(cfg, "update:\n  enabled: true", "update:\n  enabled: false", 1)
	cfg = strings.Replace(cfg, "  require_auth: true", "  require_auth: true\n  local_console: true\n  remote_requires_sealed: false", 1)
	cfg += "publishing:\n  enabled: true\nsubscriptions:\n  default_retention: " + retention + "\n"
	if err := os.WriteFile(d.cfg, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	requireIsolatedConfig(t, d.cfg)
	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		t.Fatal(err)
	}
	passwordPath := filepath.Join(root, "key-password")
	if err := os.WriteFile(passwordPath, []byte(hex.EncodeToString(password)), 0400); err != nil {
		t.Fatal(err)
	}
	d.env = append(environWithout("HOME", "SDN_CONFIG", "INVOCATION_ID", "SDN_STORE_FORMAT", config.EnvKeyPassword, config.EnvKeyPasswordFile), "HOME="+home, "SDN_STORE_FORMAT=5", config.EnvKeyPasswordFile+"="+passwordPath)
	if os.Getenv("XDG_CACHE_HOME") == "" {
		if cache, err := os.UserCacheDir(); err == nil {
			d.env = append(d.env, "XDG_CACHE_HOME="+cache)
		}
	}
	logRoot := os.Getenv("SDN_E2E_PUBLICATION_LOGS")
	if logRoot == "" {
		logRoot = root
	}
	if err := os.MkdirAll(logRoot, 0700); err != nil {
		t.Fatal(err)
	}
	d.logPath = filepath.Join(logRoot, filepath.Base(root)+".log")
	t.Cleanup(func() { d.stop(t); d.requireIsolated(t) })
	return d
}

func (d *publicationDaemon) importPolicy(t *testing.T, policy string) {
	raw, err := os.ReadFile(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if policy == "deny" {
		text = strings.Replace(text, "    allow:", "    deny:", 1)
	} else {
		text = strings.Replace(text, "      - provider_id: publication-e2e", "      - provider_id: another-test-provider", 1)
	}
	if err := os.WriteFile(d.cfg, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func (d *publicationDaemon) start(t *testing.T) {
	t.Helper()
	requireIsolatedConfig(t, d.cfg)
	var err error
	d.log, err = os.OpenFile(d.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"daemon", "--allow-multi-daemon", "--config", d.cfg}
	d.cmd = exec.Command(d.exe, args...)
	d.cmd.Dir = d.root
	d.cmd.Env = d.env
	d.cmd.Stdout = d.log
	d.cmd.Stderr = d.log
	if err = d.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d.done = make(chan error, 1)
	cmd := d.cmd
	go func() { d.done <- cmd.Wait() }()
	t.Logf("started test daemon pid=%d log=%s", d.cmd.Process.Pid, d.logPath)
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case err := <-d.done:
			d.cmd = nil
			t.Fatalf("daemon exited: %v; %s", err, tailString(readFileString(d.logPath), 20))
		default:
		}
		c := http.Client{Timeout: time.Second}
		response, err := c.Get(d.base + daemonReadyPath)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(time.Second)
	}
	format, err := storage.StoreFormat(d.store)
	if err != nil || format != 5 {
		t.Fatalf("test daemon store format=%d, want 5: %v", format, err)
	}
	body := d.request(t, "GET", "/api/v1/id", "", nil, http.StatusOK)
	var id struct {
		ID        string   `json:"peer_id"`
		Addresses []string `json:"listen_addresses"`
	}
	if err := json.Unmarshal(body, &id); err != nil {
		t.Fatal(err)
	}
	if id.ID == "" {
		t.Fatal("daemon identity is empty")
	}
	d.id = id.ID
	for _, addr := range id.Addresses {
		if strings.HasPrefix(addr, "/ip4/127.0.0.1/tcp/") {
			d.address = addr + "/p2p/" + id.ID
			break
		}
	}
	if d.address == "" {
		t.Fatal("no loopback peer address")
	}
}

func (d *publicationDaemon) stop(t *testing.T) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmd == nil {
		return
	}
	// Only the exact child started here is signaled, after inspecting argv.
	command := processField(d.cmd.Process.Pid, "command")
	if !strings.Contains(command, d.exe) || !strings.Contains(command, d.cfg) {
		t.Errorf("refusing to stop pid %d with unexpected command", d.cmd.Process.Pid)
		return
	}
	_ = d.cmd.Process.Signal(os.Interrupt)
	select {
	case <-d.done:
	case <-time.After(60 * time.Second):
		_ = d.cmd.Process.Kill()
		<-d.done
	}
	d.log.Close()
	d.cmd = nil
}

func (d *publicationDaemon) requireIsolated(t *testing.T) {
	text := readFileString(d.logPath)
	cfg, _, err := config.LoadResolved(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "falling back to built-in defaults") {
		t.Errorf("bootstrap fallback in %s", d.logPath)
	}
	// Resolve the complete bootstrap list again; every permitted dial is local.
	requireIsolatedConfig(t, d.cfg)
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "Connected to bootstrap peer") {
			allowed := false
			for _, addr := range cfg.Network.Bootstrap {
				parts := strings.Split(addr, "/p2p/")
				if len(parts) == 2 && strings.Contains(line, parts[1]) {
					allowed = true
				}
			}
			if !allowed {
				t.Errorf("connection to an unconfigured peer in %s: %s", d.logPath, line)
			}
		}
	}
}

func (d *publicationDaemon) request(t *testing.T, method, path, contentType string, body []byte, want int) []byte {
	t.Helper()
	req, err := http.NewRequest(method, d.base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := http.Client{Timeout: 10 * time.Minute}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("%s %s: HTTP %d, want %d; %.1024q", method, path, res.StatusCode, want, raw)
	}
	return raw
}

func (d *publicationDaemon) ingestOMM(t *testing.T, source, batch string, offset, n int) {
	var body bytes.Buffer
	for i := 0; i < n; i++ {
		record := sds.NewOMMBuilder().WithNoradCatID(uint32(1000000 + offset + i)).WithObjectName(fmt.Sprintf("SYNTHETIC-%08d", offset+i)).WithEpoch("2026-01-01T00:00:00Z").WithEpochTimestamp(1767225600).WithMeanMotion(15.5).Build()
		body.Write(record)
	}
	d.ingest(t, "OMM.fbs", source, batch, body.Bytes(), n)
}

func (d *publicationDaemon) ingest(t *testing.T, schema, source, batch string, body []byte, n int) {
	t.Helper()
	query := url.Values{"provider_id": {"publication-e2e"}, "source_name": {source}, "batch_id": {batch}}
	raw := d.request(t, "POST", "/api/v1/data/publish/batch/"+schema+"?"+query.Encode(), publicationStreamType, body, http.StatusCreated)
	var response struct {
		Results []struct {
			CID   string `json:"cid"`
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != n {
		t.Fatalf("ingest returned %d results, want %d", len(response.Results), n)
	}
	for _, r := range response.Results {
		if r.Error != "" || r.CID == "" {
			t.Fatalf("ingest refused a record: %s", r.Error)
		}
	}
}

func (d *publicationDaemon) publish(t *testing.T, schema, source, batch string) publicationResult {
	return d.publishMode(t, schema, source, batch, false)
}

func (d *publicationDaemon) publishMode(t *testing.T, schema, source, batch string, existing bool) publicationResult {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"schema": schema, "providerId": "publication-e2e", "sourceName": source, "batchId": batch, "limit": 50000, "chunkSize": 50000, "announceExisting": existing, "fullCatalog": true})
	raw := d.request(t, "POST", "/api/v1/admin/dataset-updates/publish", "application/json", body, http.StatusAccepted)
	var result publicationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.ManifestCID == "" {
		t.Fatal("publication names no manifest")
	}
	return result
}

func (d *publicationDaemon) scan(t *testing.T, schema, source string) map[string]int {
	return d.scanBatch(t, schema, source, "")
}

func (d *publicationDaemon) scanBatch(t *testing.T, schema, source, batch string) map[string]int {
	t.Helper()
	result := map[string]int{}
	cursor := ""
	for {
		request := syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: schema, ProviderID: "publication-e2e", SourceName: source, BatchID: batch, Limit: 1000, Cursor: cursor})
		raw := d.request(t, "POST", "/api/v1/data/scan", publicationStreamType, request, http.StatusOK)
		frames, err := syncwire.SplitFrames(raw)
		if err != nil {
			t.Fatal(err)
		}
		next := ""
		for i := 0; i < len(frames); {
			q, err := syncwire.ParseQRP(frames[i])
			if err != nil {
				t.Fatal(err)
			}
			i++
			count := int(q.RECORD_COUNT())
			if i+count > len(frames) {
				t.Fatal("incomplete scan run")
			}
			for _, frame := range frames[i : i+count] {
				sum := sha256.Sum256(frame)
				result[hex.EncodeToString(sum[:])]++
			}
			i += count
			next = string(q.NEXT_CURSOR())
		}
		if next == "" {
			break
		}
		if next == cursor {
			t.Fatal("scan cursor did not advance")
		}
		cursor = next
	}
	return result
}

func (d *publicationDaemon) requireRecords(t *testing.T, schema, source string, want map[string]int) {
	t.Helper()
	got := d.scan(t, schema, source)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record digest multisets differ: got %d distinct, want %d", len(got), len(want))
	}
}

func (d *publicationDaemon) awaitRecords(t *testing.T, schema, source string, want map[string]int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if reflect.DeepEqual(d.scan(t, schema, source), want) {
			return
		}
		time.Sleep(2 * time.Second)
	}
	d.requireRecords(t, schema, source, want)
}

func (d *publicationDaemon) requirePublication(t *testing.T, source string, pub publicationResult) {
	t.Helper()
	raw := d.request(t, "GET", "/api/v1/sync/OMM.fbs/publication-e2e/"+source, "", nil, http.StatusOK)
	frames, err := syncwire.SplitFrames(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range frames {
		status := DSS.GetSizePrefixedRootAsDSS(frame, 0)
		if string(status.LAST_PUBLICATION_CID()) == pub.ManifestCID || string(status.LAST_PNM_CID()) == pub.PNMCID {
			return
		}
	}
	t.Fatal("receiver status does not name the producer publication")
}
