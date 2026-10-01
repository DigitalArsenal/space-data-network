package format4proof

import (
	"fmt"
	"math/rand"
	"time"
)

// Reads: one process per (arm, class) on a fresh clone. Pass 0 is cold: the
// first call of each shape after open (the Mac cannot drop the page cache
// without root, so "cold" is first-call-after-open, as the protocol allows
// and labels). Warm passes then run every shape again in a fixed shuffled
// order. Answers are kept from the cold pass; every warm answer is checked
// against it.

// Warm passes per shape: the owner's smallest sample that still gives a p50
// and a p99 (2026-10-01 evening: one cold pass and three warm passes; the
// report says p99 rests on few samples). P4PROOF_WARM overrides it.
const (
	defaultWarm     = 3
	classBudgetWarm = 25 * time.Minute // warm passes stop past this (reported)
)

// ReadSpec is one read run.
type ReadSpec struct {
	Arm, Label, Class, Store, Out string
	Warm                          int // -1 = defaultWarm
	CallLimit                     time.Duration
	// Round > 1 is a further cold round (another fresh process on another
	// fresh clone): its samples add cold samples per shape; its answers are
	// not kept (round 1's are).
	Round int
	// Shape (EnvShape) re-runs only the shapes it names: its samples add to
	// theirs, its answers are not kept.
	Shape string
}

// hydrateClasses are the classes format 1 serves from its engine hot window
// (engine relations, the epoch stream, sandboxed SQL). Format 1 fills that
// window at boot in production; the run fills it before timing and reports
// the fill time.
var hydrateClasses = map[string]bool{"R17": true, "R18": true, "R19": true}

// RunReads measures shapes (all of spec.Class) on one arm and writes the run
// and the cold answers into spec.Out.
func RunReads(spec ReadSpec, shapes []Shape) (*Run, error) {
	runClass := spec.Class
	if spec.Round > 1 {
		runClass = fmt.Sprintf("%s-r%d", spec.Class, spec.Round)
	}
	if spec.Shape != "" {
		runClass = fmt.Sprintf("%s-rerun-%d", runClass, time.Now().Unix())
	}
	r := &Run{Kind: KindReads, Arm: spec.Arm, Format: ArmFormat(spec.Arm), Label: spec.Label, Class: runClass,
		Started: time.Now().UTC().Format(time.RFC3339), Machine: ThisMachine(), LoadStart: Load(), Extra: map[string]any{}}
	r.Mem = append(r.Mem, Snapshot("start", ""))
	s, openMs, err := OpenArm(spec.Arm, spec.Store)
	r.OpenMs = openMs
	if err != nil {
		return r, err
	}
	r.Mem = append(r.Mem, Snapshot("open", ""))
	if spec.Arm == ArmF1 && hydrateClasses[spec.Class] {
		st := time.Now()
		n, err := s.HydrateEngineHotWindow()
		r.Extra["f1_hot_window_hydrate_s"] = time.Since(st).Seconds()
		r.Extra["f1_hot_window_records"] = n
		if err != nil {
			r.Extra["f1_hot_window_error"] = err.Error()
		}
		r.Mem = append(r.Mem, Snapshot("hydrated", ""))
	}
	limit := spec.CallLimit
	if limit <= 0 {
		limit = 330 * time.Second
	}
	answers := &AnswerFile{Arm: spec.Arm, Label: spec.Label, Class: spec.Class}
	coldHash := map[string]string{}
	aborted := ""
	runCall := func(si, pass int, sh Shape, c Call) {
		var res Result
		d, ok := guard(limit, func() { res = c.Run(s) })
		ms := float64(d.Microseconds()) / 1000
		var a Answer
		var m Measure
		if ok && res != nil {
			a, m = res() // canonicalized after the timer stopped
		}
		smp := Sample{Class: spec.Class, Shape: sh.Name, Pass: pass, Ms: ms, Rows: m.Rows, Bytes: m.Bytes}
		switch {
		case !ok:
			smp.Err = fmt.Sprintf("timeout after %s", limit)
			aborted = sh.Name + " " + c.Name + ": " + smp.Err
		case a.Err != "":
			smp.Err = a.Err
		}
		r.Samples = append(r.Samples, smp)
		key := sh.Name + "\x00" + c.Name
		if pass == 0 {
			coldHash[key] = a.Hash()
			a.Call = c.Name
			answers.Shapes[si].Calls = append(answers.Shapes[si].Calls, a)
		} else if ok && a.Hash() != coldHash[key] {
			sa := &answers.Shapes[si]
			if !contains(sa.Unstable, c.Name) {
				sa.Unstable = append(sa.Unstable, c.Name)
			}
		}
	}
	for _, sh := range shapes {
		answers.Shapes = append(answers.Shapes, ShapeAnswers{Class: sh.Class, Shape: sh.Name, Schema: sh.Schema, Policy: sh.Policy})
	}
	// Pass 0: cold.
	for si, sh := range shapes {
		for _, c := range sh.Calls {
			if aborted != "" {
				break
			}
			runCall(si, 0, sh, c)
		}
	}
	// Warm passes.
	warm := defaultWarm
	if spec.Warm >= 0 {
		warm = spec.Warm
	}
	warmStart := time.Now()
	for pass := 1; pass <= warm && aborted == ""; pass++ {
		if time.Since(warmStart) > classBudgetWarm {
			r.Extra["warm_truncated_at_pass"] = pass
			break
		}
		order := rand.New(rand.NewSource(int64(pass))).Perm(len(shapes))
		for _, si := range order {
			if aborted != "" {
				continue
			}
			for _, c := range shapes[si].Calls {
				runCall(si, pass, shapes[si], c)
			}
		}
	}
	if aborted != "" {
		r.Extra["aborted"] = aborted
	}
	r.Mem = append(r.Mem, Snapshot("after "+spec.Class, spec.Store))
	if aborted == "" {
		if n, err := StoreRecords(s); err == nil {
			r.Records = n
		} else {
			r.Extra["records_error"] = err.Error()
		}
		cs := time.Now()
		if err := s.Close(); err != nil {
			r.Extra["close_error"] = err.Error()
		}
		r.Extra["close_s"] = time.Since(cs).Seconds()
	}
	r.LoadEnd = Load()
	r.Mem = append(r.Mem, Snapshot("end", ""))
	if spec.Out != "" {
		if _, err := WriteRun(spec.Out, r); err != nil {
			return r, err
		}
		if spec.Round <= 1 && spec.Shape == "" {
			if err := WriteAnswers(spec.Out, answers); err != nil {
				return r, err
			}
		}
	}
	return r, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
