package api

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IQC"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// dataset_publication_kubo_live_test.go — the retention and IPNS pointer
// acceptance against a REAL Kubo (graph: sdn-publication-hygiene-20260928).
// It needs an `ipfs` binary on PATH and is skipped without one (and under
// -short). The daemon runs offline on a throwaway repository whose
// Datastore.StorageMax is the cap under test; Kubo is driven only through its
// RPC API, exactly as the node drives it.

const liveKuboStorageMax = 8_000_000

type liveKubo struct {
	api  string
	repo string
}

func startLiveKubo(t *testing.T) *liveKubo {
	t.Helper()
	if testing.Short() {
		t.Skip("live Kubo test skipped under -short")
	}
	binary, err := exec.LookPath("ipfs")
	if err != nil {
		t.Skip("no ipfs binary on PATH")
	}
	repo := filepath.Join(t.TempDir(), "kubo")
	env := append(os.Environ(), "IPFS_PATH="+repo)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ipfs %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--profile=test", "--empty-repo")
	run("config", "Datastore.StorageMax", fmt.Sprintf("%dB", liveKuboStorageMax))
	daemon := exec.Command(binary, "daemon", "--offline")
	daemon.Env = env
	stdout, err := daemon.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	daemon.Stderr = daemon.Stdout
	if err := daemon.Start(); err != nil {
		t.Fatalf("start ipfs daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = daemon.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = daemon.Process.Kill()
			<-done
		}
	})
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		signalled := false
		for scanner.Scan() {
			if !signalled && strings.Contains(scanner.Text(), "Daemon is ready") {
				close(ready)
				signalled = true
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(60 * time.Second):
		t.Fatal("ipfs daemon did not become ready")
	}
	raw, err := os.ReadFile(filepath.Join(repo, "api"))
	if err != nil {
		t.Fatalf("read api address: %v", err)
	}
	// /ip4/127.0.0.1/tcp/<port>
	parts := strings.Split(strings.TrimSpace(string(raw)), "/")
	if len(parts) < 5 {
		t.Fatalf("unexpected api address %q", raw)
	}
	return &liveKubo{api: "http://" + parts[2] + ":" + parts[4], repo: repo}
}

func (k *liveKubo) rpc(t *testing.T, command string, args url.Values) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, k.api+"/api/v0/"+command+"?"+args.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kubo %s: %v", command, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("kubo %s: %d %s", command, resp.StatusCode, body)
	}
	return body
}

func (k *liveKubo) recursivePins(t *testing.T) map[string]bool {
	t.Helper()
	var listed struct {
		Keys map[string]struct{ Type string }
	}
	if err := json.Unmarshal(k.rpc(t, "pin/ls", url.Values{"type": {"recursive"}}), &listed); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for value := range listed.Keys {
		out[value] = true
	}
	return out
}

func (k *liveKubo) repoSize(t *testing.T) int64 {
	t.Helper()
	// repo/gc streams one object per removed block.
	_ = k.rpc(t, "repo/gc", url.Values{})
	var stat struct {
		RepoSize   int64
		StorageMax int64
	}
	if err := json.Unmarshal(k.rpc(t, "repo/stat", url.Values{"size-only": {"true"}}), &stat); err != nil {
		t.Fatal(err)
	}
	if stat.StorageMax != liveKuboStorageMax {
		t.Fatalf("repo StorageMax = %d, want %d", stat.StorageMax, liveKuboStorageMax)
	}
	return stat.RepoSize
}

// liveIQC is a ~4 KB $IQC record.
func liveIQC(seq int, stamp string) []byte {
	b := flatbuffers.NewBuilder(5000)
	id := b.CreateString(fmt.Sprintf("iqengine:local/local/capture-%06d", seq))
	capture := b.CreateString(fmt.Sprintf("capture-%06d", seq))
	source := b.CreateString(hygieneSource)
	desc := b.CreateString(strings.Repeat(fmt.Sprintf("SigMF capture %06d, ", seq), 180))
	retrieved := b.CreateString(stamp)
	IQC.IQCStart(b)
	IQC.IQCAddID(b, id)
	IQC.IQCAddCAPTURE_ID(b, capture)
	IQC.IQCAddSOURCE_NAME(b, source)
	IQC.IQCAddRETRIEVED_AT(b, retrieved)
	IQC.IQCAddDESCRIPTION(b, desc)
	IQC.IQCAddCREATED_AT(b, retrieved)
	IQC.IQCAddUPDATED_AT(b, retrieved)
	root := IQC.IQCEnd(b)
	b.FinishWithFileIdentifier(root, []byte(IQC.IQCIdentifier))
	return append([]byte(nil), b.FinishedBytes()...)
}

