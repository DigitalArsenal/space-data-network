package format4proof

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Markdown tables over a results directory: the gate report, the per-shape
// read table, ingest, bytes and M01. Every table names the loads its runs saw.

// FormatMs renders a duration in milliseconds with a unit that keeps three
// significant figures readable ("812 µs", "4.6 ms", "52 ms", "3.17 s").
func FormatMs(ms float64) string {
	switch {
	case math.IsNaN(ms):
		return "-"
	case math.IsInf(ms, 1):
		return "error"
	case ms >= 1000:
		return fmt.Sprintf("%.2f s", ms/1000)
	case ms >= 10:
		return fmt.Sprintf("%.0f ms", ms)
	case ms >= 1:
		return fmt.Sprintf("%.1f ms", ms)
	default:
		return fmt.Sprintf("%.0f µs", ms*1000)
	}
}

func formatValue(v float64, unit string) string {
	if math.IsNaN(v) {
		return "-"
	}
	if math.IsInf(v, 1) {
		return "error"
	}
	switch unit {
	case "ms":
		return FormatMs(v)
	case "x":
		return fmt.Sprintf("%.3f", v)
	case "B":
		if v >= 1<<30 {
			return fmt.Sprintf("%.2f GiB", v/(1<<30))
		}
		if v >= 1<<20 {
			return fmt.Sprintf("%.1f MiB", v/(1<<20))
		}
		return fmt.Sprintf("%.0f B", v)
	case "rec/s":
		return fmt.Sprintf("%.0f", v)
	}
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.3g", v)
}

func esc(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// GateMarkdown renders the gate report: the summary, then every gate's
// failing checks in full and its passing checks counted (all of them with
// all=true).
func GateMarkdown(r Report, all bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Format 4 gate report\n\nGenerated %s. A gate passes only when every check passes and nothing it needs is missing.\n\n", r.Generated)
	b.WriteString("Samples: by default one cold pass and three warm passes per read shape (owner, 2026-10-01), so every p99 rests on few samples. A shape within 10% of its bar is listed under its gate: re-run only that shape before calling it.\n\n")
	b.WriteString("| Gate | Status | Checks passed | Missing |\n|---|---|---|---|\n")
	for _, g := range r.Gates {
		pass, n := 0, 0
		for _, c := range g.Checks {
			if c.Info {
				continue
			}
			n++
			if c.Pass {
				pass++
			}
		}
		fmt.Fprintf(&b, "| %s %s | **%s** | %d / %d | %s |\n", g.ID, esc(g.Title), g.Status, pass, n, esc(strings.Join(g.Missing, "; ")))
	}
	for _, g := range r.Gates {
		fmt.Fprintf(&b, "\n## Gate %s: %s — %s\n\n", g.ID, g.Title, g.Status)
		if len(g.Loads) > 0 {
			fmt.Fprintf(&b, "Load averages of the runs used: %s.\n\n", strings.Join(g.Loads, ", "))
		}
		for _, m := range g.Missing {
			fmt.Fprintf(&b, "- Missing: %s\n", m)
		}
		if len(g.Missing) > 0 {
			b.WriteString("\n")
		}
		var rows []Check
		passed := 0
		for _, c := range g.Checks {
			if all || !c.Pass || c.Info {
				rows = append(rows, c)
			} else {
				passed++
			}
		}
		if len(rows) > 0 {
			b.WriteString("| Check | S | F1 | F2 | Bar | Result | Note |\n|---|---|---|---|---|---|---|\n")
			for _, c := range rows {
				res := "pass"
				switch {
				case c.Info && c.Pass:
					res = "info (meets)"
				case c.Info:
					res = "info (below)"
				case !c.Pass:
					res = "**FAIL**"
				}
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n", esc(c.Item), formatValue(c.S, c.Unit),
					formatValue(c.F1, c.Unit), formatValue(c.F2, c.Unit), formatValue(c.Bar, c.Unit), res, esc(c.Note))
			}
		}
		if !all && passed > 0 {
			fmt.Fprintf(&b, "\n%d further checks pass (the full list: the shape table, or the report with all checks).\n", passed)
		}
		writeNearBar(&b, g)
	}
	return b.String()
}

