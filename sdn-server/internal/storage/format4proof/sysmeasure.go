package format4proof

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Load is the 1, 5 and 15 minute load averages, as the OS prints them.
func Load() string {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			return strings.Join(f[:3], " ")
		}
	}
	if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
		return strings.Trim(strings.TrimSpace(string(out)), "{} ")
	}
	return "n/a"
}

// RSSMB is the process's resident set now, in MiB.
func RSSMB() float64 {
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				f := strings.Fields(line)
				if len(f) >= 2 {
					kb, _ := strconv.ParseFloat(f[1], 64)
					return kb / 1024
				}
			}
		}
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	kb, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return kb / 1024
}

// OpenFDs counts the process's open file descriptors.
func OpenFDs() int {
	dir := "/dev/fd"
	if runtime.GOOS == "linux" {
		dir = "/proc/self/fd"
	}
	e, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(e)
}

// Tree is what a directory holds on disk.
type Tree struct {
	Apparent  int64 // Σ file sizes
	Allocated int64 // Σ allocated blocks × 512
	Files     int64
	WAL       int64 // Σ sizes of "*-wal" files
}

// DiskTree walks dir. skip names top-level entries left out (a migrated
// store's kept pre-migration copy, which serves no read).
func DiskTree(dir string, skip ...string) (Tree, error) {
	var t Tree
	skipSet := map[string]bool{}
	for _, s := range skip {
		skipSet[filepath.Join(dir, s)] = true
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // a file dropped under the walk (a live store)
			}
			return err
		}
		if skipSet[p] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		t.Files++
		t.Apparent += info.Size()
		t.Allocated += allocatedBytes(info)
		if strings.HasSuffix(p, "-wal") {
			t.WAL += info.Size()
		}
		return nil
	})
	return t, err
}

var runStart = time.Now()

// Snapshot is the measuring process's resource use now. store, when set, is
// walked for its WAL and total bytes (a few ms for a store of thousands of
// files; skip it on hot paths).
func Snapshot(at, store string) MemPoint {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	w, r := diskIO()
	m := MemPoint{At: at, ElapsedS: time.Since(runStart).Seconds(), RSSMB: RSSMB(), MaxRSSMB: MaxRSSMB(),
		GoHeapMB: float64(ms.HeapAlloc) / (1 << 20), DiskWMB: float64(w) / (1 << 20), DiskRMB: float64(r) / (1 << 20),
		FDs: OpenFDs(), Load: Load()}
	if store != "" {
		if t, err := DiskTree(store); err == nil {
			m.WALBytes, m.StoreBytes = t.WAL, t.Apparent
		}
	}
	return m
}