func TestLiveKuboRetentionHoldsStorageMaxAndIPNSPointerTracksLatestDPM(t *testing.T) {
	kubo := startLiveKubo(t)
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, signingKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	service := NewConcreteDatasetPublicationService(store, &fakeDatasetUpdatePublisher{}, signingKey, hygienePeerID, "bafy-provider-epm", kubo.api, filepath.Join(dir, "dataset-publications"))
	service.SetPublicationPolicy(config.PublicationRetentionConfig{KeepSeries: 2},
		[]config.IPNSPointerConfig{{Schema: "IQC.fbs", SourceName: hygieneSource, Key: "self", Lifetime: time.Hour}})

	// What a fresh repository already pins (the empty directory) is Kubo's.
	baseline := kubo.recursivePins(t)
	const batch = "60cc968008101521f0f062a74bf83f9c7e72fcc3bcd8bc3ed84b97d12f4a29e3"
	var published int64
	var previous map[string]bool
	for round := 0; round < 8; round++ {
		// The pre-fix host-02 shape: the same batch grows every run.
		var records [][]byte
		for seq := 0; seq < 125+25*round; seq++ {
			records = append(records, liveIQC(seq, fmt.Sprintf("2026-09-%02dT00:00:00Z", 10+round)))
		}
		if _, err := store.StoreBatchWithSourceTags("IQC.fbs", records, "module:sigmf", nil,
			storage.SourceTags{ProviderID: hygieneProvider, SourceName: hygieneSource, BatchID: batch, ContentKeyID: "public"}); err != nil {
			t.Fatal(err)
		}
		result, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
			Schema: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource, BatchID: batch, MaxShardBytes: 512 << 10,
		})
		if err != nil {
			t.Fatalf("round %d publish: %v", round, err)
		}

		// The pointer names this round's DPM.
		service.hygiene.ipns.wait()
		status, ok := service.hygiene.ipns.lastStatus("self")
		if !ok || status.Err != nil {
			t.Fatalf("round %d: IPNS publish %+v", round, status)
		}
		var resolved struct{ Path string }
		if err := json.Unmarshal(kubo.rpc(t, "name/resolve", url.Values{"arg": {"/ipns/" + status.Name}, "nocache": {"true"}}), &resolved); err != nil {
			t.Fatal(err)
		}
		if want := "/ipfs/" + result.ManifestCID; resolved.Path != want {
			t.Fatalf("round %d: /ipns/%s resolves to %s, want %s", round, status.Name, resolved.Path, want)
		}

		// Kubo pins exactly the newest two series.
		series, err := store.ListDatasetPublicationSeries(storage.DatasetPublicationLane{SchemaName: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource})
		if err != nil || len(series) == 0 {
			t.Fatalf("round %d series: %v", round, err)
		}
		// Bundles follow the current head: a newer series of the same scope
		// unpins the older bundle at once. The previous series keeps its
		// shards, indexes and DPMs.
		current := map[string]bool{}
		carried := map[string]bool{}
		for _, member := range series[0].Members {
			current[member.CID] = true
			if member.Role != storage.PinLedgerRoleShardGroupCAR {
				carried[member.CID] = true
			}
		}
		want := map[string]bool{}
		for value := range current {
			want[value] = true
		}
		for value := range previous {
			want[value] = true
		}
		pins := kubo.recursivePins(t)
		for value := range want {
			if !pins[value] {
				t.Fatalf("round %d: kept CID %s is not pinned", round, value)
			}
		}
		for value := range pins {
			if !want[value] && !baseline[value] {
				t.Fatalf("round %d: %s is pinned but belongs to no kept series", round, value)
			}
		}
		previous = carried
	}
	// Everything the lane ever pinned, from the pin ledger (each CID once).
	entries, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{SchemaName: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource})
	if err != nil {
		t.Fatal(err)
	}
	counted := map[string]bool{}
	for _, entry := range entries {
		if !counted[entry.CID] {
			counted[entry.CID] = true
			published += entry.ByteCount
		}
	}
	size := kubo.repoSize(t)
	if published <= liveKuboStorageMax {
		t.Fatalf("fixture error: %d bytes published in all, which never tests the %d-byte cap", published, liveKuboStorageMax)
	}
	if size > liveKuboStorageMax {
		t.Fatalf("after GC kubo holds %d bytes, over its %d-byte StorageMax", size, liveKuboStorageMax)
	}
	t.Logf("8 series pinned %d bytes in all; kubo holds %d bytes after GC (StorageMax %d)", published, size, liveKuboStorageMax)
}
