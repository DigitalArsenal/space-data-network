package format4proof

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Measurement children: every measured process is the test binary itself,
// re-executed with -test.run '^TestProofChild$' and the spec in EnvChild, so
// each (arm, class) starts cold in a fresh process and a kill -9 hits a real
// process with real files.

// Child modes.
const (
	ModeReads          = "reads"
	ModeIngest         = "ingest"
	ModeM01            = "m01"
	ModeWrite          = "write"
	ModePrepare        = "prepare"
	ModeCrashIngest    = "crash-ingest"
	ModeCrashSupersede = "crash-supersede"
	ModeCrashVerify    = "crash-verify"
)

// ChildSpec is what a child does.
type ChildSpec struct {
	Mode     string       `json:"mode"`
	Benchset string       `json:"benchset,omitempty"`
	Work     string       `json:"work,omitempty"`
	Read     *ReadSpec    `json:"read,omitempty"`
	Ingest   *IngestSpec  `json:"ingest,omitempty"`
	M01      *M01Spec     `json:"m01,omitempty"`
	Write    *WriteSpec   `json:"write,omitempty"`
	Crash    *CrashSpec   `json:"crash,omitempty"`
	Prepare  *PrepareSpec `json:"prepare,omitempty"`
}

// PrepareSpec extracts the inputs from a format-1 clone.
type PrepareSpec struct{ Store, Work string }

// ChildRun is a finished child.
type ChildRun struct {
	ExitCode int
	Killed   bool
	MaxRSSMB float64
	Wall     time.Duration
	Log      string
}

// StartChild starts a child of the running test binary (env added to its
// environment); its output goes to logPath. The caller waits (or kills)
// through the returned command.
func StartChild(spec ChildSpec, logPath string, env ...string) (*exec.Cmd, *os.File, error) {
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, nil, err
	}
	log, err := os.Create(logPath)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestProofChild$", "-test.v", "-test.count=1", "-test.timeout", "12h")
	cmd.Env = append(append(os.Environ(), env...), EnvChild+"="+string(b))
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, nil, err
	}
	return cmd, log, nil
}

// RunChild runs a child to completion (or until ctx ends, which kills it).
func RunChild(ctx context.Context, spec ChildSpec, logPath string) (ChildRun, error) {
	st := time.Now()
	cmd, log, err := StartChild(spec, logPath)
	if err != nil {
		return ChildRun{}, err
	}
	defer log.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var werr error
	select {
	case werr = <-done:
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		werr = <-done
	}
	cr := ChildRun{Wall: time.Since(st), Log: logPath, MaxRSSMB: ChildMaxRSSMB(cmd.ProcessState)}
	if cmd.ProcessState != nil {
		cr.ExitCode = cmd.ProcessState.ExitCode()
	}
	if werr != nil {
		return cr, fmt.Errorf("child %s exited %d (log %s): %w", spec.Mode, cr.ExitCode, logPath, werr)
	}
	return cr, nil
}

// ChildSpecFromEnv decodes EnvChild (nil when unset).
func ChildSpecFromEnv() (*ChildSpec, error) {
	v := os.Getenv(EnvChild)
	if v == "" {
		return nil, nil
	}
	var spec ChildSpec
	if err := json.Unmarshal([]byte(v), &spec); err != nil {
		return nil, fmt.Errorf("%s: %w", EnvChild, err)
	}
	return &spec, nil
}

// ExecChild runs a child spec in this process.
func ExecChild(spec *ChildSpec) error {
	if err := PrewarmAOT(); err != nil {
		return err
	}
	switch spec.Mode {
	case ModePrepare:
		return Prepare(spec.Prepare.Store, spec.Prepare.Work, func(f string, a ...any) { fmt.Printf(f+"\n", a...) })
	case ModeReads:
		bs, err := LoadBenchset(spec.Benchset)
		if err != nil {
			return err
		}
		in, _, err := LoadInputs(spec.Work)
		if err != nil {
			return err
		}
		shapes, err := BuildShapes(bs, in)
		if err != nil {
			return err
		}
		fixture := FixtureT6W
		if spec.Read.Label == FixtureH2Copy {
			fixture = FixtureH2Copy
		}
		only, err := shapeFilter(spec.Read.Shape)
		if err != nil {
			return err
		}
		sh := ShapesForArm(ShapesOf(shapes, spec.Read.Class, fixture, only), spec.Read.Arm)
		if len(sh) == 0 {
			return fmt.Errorf("no %s shapes for class %s", fixture, spec.Read.Class)
		}
		_, err = RunReads(*spec.Read, sh)
		return err
	case ModeIngest:
		_, err := RunIngest(*spec.Ingest)
		return err
	case ModeM01:
		hits, err := benchsetHits(spec.Benchset)
		if err != nil {
			return err
		}
		_, err = RunM01(*spec.M01, hits)
		return err
	case ModeWrite:
		hits, err := benchsetHits(spec.Benchset)
		if err != nil {
			return err
		}
		_, err = RunWrite(*spec.Write, hits)
		return err
	case ModeCrashIngest, ModeCrashSupersede:
		return CrashWriter(*spec.Crash, spec.Mode)
	case ModeCrashVerify:
		_, err := CrashVerify(*spec.Crash)
		return err
	}
	return fmt.Errorf("unknown child mode %q", spec.Mode)
}

func benchsetHits(path string) (map[string][]string, error) {
	bs, err := LoadBenchset(path)
	if err != nil {
		return nil, err
	}
	r01, ok := bs.Read("R01")
	if !ok {
		return nil, fmt.Errorf("benchset has no R01")
	}
	return hitLists(r01)
}
