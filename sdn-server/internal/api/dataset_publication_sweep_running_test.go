package api

import (
	"context"
	"crypto/ed25519"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// pausingKubo is the dataset publication test Kubo behind a proxy that can
// hold the next add (the pin step that follows an export) until released,
// or fail it.
type pausingKubo struct {
	*httptest.Server
	hold     atomic.Bool
	fail     atomic.Bool
	held     chan struct{}
	released chan struct{}
}

func newPausingKubo(t *testing.T) *pausingKubo {
	t.Helper()
	inner := newDatasetPublicationKuboTestServer(t, map[string][]byte{})
	t.Cleanup(inner.Close)
	target, _ := url.Parse(inner.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	k := &pausingKubo{held: make(chan struct{}), released: make(chan struct{})}
	k.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/add" {
			if k.fail.CompareAndSwap(true, false) {
				http.Error(w, "kubo is restarting", http.StatusInternalServerError)
				return
			}
			if k.hold.CompareAndSwap(true, false) {
				close(k.held)
				<-k.released
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(k.Server.Close)
	return k
}

// publicationClock is a service clock the test moves forward: the sweep
// then sees the files as written that long ago.
type publicationClock struct{ ahead atomic.Int64 }

func (c *publicationClock) now() time.Time {
	return time.Now().UTC().Add(time.Duration(c.ahead.Load()))
}

func newSweepTestService(t *testing.T, store *storage.FlatSQLStore, kubo string, outputDir string, clock *publicationClock) *ConcreteDatasetPublicationService {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	service := NewConcreteDatasetPublicationService(store, &fakeDatasetUpdatePublisher{}, key, "synthetic-node", "bafy-provider-epm", kubo, outputDir)
	pins, err := pinpolicy.New(kubo, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetPinPolicy(pins)
	service.now = clock.now
	return service
}

// publicationFiles lists the shard and index files under dir.
func publicationFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if !entry.IsDir() && (strings.HasSuffix(name, ".fbshard") || strings.HasSuffix(name, ".index.json") || strings.HasSuffix(name, ".index.fb")) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

// withoutFiles is files less those in drop.
func withoutFiles(files, drop []string) []string {
	dropped := map[string]bool{}
	for _, path := range drop {
		dropped[path] = true
	}
	var kept []string
	for _, path := range files {
		if !dropped[path] {
			kept = append(kept, path)
		}
	}
	return kept
}

func requireFiles(t *testing.T, what string, files []string) {
	t.Helper()
	if len(files) == 0 {
		t.Fatalf("%s: no files to check", what)
	}
	for _, path := range files {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %s was removed (%v)", what, path, err)
		}
	}
}

// publishWhileSwept starts publish, holds it at its first pin (its export's
// files are written and no row names them), sweeps schema an hour later on
// the service's clock, and returns the files present while it was held and
// the publication's error.
func publishWhileSwept(t *testing.T, service *ConcreteDatasetPublicationService, kubo *pausingKubo, clock *publicationClock, schema string, publish func() error) ([]string, error) {
	t.Helper()
	kubo.hold.Store(true)
	errs := make(chan error, 1)
	go func() { errs <- publish() }()
	select {
	case <-kubo.held:
	case err := <-errs:
		t.Fatalf("the publication ended before its pin step: %v", err)
	case <-time.After(2 * time.Minute):
		t.Fatal("the publication did not reach its pin step")
	}
	files := publicationFiles(t, service.outputDir)
	clock.ahead.Store(int64(time.Hour))
	service.sweepPublicationFiles(schema, nil, true)
	close(kubo.released)
	return files, <-errs
}

// The publication-file sweep keeps the files of every running publication:
// a full-catalog CAT publication writing its OCM products while an OCM
// sweep runs, and a retry that reuses the identical files a failed
// publication left. A sweep, at start or after a publication, keeps a
// registered datastore's publication files.
func TestPublicationSweepKeepsFilesOfRunningPublications(t *testing.T) {
	if testing.Short() {
		t.Skip("real format-4 stores and catalog fixtures")
	}
	t.Run("full-catalog run under another schema", func(t *testing.T) {
		root := t.TempDir()
		store := pairedStore(t, filepath.Join(root, "producer"))
		defer store.Close()
		catalogEnableRetention(t, store)
		kubo := newPausingKubo(t)
		clock := &publicationClock{}
		service := newSweepTestService(t, store, kubo.URL, filepath.Join(root, "exports"), clock)
		catalogPutDay(t, store, 0, "OCM", "CAT")
		catalogDrain(t, NewAutoPublisher(service, catalogRules()))
		// What the batch publications left unreferenced goes first.
		clock.ahead.Store(int64(time.Hour))
		service.sweepPublicationFiles("OCM.fbs", nil, true)
		service.sweepPublicationFiles("CAT.fbs", nil, true)
		clock.ahead.Store(0)
		catalogPutDay(t, store, 1, "OCM", "CAT")
		files, err := publishWhileSwept(t, service, kubo, clock, "OCM.fbs", func() error {
			_, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", FullCatalog: true})
			return err
		})
		requireFiles(t, "OCM sweep during a full-catalog CAT publication", files)
		if err != nil {
			t.Fatalf("the full-catalog publication failed: %v", err)
		}
	})

	retry := func(t *testing.T, refreshFails bool) {
		root := t.TempDir()
		store := pairedStore(t, filepath.Join(root, "producer"))
		defer store.Close()
		for i := uint32(1); i <= 5; i++ {
			pairedCommit(t, store, "retry", i)
		}
		kubo := newPausingKubo(t)
		clock := &publicationClock{}
		service := newSweepTestService(t, store, kubo.URL, filepath.Join(root, "exports"), clock)
		// A completed publication of the schema: the sweep has rows to go by.
		for i := uint32(100); i <= 102; i++ {
			pairedCommit(t, store, "seed", i)
		}
		if _, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", BatchID: "seed"}); err != nil {
			t.Fatal(err)
		}
		seeded := publicationFiles(t, service.outputDir)
		req := DatasetPublicationRequest{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", BatchID: "retry"}
		kubo.fail.Store(true)
		if _, err := service.PublishDatasetUpdate(context.Background(), req); err == nil {
			t.Fatal("the first publication did not fail at its pin step")
		}
		left := withoutFiles(publicationFiles(t, service.outputDir), seeded)
		if len(left) == 0 {
			t.Fatal("the failed publication left no files")
		}
		old := time.Now().Add(-2 * time.Hour)
		for _, path := range left {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
		if refreshFails {
			// The reused files' times cannot be refreshed (a file the node
			// may write but does not own, on a system that refuses it).
			defer storage.SetReusedExportFileRefresh(func(string) error { return syscall.EPERM })()
		}
		files, err := publishWhileSwept(t, service, kubo, clock, "CAT.fbs", func() error {
			_, err := service.PublishDatasetUpdate(context.Background(), req)
			return err
		})
		files = withoutFiles(files, seeded)
		if strings.Join(files, "\n") != strings.Join(left, "\n") {
			t.Fatalf("the retry wrote %v, the failed publication left %v: the retry did not reuse them", files, left)
		}
		requireFiles(t, "sweep during a retry", files)
		if err != nil {
			t.Fatalf("the retry failed: %v", err)
		}
	}
	t.Run("retry reusing identical files", func(t *testing.T) { retry(t, false) })
	t.Run("retry reusing identical files whose time cannot be refreshed", func(t *testing.T) { retry(t, true) })

	t.Run("datastore publication", func(t *testing.T) {
		validator, err := sds.NewValidator(nil)
		if err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		storeBase := filepath.Join(root, "store")
		rootStore, err := storage.NewFlatSQLStore(storeBase, validator)
		if err != nil {
			t.Fatal(err)
		}
		defer rootStore.Close()
		identity := storage.DatastoreIdentity{
			SchemaName: "CAT.fbs", SourcePeerID: "source:legacy-sqlite", ProviderID: "space-data-network-02",
			SourceName: "provider-cat-historical", BatchHead: "historical-head", QueryProfile: storage.DatasetPublicationQueryProfile,
			SnapshotID: "historical-head", HighWaterMark: "historical-head", ArtifactHash: "historical-head",
		}
		datastoreKey, err := identity.Key()
		if err != nil {
			t.Fatal(err)
		}
		namespace, err := storage.NewFlatSQLStoreForIdentity(storeBase, validator, identity)
		if err != nil {
			t.Fatal(err)
		}
		records := [][]byte{
			sds.NewCATBuilder().WithNoradCatID(25544).WithObjectName("ISS (ZARYA)").WithObjectID("1998-067A").Build(),
			sds.NewCATBuilder().WithNoradCatID(1).WithObjectName("SPUTNIK 1").WithObjectID("1957-001A").Build(),
		}
		if _, err := namespace.StoreBatch("CAT.fbs", records, "source:legacy-sqlite", nil); err != nil {
			t.Fatal(err)
		}
		if err := namespace.Close(); err != nil {
			t.Fatal(err)
		}
		// A publication of the node's own store in the same schema.
		own := sds.NewCATBuilder().WithNoradCatID(5).WithObjectName("VANGUARD 1").WithObjectID("1958-002B").Build()
		if _, err := rootStore.StoreWithSourceTags("CAT.fbs", own, "fixture", nil, storage.SourceTags{ProviderID: "synthetic-node", SourceName: "own", BatchID: "b1"}); err != nil {
			t.Fatal(err)
		}
		kubo := newPausingKubo(t)
		clock := &publicationClock{}
		service := newSweepTestService(t, rootStore, kubo.URL, filepath.Join(root, "exports"), clock)
		if _, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "own", BatchID: "b1"}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{Schema: "CAT.fbs", DatastoreKey: datastoreKey, DatasetID: "provider-cat-historical", Limit: 10}); err != nil {
			t.Fatal(err)
		}
		files := publicationFiles(t, service.outputDir)
		if len(files) < 4 {
			t.Fatalf("%d publication files, want the shard and index of each publication: %v", len(files), files)
		}
		clock.ahead.Store(int64(2 * time.Hour))
		service.SweepPublicationFiles()
		requireFiles(t, "sweep at start", files)
		service.sweepPublicationFiles("CAT.fbs", nil, true)
		requireFiles(t, "sweep after a publication", files)
	})
}
