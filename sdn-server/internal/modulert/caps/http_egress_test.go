package caps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The CelesTrak fetch policy is binding owner law: requests are SERIAL with
// 2.5 s between them. It is a compiled-in floor, not a default, because a
// misconfiguration must not be able to get this node banned from a publisher
// the whole network depends on.
func TestCelesTrakFloorCannotBeLowered(t *testing.T) {
	t.Cleanup(func() { SetEgressMinIntervals(nil) })

	pacer := &egressPacer{hosts: map[string]*egressHostGate{}}
	for _, host := range []string{"celestrak.org", "celestrak.com", "www.celestrak.org", "sub.celestrak.org"} {
		if got := pacer.minInterval(host); got != CelesTrakMinRequestInterval {
			t.Fatalf("minInterval(%s) = %v, want %v with no configuration", host, got, CelesTrakMinRequestInterval)
		}
	}

	// An operator trying to go faster is ignored...
	pacer.overrides = map[string]time.Duration{"celestrak.org": 10 * time.Millisecond}
	if got := pacer.minInterval("celestrak.org"); got != CelesTrakMinRequestInterval {
		t.Fatalf("configured 10ms lowered the CelesTrak floor to %v", got)
	}
	// ...but going SLOWER is always allowed.
	pacer.overrides = map[string]time.Duration{"celestrak.org": 30 * time.Second}
	if got := pacer.minInterval("celestrak.org"); got != 30*time.Second {
		t.Fatalf("configured 30s not honoured: got %v", got)
	}

	// Hosts with no floor and no configuration are not paced at all.
	if got := pacer.minInterval("example.com"); got != 0 {
		t.Fatalf("minInterval(example.com) = %v, want 0", got)
	}
}

func TestEgressHostKeyStripsPortAndCase(t *testing.T) {
	cases := map[string]string{
		"https://CelesTrak.org/pub/satcat.csv":      "celestrak.org",
		"https://celestrak.org:443/NORAD/gp.php":    "celestrak.org",
		"http://127.0.0.1:8080/x":                   "127.0.0.1",
		"https://user:pw@www.CELESTRAK.ORG/a?b=c#d": "www.celestrak.org",
	}
	for raw, want := range cases {
		if got := egressHostKey(raw); got != want {
			t.Fatalf("egressHostKey(%q) = %q, want %q", raw, got, want)
		}
	}
	if isCelesTrakHost("notcelestrak.org") {
		t.Fatal("notcelestrak.org must not match the CelesTrak floor (suffix, not substring)")
	}
	if !isCelesTrakHost("celestrak.org") || !isCelesTrakHost("a.b.celestrak.com") {
		t.Fatal("CelesTrak host matching missed a real CelesTrak host")
	}
}

// Concurrent callers to the same host are SERIALIZED and spaced: the pacer
// holds the slot for the whole request, so two requests never overlap and the
// second starts no earlier than one interval after the first finished.
func TestEgressPacerSerializesAndSpaces(t *testing.T) {
	const interval = 120 * time.Millisecond
	pacer := &egressPacer{
		hosts:     map[string]*egressHostGate{},
		overrides: map[string]time.Duration{"example.com": interval},
	}

	var (
		mu       sync.Mutex
		inFlight int
		overlaps int
		starts   []time.Time
		wg       sync.WaitGroup
	)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, _, err := pacer.acquire(context.Background(), "example.com")
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			mu.Lock()
			inFlight++
			if inFlight > 1 {
				overlaps++
			}
			starts = append(starts, time.Now())
			mu.Unlock()

			time.Sleep(10 * time.Millisecond) // stand in for the request

			mu.Lock()
			inFlight--
			mu.Unlock()
			release()
		}()
	}
	wg.Wait()

	if overlaps != 0 {
		t.Fatalf("%d overlapping requests to one host; pacing must be SERIAL", overlaps)
	}
	if len(starts) != 3 {
		t.Fatalf("recorded %d starts, want 3", len(starts))
	}
	// starts is append-ordered under the lock, which is also acquisition order.
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < interval {
			t.Fatalf("gap between request %d and %d was %v, want >= %v", i-1, i, gap, interval)
		}
	}
}

// A cancelled/expired budget must abort the wait and free the slot rather than
// pinning a flow instance behind a jammed destination.
func TestEgressPacerReleasesOnContextCancel(t *testing.T) {
	pacer := &egressPacer{
		hosts:     map[string]*egressHostGate{},
		overrides: map[string]time.Duration{"example.com": time.Hour},
	}
	release, _, err := pacer.acquire(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	release()

	// The next acquire owes a full hour of spacing; a short budget must give up.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, _, err := pacer.acquire(ctx, "example.com"); err == nil {
		t.Fatal("acquire returned nil error despite an expired budget")
	}

	// The slot must be free: a fresh pacer state would deadlock here otherwise.
	done := make(chan struct{})
	go func() {
		ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel2()
		pacer.acquire(ctx2, "example.com")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("egress slot was never released after a cancelled wait")
	}
}

// The observer sees exactly what the connector did — and nothing about what the
// payload means.
func TestHTTPCapFetchObserverRecordsRealFetch(t *testing.T) {
	body := []byte("OBJECT_NAME,NORAD_CAT_ID\nISS (ZARYA),25544\n")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.Write(body)
	}))
	defer origin.Close()

	type observation struct {
		url    string
		status int
		bytes  int64
		errMsg string
	}
	var got []observation
	SetFetchObserver(func(url string, status int, bytes, durationMs int64, errMsg string) {
		got = append(got, observation{url, status, bytes, errMsg})
	})
	defer SetFetchObserver(nil)

	meta := httpCapCall(t, map[string]interface{}{"url": origin.URL + "/gp.php"})
	if ok, _ := meta["ok"].(bool); !ok {
		t.Fatalf("cap call failed: %v", meta)
	}
	if len(got) != 1 {
		t.Fatalf("observer saw %d fetches, want 1", len(got))
	}
	if got[0].status != http.StatusOK || got[0].bytes != int64(len(body)) || got[0].errMsg != "" {
		t.Fatalf("observation = %+v, want status 200 / %d bytes / no error", got[0], len(body))
	}

	// A failed request is booked too — an app that never successfully pulls
	// must not look like an app that never tried.
	got = nil
	httpCapCall(t, map[string]interface{}{"url": "http://127.0.0.1:1/nothing-listens-here", "timeout_ms": 500})
	if len(got) != 1 || got[0].status != 0 || got[0].errMsg == "" {
		t.Fatalf("failed fetch observation = %+v, want status 0 with an error", got)
	}
}