// writeNearBar lists a gate's shapes within 10% of a bar, each with the
// environment that re-runs it alone (TestProofReads).
func writeNearBar(b *strings.Builder, g Gate) {
	type key struct{ class, shape string }
	var order []key
	why := map[key][]string{}
	for _, c := range g.Checks {
		if !nearBar(c) {
			continue
		}
		k := key{c.Class, c.Shape}
		if why[k] == nil {
			order = append(order, k)
		}
		why[k] = append(why[k], fmt.Sprintf("%s %s vs %s", strings.TrimPrefix(c.Item, c.Class+" "+c.Shape+" "),
			formatValue(c.S, c.Unit), formatValue(c.Bar, c.Unit)))
	}
	if len(order) == 0 {
		return
	}
	b.WriteString("\nWithin 10% of the bar: re-run only these before calling them.\n\n")
	for _, k := range order {
		fmt.Fprintf(b, "- %s %s (%s): `%s=%s %s='^%s$'`\n", k.class, esc(k.shape), strings.Join(why[k], "; "), EnvClasses, k.class,
			EnvShape, regexp.QuoteMeta(k.shape))
	}
}

// ReadsMarkdown renders every read shape of a label: rows, cold and warm p50
// and p99 per arm, and whether S meets the bar on all four numbers.
func ReadsMarkdown(runs []*Run, label string) string {
	used := RunsOf(runs, KindReads, label)
	st := SummarizeShapes(used)
	keys := map[ShapeKey]bool{}
	for _, m := range st {
		for k := range m {
			keys[k] = true
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Read shapes, %s\n\n", label)
	loads := map[string][]string{}
	for _, r := range used {
		loads[r.Arm] = append(loads[r.Arm], r.LoadStart)
	}
	for _, arm := range Arms {
		if l := loads[arm]; len(l) > 0 {
			sort.Strings(l)
			fmt.Fprintf(&b, "- %s: %d runs, load at start %s … %s\n", arm, len(l), l[0], l[len(l)-1])
		}
	}
	b.WriteString("\nCold = first call after open in a fresh process on a fresh clone (no page-cache drop on the Mac).\n\n")
	b.WriteString("| Class | Shape | Rows (S / F1 / F2) | S cold p50 / p99 | F1 cold p50 / p99 | F2 cold p50 / p99 | S warm p50 / p99 | F1 warm p50 / p99 | F2 warm p50 / p99 | S meets bar |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	met, total := 0, 0
	for _, k := range sortedKeys(keys) {
		s, f1, f2 := st[ArmS][k], st[ArmF1][k], st[ArmF2][k]
		pair := func(x *ShapeStats, cold bool) string {
			if x == nil {
				return "-"
			}
			if cold {
				return FormatMs(x.ColdP50) + " / " + FormatMs(x.ColdP99)
			}
			return FormatMs(x.WarmP50) + " / " + FormatMs(x.WarmP99)
		}
		rows := func(x *ShapeStats) string {
			if x == nil {
				return "-"
			}
			if x.Errors > 0 {
				return fmt.Sprintf("%d (%d err)", x.Rows, x.Errors)
			}
			return fmt.Sprint(x.Rows)
		}
		verdict := "-"
		if s != nil && (f1 != nil || f2 != nil) {
			ok := true
			var misses []string
			for _, m := range readMetrics {
				bar := math.NaN()
				if f1 != nil {
					bar = m.get(f1)
				}
				if f2 != nil {
					bar = minMeasured(bar, m.get(f2))
				}
				if !lowerOrEqual(m.get(s), bar) {
					ok = false
					misses = append(misses, m.name)
				}
			}
			total++
			if ok {
				met++
				verdict = "yes"
			} else {
				verdict = "**NO** (" + strings.Join(misses, ", ") + ")"
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %s / %s / %s | %s | %s | %s | %s | %s | %s | %s |\n", k.Class, esc(k.Shape),
			rows(s), rows(f1), rows(f2), pair(s, true), pair(f1, true), pair(f2, true), pair(s, false), pair(f1, false), pair(f2, false), verdict)
	}
	fmt.Fprintf(&b, "\nS meets the bar on %d of %d shapes with a baseline.\n", met, total)
	return b.String()
}

// IngestMarkdown renders the ingest phases per arm.
func IngestMarkdown(runs []*Run) string {
	var b strings.Builder
	b.WriteString("# Ingest\n\n| Phase | Arm | Label | Records | rec/s | call p50 | call p99 | call max | errors | disk written per record | write amplification | load start / end |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, ph := range []string{PhaseOneWriter, PhaseFourWriters, PhaseSameType} {
		for _, arm := range Arms {
			for _, r := range RunsOf(runs, KindIngest, "") {
				if r.Arm != arm {
					continue
				}
				var v []float64
				errs := 0
				for _, s := range r.Samples {
					if s.Class != ph {
						continue
					}
					if s.Err != "" {
						errs++
						continue
					}
					v = append(v, s.Ms)
				}
				if len(v) == 0 && errs == 0 {
					continue
				}
				m, _ := r.Extra[ph].(map[string]any)
				num := func(k string) float64 {
					if f, ok := m[k].(float64); ok {
						return f
					}
					return math.NaN()
				}
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %d | %s | %s | %s / %s |\n", phaseTitles[ph], arm, r.Label,
					formatValue(num("records"), ""), formatValue(num("rec_per_s"), "rec/s"), FormatMs(Percentile(v, 0.5)),
					FormatMs(Percentile(v, 0.99)), FormatMs(Percentile(v, 1)), errs, formatValue(num("disk_written_per_record_b"), "B"),
					formatValue(num("write_amplification"), "x"), r.LoadStart, r.LoadEnd)
			}
		}
	}
	return b.String()
}

// BytesMarkdown renders bytes on disk per record per arm and label.
func BytesMarkdown(runs []*Run) string {
	var b strings.Builder
	b.WriteString("# Bytes on disk\n\n| Arm | Label | Store bytes (apparent) | Allocated | Files | Unique records | Copies | B per record | B per copy | B per added record |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, label := range []string{LabelFixture, LabelGrown} {
		for _, arm := range Arms {
			r := runOf(runs, KindBytes, arm, label)
			if r == nil {
				continue
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", arm, label,
				formatValue(extraFloat(r, "store_bytes"), ""), formatValue(extraFloat(r, "allocated_bytes"), ""),
				formatValue(extraFloat(r, "files"), ""), formatValue(extraFloat(r, "unique_records"), ""),
				formatValue(extraFloat(r, "copies"), ""), formatValue(extraFloat(r, "bytes_per_record"), "B"),
				formatValue(extraFloat(r, "bytes_per_copy"), "B"), formatValue(extraFloat(r, "bytes_per_added_record"), "B"))
		}
	}
	return b.String()
}

// M01Markdown renders the reads-never-wait runs: per arm, the read latency
// idle and during the writes, the WAL high water (Σ SQLite -wal files), and
// the write operations' durations.
func M01Markdown(runs []*Run) string {
	var b strings.Builder
	b.WriteString("# M01: reads during writes\n\n| Arm | Read | idle p50 / p99 / max | during writes p50 / p99 / max | reads over 50 ms during writes | errors | WAL max | load start / end |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	var writes strings.Builder
	for _, arm := range Arms {
		for _, r := range RunsOf(runs, KindM01, "") {
			if r.Arm != arm {
				continue
			}
			type acc struct {
				idle, busy []float64
				errs       int
			}
			per := map[string]*acc{}
			var names []string
			for _, s := range r.Samples {
				if s.Class != M01Idle && s.Class != M01Busy {
					fmt.Fprintf(&writes, "| %s | %s | %s | %d | %s |\n", arm, esc(s.Shape), FormatMs(s.Ms), s.Rows, esc(s.Err))
					continue
				}
				a := per[s.Shape]
				if a == nil {
					a = &acc{}
					per[s.Shape] = a
					names = append(names, s.Shape)
				}
				switch {
				case s.Err != "":
					a.errs++
				case s.Class == M01Idle:
					a.idle = append(a.idle, s.Ms)
				default:
					a.busy = append(a.busy, s.Ms)
				}
			}
			sort.Strings(names)
			for _, n := range names {
				a := per[n]
				over := 0
				for _, v := range a.busy {
					if v > M01BlockedMs {
						over++
					}
				}
				tri := func(v []float64) string {
					return FormatMs(Percentile(v, 0.5)) + " / " + FormatMs(Percentile(v, 0.99)) + " / " + FormatMs(Percentile(v, 1))
				}
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %d of %d | %d | %s | %s / %s |\n", arm, esc(n), tri(a.idle), tri(a.busy), over,
					len(a.busy), a.errs, formatValue(extraFloat(r, "wal_max_bytes"), "B"), r.LoadStart, r.LoadEnd)
			}
		}
	}
	if writes.Len() > 0 {
		b.WriteString("\n| Arm | Write | Duration | Records | Error |\n|---|---|---|---|---|\n")
		b.WriteString(writes.String())
	}
	return b.String()
}

// M01 sample classes.
const (
	M01Idle = "M01_idle"
	M01Busy = "M01_busy"
)
