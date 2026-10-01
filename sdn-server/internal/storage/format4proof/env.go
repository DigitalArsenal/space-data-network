package format4proof

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Environment. Fixture paths come from here and are never written: every
// store a run opens is a clone (cp -c, an APFS clone; a reflink copy on
// Linux) in the work directory.
const (
	EnvF1Fixture  = "SDN_F1_FIXTURE" // format-1 store directory (holds control.flatsqldb)
	EnvF2Fixture  = "SDN_F2_FIXTURE" // the same fixture after store-migrate --to 2
	EnvP4Fixture  = "P4_FIXTURE"     // the same fixture after store-migrate --to 4
	EnvBenchset   = "P4PROOF_BENCHSET"
	EnvWork       = "P4PROOF_WORK"    // clones and grown stores (same volume as the fixtures)
	EnvOut        = "P4PROOF_OUT"     // results: run JSON, answers, tables, the gate report
	EnvArms       = "P4PROOF_ARMS"    // comma list, default "s,f1,f2"
	EnvClasses    = "P4PROOF_CLASSES" // comma list of benchset ids, default every read the fixture covers
	EnvWarm       = "P4PROOF_WARM"    // warm passes per shape (overrides the per-class default)
	EnvCallLimit  = "P4PROOF_CALL_LIMIT_S"
	EnvColdRounds = "P4PROOF_COLD_ROUNDS" // cold rounds per (arm, class), each a fresh process on a fresh clone (1)
	EnvSDNBin     = "P4PROOF_SDN_BIN"     // a spacedatanetwork binary (store-migrate kill loops)
	EnvChild      = "P4PROOF_CHILD"       // set by the driver for a measurement child
	// The real host-02 copy (benchset h2copy: R21 PNM, R22, R24), per arm.
	EnvH2F1 = "P4PROOF_H2_F1"
	EnvH2F2 = "P4PROOF_H2_F2"
	EnvH2S  = "P4PROOF_H2_S"
)

// Config is the harness environment.
type Config struct {
	Fixtures   map[string]string // arm -> fixture store directory
	H2         map[string]string // arm -> h2copy store directory
	Benchset   string
	Work, Out  string
	Arms       []string
	Classes    []string
	Warm       int // -1 = per-class default
	ColdRounds int
	CallLimit  int // seconds
	SDNBin     string
}

// ConfigFromEnv reads the environment.
func ConfigFromEnv() Config {
	c := Config{
		Fixtures:   map[string]string{},
		H2:         map[string]string{},
		Benchset:   os.Getenv(EnvBenchset),
		Work:       os.Getenv(EnvWork),
		Out:        os.Getenv(EnvOut),
		Arms:       splitList(os.Getenv(EnvArms)),
		Classes:    splitList(os.Getenv(EnvClasses)),
		Warm:       envInt(EnvWarm, -1),
		ColdRounds: envInt(EnvColdRounds, 1),
		CallLimit:  envInt(EnvCallLimit, 330),
		SDNBin:     os.Getenv(EnvSDNBin),
	}
	for arm, env := range map[string]string{ArmF1: EnvF1Fixture, ArmF2: EnvF2Fixture, ArmS: EnvP4Fixture} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			c.Fixtures[arm] = v
		}
	}
	for arm, env := range map[string]string{ArmF1: EnvH2F1, ArmF2: EnvH2F2, ArmS: EnvH2S} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			c.H2[arm] = v
		}
	}
	if len(c.Arms) == 0 {
		c.Arms = append([]string(nil), Arms...)
	}
	// Without P4_FIXTURE, the format-4 fixture is the reference migration
	// TestProofMigrate keeps in the work directory.
	if c.Fixtures[ArmS] == "" && c.Work != "" {
		if _, err := os.Stat(filepath.Join(ReferenceStore(c.Work), "fsql4", "STORE")); err == nil {
			c.Fixtures[ArmS] = ReferenceStore(c.Work)
		}
	}
	return c
}

// ArmFormat is the SDN_STORE_FORMAT value an arm opens its store with.
func ArmFormat(arm string) string {
	switch arm {
	case ArmF2:
		return "2"
	case ArmS:
		return "4"
	}
	return ""
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func envInt(k string, d int) int {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}

// IsFixturePath reports whether p is, or lies inside, one of the fixture
// directories (which the harness never opens or writes).
func (c Config) IsFixturePath(p string) bool {
	ap, err := filepath.Abs(p)
	if err != nil {
		return true
	}
	all := []string{}
	for _, f := range c.Fixtures {
		all = append(all, f)
	}
	for _, f := range c.H2 {
		all = append(all, f)
	}
	for _, f := range all {
		af, err := filepath.Abs(f)
		if err != nil {
			continue
		}
		if ap == af || strings.HasPrefix(ap, af+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// CloneStore copies the store directory src to dst (which must not exist)
// as a copy-on-write clone: cp -c on macOS (APFS clonefile), cp --reflink=auto
// elsewhere. src is only read.
func CloneStore(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("format4proof: clone target %s exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("cp", "-c", "-R", src, dst)
	} else {
		cmd = exec.Command("cp", "-a", "--reflink=auto", src, dst)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("format4proof: clone %s: %v: %s", src, err, strings.TrimSpace(string(out)))
	}
	return nil
}
