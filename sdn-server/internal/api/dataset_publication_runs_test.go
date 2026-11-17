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
