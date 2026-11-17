package modulert

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// RunFaults collects what a host capability saw fail during one flow run that
// the flow itself cannot report: a flow's nodes run inside the guest, and a
// node that receives a failed answer may pass it on as an ordinary frame, so
// the run would end "ok". A fault recorded here makes the run's result an
// error.
//
// A capability records only failures that mean the run did not do its job
// (the node's own dataset publication answering anything but success), never
// an ordinary upstream refusal such as a dead mirror's 502.
type RunFaults struct {
	mu     sync.Mutex
	faults []string
}

type runFaultsKey struct{}

// WithRunFaults returns a context that carries a fresh fault collector for
// one run, and the collector.
func WithRunFaults(ctx context.Context) (context.Context, *RunFaults) {
	faults := &RunFaults{}
	return context.WithValue(ctx, runFaultsKey{}, faults), faults
}

// RecordRunFault records a fault on the run that ctx belongs to. It reports
// whether ctx carries a collector.
func RecordRunFault(ctx context.Context, fault string) bool {
	if ctx == nil {
		return false
	}
	faults, ok := ctx.Value(runFaultsKey{}).(*RunFaults)
	if !ok || faults == nil {
		return false
	}
	faults.mu.Lock()
	faults.faults = append(faults.faults, fault)
	faults.mu.Unlock()
	return true
}

// Err is nil when nothing was recorded, else every recorded fault.
func (f *RunFaults) Err() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.faults) == 0 {
		return nil
	}
	return errors.New(strings.Join(f.faults, "; "))
}
