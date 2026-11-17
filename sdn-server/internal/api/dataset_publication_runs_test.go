package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
)

// A publication waiting for its lane gives up when its context ends, and its
// deadline starts once it holds the lane, not while it waits.
func TestPublicationLaneWaitHonorsContextAndDeadlineStartsAtTheLane(t *testing.T) {
	h := newPublicationHygiene()
	_, first, err := h.holdLane(context.Background(), time.Hour, "OMM.fbs", "p", "s")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, _, err := h.holdLane(cancelled, time.Hour, "OMM.fbs", "p", "s"); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a wait for a held lane whose context ended answered %v", err)
	}
	if waited := time.Since(started); waited > 5*time.Second {
		t.Fatalf("the wait ended %s after its context", waited)
	}

	const deadline = 200 * time.Millisecond
	held := make(chan time.Time, 1)
	go func() {
		time.Sleep(3 * deadline)
		held <- time.Now()
		first()
	}()
	ctx, release, err := h.holdLane(context.Background(), deadline, "OMM.fbs", "p", "s")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	freed := <-held
	if ctx.Err() != nil {
		t.Fatalf("the publication's deadline ran out while it waited for the lane: %v", ctx.Err())
	}
	if end, ok := ctx.Deadline(); !ok || end.Before(freed.Add(deadline-50*time.Millisecond)) {
		t.Fatalf("deadline %v, the lane was freed at %v: the deadline did not start at the lane", end, freed)
	}
}

// Running publications stop with the node: StopPublications cancels them,
// waits for them, raises no alert for them, and refuses new ones. A caller
// bound to its publications (the auto-publisher) ends them with its own
// deadline; any other caller leaving does not.
func TestPublicationRunsStopWithTheNodeAndTheirCaller(t *testing.T) {
	s := &ConcreteDatasetPublicationService{runs: newPublicationRuns(), now: time.Now, alerts: ops.NewRegistry()}
	req := DatasetPublicationRequest{Schema: "OMM.fbs", ProviderID: "p", SourceName: "s", BatchID: "b"}
	blocked := func(ctx context.Context) (*DatasetPublicationResult, error) {
		<-ctx.Done()
		return nil, atPublicationStep("export", context.Cause(ctx))
	}

	// A client that leaves does not end the publication.
	client, leave := context.WithCancel(context.Background())
	leave()
	result, err := s.runDatasetPublication(client, req, func(ctx context.Context) (*DatasetPublicationResult, error) {
		time.Sleep(100 * time.Millisecond)
		return &DatasetPublicationResult{RecordCount: 1}, ctx.Err()
	})
	if err != nil || result == nil || result.RecordCount != 1 {
		t.Fatalf("a publication whose client left answered %+v, %v", result, err)
	}

	// A bound caller's deadline ends it.
	bound, cancelBound := context.WithTimeout(withPublicationBoundToCaller(context.Background()), 100*time.Millisecond)
	defer cancelBound()
	if _, err := s.runDatasetPublication(bound, req, blocked); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a publication of a bound caller past its deadline answered %v", err)
	}
	// The caller answers at its deadline; the run it was the only caller of
	// is cancelled and ends.
	for ended := time.Now().Add(5 * time.Second); !s.runs.oldestStart(nil).IsZero(); time.Sleep(time.Millisecond) {
		if time.Now().After(ended) {
			t.Fatal("the run of a bound caller that left did not end")
		}
	}
	s.runs.active.Wait()
	s.alerts.Clear(ops.KindPublicationFailed, datasetPublicationLaneName(req))
	s.runs = newPublicationRuns()

	// The node's stop ends it.
	errs := make(chan error, 1)
	go func() {
		_, err := s.runDatasetPublication(context.Background(), req, blocked)
		errs <- err
	}()
	for s.runs.oldestStart(nil).IsZero() {
		time.Sleep(time.Millisecond)
	}
	stop, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStop()
	s.StopPublications(stop)
	if stop.Err() != nil {
		t.Fatal("StopPublications waited past its bound")
	}
	if err := <-errs; !errors.Is(err, errPublicationsStopped) {
		t.Fatalf("a publication the node stopped answered %v", err)
	}
	if active := s.alerts.Active(); len(active) != 0 {
		t.Fatalf("a publication the node stopped raised %+v", active)
	}
	if _, err := s.runDatasetPublication(context.Background(), req, blocked); !errors.Is(err, errPublicationsStopped) {
		t.Fatalf("a publication requested after the stop answered %v", err)
	}
}

