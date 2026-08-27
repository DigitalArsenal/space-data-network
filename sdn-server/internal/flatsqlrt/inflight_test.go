package flatsqlrt

import (
	"strings"
	"testing"
	"time"
)

// A POISON THAT NAMES NOTHING IS NOT A DIAGNOSTIC. These tests fix the
// contract that made the boot investigation possible: while a statement is
// executing, the runtime can say WHICH statement and WHICH phase, and a failed
// call is attributed to that statement rather than to the bare "(batch)"
// dispatch label.

func TestInFlightStatementIsVisibleWhileItRuns(t *testing.T) {
	r := &Runtime{}
	if _, _, _, ok := r.InFlight(); ok {
		t.Fatal("an idle runtime reports a statement in flight")
	}

	r.SetPhase("boot: unified-view rebuild")
	done := r.beginInFlight("SELECT   count(*)\n  FROM sdn_record_index")

	phase, sql, elapsed, ok := r.InFlight()
	if !ok {
		t.Fatal("a dispatched statement is not reported in flight — a poison here would name nothing")
	}
	if phase != "boot: unified-view rebuild" {
		t.Fatalf("phase = %q, want the phase set before dispatch", phase)
	}
	// collapseSQL squeezes whitespace; the table name is the load-bearing part.
	if want := "SELECT count(*) FROM sdn_record_index"; sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
	if elapsed < 0 {
		t.Fatalf("elapsed = %s", elapsed)
	}

	done()
	if _, _, _, ok := r.InFlight(); ok {
		t.Fatal("a finished statement is still reported in flight — the NEXT poison would be misattributed to it")
	}
}

func TestInFlightPhaseSurvivesNestedStatements(t *testing.T) {
	r := &Runtime{}
	r.SetPhase("boot: initTables")
	outer := r.beginInFlight("CREATE INDEX idx ON t (a)")
	inner := r.beginInFlight("SELECT 1")

	_, sql, _, ok := r.InFlight()
	if !ok || sql != "SELECT 1" {
		t.Fatalf("in flight = %q (ok=%v), want the most recent statement", sql, ok)
	}
	// The inner statement finishing must not resurrect the outer one, but it
	// must also not leave a stale entry: CompareAndSwap only clears its own.
	inner()
	if _, _, _, ok := r.InFlight(); ok {
		t.Fatal("clearing the inner statement left something in flight")
	}
	outer()
	if r.Phase() != "boot: initTables" {
		t.Fatalf("phase = %q, want it to outlive individual statements", r.Phase())
	}
	r.SetPhase("")
	if r.Phase() != "" {
		t.Fatalf("phase = %q, want cleared", r.Phase())
	}
}

func TestInFlightWatchdogLogsBeforeTheBudgetFires(t *testing.T) {
	// The watchdog exists so the log names the statement BEFORE the engine
	// abandons it. Its thresholds must therefore all be under the budget the
	// engine actually enforces — a threshold at or past the budget would only
	// ever fire after the poison, which is exactly the state this replaces.
	budget := DefaultEngineExecTimeout
	if len(inFlightWatchThresholds) == 0 {
		t.Fatal("no watchdog thresholds: a long statement would run unreported")
	}
	prev := time.Duration(0)
	for i, th := range inFlightWatchThresholds {
		if th <= prev {
			t.Fatalf("threshold %d (%s) does not increase over %s", i, th, prev)
		}
		if th >= budget {
			t.Fatalf("threshold %d (%s) is not below the engine budget %s — it could only fire after the poison", i, th, budget)
		}
		prev = th
	}
}

func TestPhaseSuffixIsOmittedWhenUnset(t *testing.T) {
	if got := phaseSuffix("  "); got != "" {
		t.Fatalf("phaseSuffix(blank) = %q, want empty", got)
	}
	if got := phaseSuffix("boot: initTables"); !strings.Contains(got, "boot: initTables") {
		t.Fatalf("phaseSuffix = %q, want it to carry the phase", got)
	}
}
