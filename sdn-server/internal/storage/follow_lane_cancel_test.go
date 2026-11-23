package storage

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestFollowLaneBatchListCancelFreesEngineLanes: a follower's read of a
// batch's list on the real engine (format 5) stops when its context ends,
// and the engine's lane is free afterwards. The lane holds one small batch
// stored first and 1,000,000 records after it, each tagged to 8 batches, so
// one page of the small batch's list walks 8 million tag rows of the lane to
// find that no more follow (as a keep-all lane of millions of records does
// on a production store: 8.86 million rows a page there).
//
// The engine serves reads on 4 interactive lanes. A control first holds all
// of them with four such list reads and times a small read asked meanwhile:
// it waits for a lane. Then four list reads are started and their contexts
// end a quarter of the way through: they return within 500 ms (a request
// the engine does not end is abandoned by its caller after 1 s), and a small
// read asked right after is served promptly, so the engine stopped the walks
// instead of finishing them.
func TestFollowLaneBatchListCancelFreesEngineLanes(t *testing.T) {
	onFormat4Engine(t, followLaneBatchListCancel)
}

func followLaneBatchListCancel(t *testing.T) {
	const n, batches = 1_000_000, 8
	store := openFormat4ForTest(t, t.TempDir())
	t.Cleanup(func() { store.Close() })
	const provider, source = "prov", "keep-all"
	small := SourceTags{ProviderID: provider, SourceName: source, BatchID: "small"}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", partitionRecords(t, 10, 10), "peer", nil, small); err != nil {
		t.Fatal(err)
	}
	large := SourceTags{ProviderID: provider, SourceName: source, BatchID: "large"}
	began := time.Now()
	const chunk = 20_000
	for start := 0; start < n; start += chunk {
		records := make([][]byte, 0, chunk)
		for i := start; i < min(start+chunk, n); i++ {
			records = append(records, buildEngineOMM(t, uint32(1_000_000+i), "L", 1_700_000_000+int64(i)))
		}
		if _, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "peer", nil, large); err != nil {
			t.Fatalf("store records %d to %d: %v", start, start+len(records), err)
		}
		for b := 1; b < batches; b++ {
			again := large
			again.BatchID = "large-" + strconv.Itoa(b)
			if _, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "peer", nil, again); err != nil {
				t.Fatalf("tag records %d to %d to %s: %v", start, start+len(records), again.BatchID, err)
			}
		}
	}
	t.Logf("stored %d records (%d tag rows) in %s", n+10, n*batches+10, time.Since(began).Round(time.Millisecond))

	list := func(ctx context.Context) (time.Duration, error) {
		began := time.Now()
		cids, _, more, err := store.LaneBatchCIDPage(ctx, "OMM.fbs", provider, source, "small", 0, 8192)
		if err == nil && (len(cids) != 10 || more) {
			err = errors.New("the small batch's list is not its 10 records in one page")
		}
		return time.Since(began), err
	}
	smallRead := func() (time.Duration, error) {
		began := time.Now()
		cids, _, _, err := store.LaneBatchCIDPage(context.Background(), "OMM.fbs", provider, source, "large", 0, 10)
		if err == nil && len(cids) != 10 {
			err = errors.New("the large batch's first page is not 10 records")
		}
		return time.Since(began), err
	}
	walk, err := list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("one page of the small batch's list (the whole lane walked): %s", walk.Round(time.Millisecond))
	if walk < 2*time.Second {
		t.Fatalf("a walk of the lane took %s; the test needs one of 2 s or more to tell a stopped walk from a finished one", walk)
	}

	const lanes = 4
	// Control: four walks hold the interactive lanes; a small read waits.
	var wg sync.WaitGroup
	for i := 0; i < lanes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := list(context.Background()); err != nil {
				t.Errorf("control walk: %v", err)
			}
		}()
	}
	time.Sleep(walk / 4)
	waited, err := smallRead()
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("control: a small read asked while %d walks run took %s", lanes, waited.Round(time.Millisecond))
	if waited < walk/4 {
		t.Fatalf("control: a small read during %d walks took %s; the walks did not hold the engine's lanes, so the test proves nothing", lanes, waited)
	}

	// Four walks whose contexts end a quarter of the way through.
	ctx, cancel := context.WithCancel(context.Background())
	took := make([]time.Duration, lanes)
	errs := make([]error, lanes)
	for i := 0; i < lanes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			took[i], errs[i] = list(ctx)
		}(i)
	}
	time.Sleep(walk / 4)
	cancel()
	canceled := time.Now()
	wg.Wait()
	returned := time.Since(canceled)
	for i := range errs {
		if !errors.Is(errs[i], context.Canceled) {
			t.Fatalf("walk %d whose context ended: %v after %s, want context canceled", i, errs[i], took[i])
		}
	}
	t.Logf("%d walks whose context ended at %s returned %s after it", lanes, (walk / 4).Round(time.Millisecond), returned.Round(time.Millisecond))
	if returned > 500*time.Millisecond {
		t.Fatalf("the walks returned %s after their context ended; want within 500 ms, the engine ending them", returned)
	}
	served, err := smallRead()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("a small read asked right after: %s", served.Round(time.Millisecond))
	if served > walk/8 {
		t.Fatalf("a small read right after the canceled walks took %s (a walk takes %s): the engine went on walking", served, walk)
	}
}
