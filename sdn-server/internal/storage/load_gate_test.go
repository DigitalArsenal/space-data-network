package storage

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// loadRatio reports the 1-minute load average divided by the core count —
// the gauntlet's own measure (graph/gauntlet/go-tier.mjs exports
// GO_TIER_LOADAVG1 / GO_TIER_NCPU / GO_TIER_LOAD_RATIO); outside the gauntlet
// it is read from the OS. A wall-clock assertion is only evidence when the box
// could honour it: on a 28-core Studio a loadavg of 14 is 50 % busy, which is
// where timing comparisons start lying (measured 2026-09-02: the warm-boot
// order-of-magnitude check flipped under a parallel gauntlet + a migration).
func loadRatio() float64 {
	envFloat := func(k string) float64 {
		v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(k)), 64)
		if err != nil || v < 0 {
			return 0
		}
		return v
	}
	if r := envFloat("GO_TIER_LOAD_RATIO"); r > 0 {
		return r
	}
	load := envFloat("GO_TIER_LOADAVG1")
	if load == 0 {
		switch runtime.GOOS {
		case "linux":
			if b, err := os.ReadFile("/proc/loadavg"); err == nil {
				if f := strings.Fields(string(b)); len(f) > 0 {
					load, _ = strconv.ParseFloat(f[0], 64)
				}
			}
		case "darwin":
			if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
				// "{ 4.67 5.07 5.86 }"
				if f := strings.Fields(strings.Trim(strings.TrimSpace(string(out)), "{} ")); len(f) > 0 {
					load, _ = strconv.ParseFloat(f[0], 64)
				}
			}
		}
	}
	ncpu := envFloat("GO_TIER_NCPU")
	if ncpu == 0 {
		ncpu = float64(runtime.NumCPU())
	}
	if ncpu == 0 {
		return 0
	}
	return load / ncpu
}

// skipTimingUnlessQuiet skips a test whose ASSERTION is a wall-clock
// comparison when the box is more than half busy. Correctness tests never
// call this; only the "is it an order of magnitude faster" class does.
func skipTimingUnlessQuiet(t *testing.T) {
	t.Helper()
	if r := loadRatio(); r > 0.5 {
		t.Skipf("timing assertion needs a quiet box: load/cores = %.2f > 0.50", r)
	}
}
