package storage

import (
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// THE BOOT PATH HAS A BUDGET AND NOTHING WAS MEASURING IT.
//
// The engine abandons any single call that outlives its uninterruptible
// wall-clock budget (flatsqlrt.DefaultEngineExecTimeout, 5 minutes) and
// poisons the instance: from that instant the node answers "engine is poisoned
// and awaiting replacement" to everything, until a human intervenes. The fleet
// installs the newest signed binary AUTOMATICALLY, so a boot phase that grows
// past that budget is a whole-fleet outage waiting on a store to get big
// enough.
//
// Every boot phase is therefore timed and stamped here, and each one is
// compared against the SAME budget the engine enforces. Two log lines fall out
// that did not exist before:
//
//   - an INFO per phase with its duration and its share of the budget, so the
//     margin is a number in the log rather than an unknown;
//   - a WARN the moment a phase crosses bootPhaseWarnFraction of the budget,
//     which is the last chance to see it coming before it becomes a poison.
//
// The phase label is also pushed into the runtime (flatsqlrt.Runtime.SetPhase)
// so the in-flight statement watchdog and the poison line both name the phase.
type bootPhaseBudget struct {
	engine *flatsqlrt.Runtime
	budget time.Duration
	total  time.Duration
}

// hostIOStats snapshots the engine's file layer. A phase that is slow because
// it is re-reading the same database pages through the wasm host-IO shim looks
// completely different from one that is slow because it is computing, and
// until these numbers were logged the two were indistinguishable.
func (b *bootPhaseBudget) hostIO() flatsqlrt.HostIOStats {
	if b == nil || b.engine == nil {
		return flatsqlrt.HostIOStats{}
	}
	return b.engine.FileIO().Stats()
}

// bootPhaseWarnFraction is the share of the per-call budget at which a phase
// stops being "slow" and starts being "about to poison the node".
const bootPhaseWarnFraction = 0.5

func newBootPhaseBudget(engine *flatsqlrt.Runtime) *bootPhaseBudget {
	budget := flatsqlrt.DefaultEngineExecTimeout
	if engine != nil {
		if b := engine.ExecBudget(); b > 0 {
			budget = b
		}
	}
	return &bootPhaseBudget{engine: engine, budget: budget}
}

// phase stamps the runtime with name and returns the func that closes it out.
// Always deferred, so a failure path reports the phase it failed in.
func (b *bootPhaseBudget) phase(name string) func() {
	if b == nil {
		return func() {}
	}
	b.engine.SetPhase(name)
	started := time.Now()
	ioBefore := b.hostIO()
	return func() {
		elapsed := time.Since(started)
		b.total += elapsed
		b.engine.SetPhase("")
		ioAfter := b.hostIO()
		reads := ioAfter.Reads - ioBefore.Reads
		bytesRead := ioAfter.BytesRead - ioBefore.BytesRead
		share := float64(elapsed) / float64(b.budget)
		if share >= bootPhaseWarnFraction {
			log.Warnf("FlatSQL boot phase %q took %s — %.0f%% of the engine's %s uninterruptible per-call budget (%d host reads, %d B). A phase that crosses the budget in ONE call abandons the execution thread and poisons the engine, and this store only grows.",
				name, elapsed.Round(time.Millisecond), share*100, b.budget, reads, bytesRead)
			return
		}
		log.Infof("FlatSQL boot phase %q took %s (%.0f%% of the %s per-call budget, %d host reads, %d B)",
			name, elapsed.Round(time.Millisecond), share*100, b.budget, reads, bytesRead)
	}
}

// summary logs the whole accounted boot, so "how long does this store take to
// open" is one grep away on any box.
func (b *bootPhaseBudget) summary() {
	if b == nil {
		return
	}
	log.Infof("FlatSQL boot: accounted phases total %s (engine per-call budget %s)",
		b.total.Round(time.Millisecond), b.budget)
}

// engineExecBudget is the per-call wall-clock budget this store's engine
// enforces. Reported with any measurement that has to be read against it.
func (s *FlatSQLStore) engineExecBudget() time.Duration {
	if s == nil || s.engine == nil {
		return flatsqlrt.DefaultEngineExecTimeout
	}
	return s.engine.ExecBudget()
}