// A bound caller (the auto-publisher) that joins a run another caller
// started stops waiting at its own deadline, and the run continues for the
// caller that started it. When the starter is the bound one and its deadline
// passes while an unbound caller waits, the run continues for that caller.
// When the last caller waiting is bound and leaves, the run is cancelled.
func TestPublicationJoinedRunsHonorEachCallersBound(t *testing.T) {
	s := &ConcreteDatasetPublicationService{runs: newPublicationRuns(), now: time.Now, alerts: ops.NewRegistry()}
	req := DatasetPublicationRequest{Schema: "OMM.fbs", ProviderID: "p", SourceName: "s", BatchID: "b"}
	type answer struct {
		result *DatasetPublicationResult
		err    error
	}
	release := make(chan struct{})
	cancelled := make(chan error, 1)
	slow := func(ctx context.Context) (*DatasetPublicationResult, error) {
		select {
		case <-release:
			return &DatasetPublicationResult{RecordCount: 7}, nil
		case <-ctx.Done():
			cancelled <- context.Cause(ctx)
			return nil, context.Cause(ctx)
		}
	}
	// finish lets the running publication complete; it fails the test when
	// the run already ended (a caller cancelled it).
	finish := func() {
		t.Helper()
		select {
		case release <- struct{}{}:
		case err := <-cancelled:
			t.Fatalf("the run was cancelled while another caller waited for it: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("the running publication did not take its release")
		}
	}
	waitRunning := func() {
		for s.runs.oldestStart(nil).IsZero() {
			time.Sleep(time.Millisecond)
		}
	}
	start := func(ctx context.Context) chan answer {
		out := make(chan answer, 1)
		go func() {
			result, err := s.runDatasetPublication(ctx, req, slow)
			out <- answer{result, err}
		}()
		return out
	}
	bound := func(d time.Duration) (context.Context, context.CancelFunc) {
		return context.WithTimeout(withPublicationBoundToCaller(context.Background()), d)
	}

	// An unbound starter; a bound joiner leaves at its deadline.
	starter := start(context.Background())
	waitRunning()
	joinerCtx, cancelJoiner := bound(100 * time.Millisecond)
	defer cancelJoiner()
	began := time.Now()
	if _, err := s.runDatasetPublication(joinerCtx, req, slow); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a bound joiner past its deadline answered %v", err)
	}
	if waited := time.Since(began); waited > 5*time.Second {
		t.Fatalf("a bound joiner waited %s past its 100 ms deadline", waited)
	}
	select {
	case err := <-cancelled:
		t.Fatalf("the joiner's deadline cancelled the starter's run: %v", err)
	default:
	}
	finish()
	if got := <-starter; got.err != nil || got.result == nil || got.result.RecordCount != 7 {
		t.Fatalf("the starter answered %+v, %v", got.result, got.err)
	}

	// A bound starter's deadline passes while an unbound joiner waits.
	starterCtx, cancelStarter := bound(100 * time.Millisecond)
	defer cancelStarter()
	boundStarter := start(starterCtx)
	waitRunning()
	joiner := start(context.Background())
	if got := <-boundStarter; !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("a bound starter past its deadline answered %+v, %v", got.result, got.err)
	}
	select {
	case err := <-cancelled:
		t.Fatalf("the starter's deadline cancelled a run another caller waits for: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	finish()
	if got := <-joiner; got.err != nil || got.result == nil || got.result.RecordCount != 7 {
		t.Fatalf("the joiner answered %+v, %v", got.result, got.err)
	}

	// Only bound callers: the run is cancelled when the last one leaves.
	firstCtx, cancelFirst := bound(100 * time.Millisecond)
	defer cancelFirst()
	first := start(firstCtx)
	waitRunning()
	lastCtx, cancelLast := bound(400 * time.Millisecond)
	defer cancelLast()
	last := start(lastCtx)
	<-first
	select {
	case err := <-cancelled:
		t.Fatalf("the run was cancelled while a bound caller still waited: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	<-last
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the run was cancelled by %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run went on after every caller waiting for it left")
	}
}

// A publication that completed is reported as completed, also when its
// context ended after it finished its work (here: the node's stop).
func TestPublicationCompletedAfterItsContextEndedIsASuccess(t *testing.T) {
	s := &ConcreteDatasetPublicationService{runs: newPublicationRuns(), now: time.Now, alerts: ops.NewRegistry()}
	req := DatasetPublicationRequest{Schema: "OMM.fbs", ProviderID: "p", SourceName: "s", BatchID: "b"}
	out := make(chan error, 1)
	var result *DatasetPublicationResult
	go func() {
		var err error
		result, err = s.runDatasetPublication(context.Background(), req, func(ctx context.Context) (*DatasetPublicationResult, error) {
			<-ctx.Done()
			return &DatasetPublicationResult{RecordCount: 3, ManifestCID: "bafy-done"}, nil
		})
		out <- err
	}()
	for s.runs.oldestStart(nil).IsZero() {
		time.Sleep(time.Millisecond)
	}
	stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.StopPublications(stop)
	if err := <-out; err != nil || result == nil || result.ManifestCID != "bafy-done" {
		t.Fatalf("a completed publication answered %+v, %v", result, err)
	}
	if active := s.alerts.Active(); len(active) != 0 {
		t.Fatalf("a completed publication raised %+v", active)
	}
}
