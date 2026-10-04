package format4proof

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// store-migrate --to 4 under kill -9 (contract §4, §5.6): a clean migration
// of a format-1 clone is the reference (and, kept, the format-4 fixture the
// reads run on); a second clone is migrated with kill -9 at random points
// until a run completes. The resumed store must equal the reference record
// for record and tag for tag, pass REBUILD verify and be activated; the
// reference must equal format 1 itself, tag times included (migrated tags
// keep format 1's created_at).

// MigrateLoopSpec drives the loop.
type MigrateLoopSpec struct {
	Bin          string // a spacedatanetwork binary with store-migrate --to 4
	Source       string // the format-1 store (cloned, never written)
	Work, Out    string
	Kills        int           // kill -9s before a run may finish (default 5)
	MaxKillDelay time.Duration // kills land in [1 s, this); default 90% of what the clean run has left (below)
	Schemas      []string      // digested types (default the fixture's four)
}

// migrateRun runs store-migrate --to 4 on store, with extra arguments;
// kill > 0 kills it with SIGKILL after that long. It returns whether it was
// killed.
func migrateRun(ctx context.Context, bin, store, logPath string, kill time.Duration, extra ...string) (ChildRun, bool, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return ChildRun{}, false, err
	}
	log, err := os.Create(logPath)
	if err != nil {
		return ChildRun{}, false, err
	}
	defer log.Close()
	cmd := exec.CommandContext(ctx, bin, append([]string{"store-migrate", "--to", "4", "--store", store}, extra...)...)
	cmd.Stdout, cmd.Stderr = log, log
	// OpenArm (SettleStore, the digests) sets SDN_STORE_FORMAT in this
	// process; store-migrate opens format 1 and must not inherit it.
	cmd.Env = envWithout(format2.FormatEnv)
	st := time.Now()
	if err := cmd.Start(); err != nil {
		return ChildRun{}, false, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	killed := false
	var werr error
	if kill > 0 {
		select {
		case werr = <-done:
		case <-time.After(kill):
			_ = cmd.Process.Kill()
			<-done
			killed = true
		}
	} else {
		werr = <-done
	}
	cr := ChildRun{Wall: time.Since(st), Log: logPath, MaxRSSMB: ChildMaxRSSMB(cmd.ProcessState)}
	if cmd.ProcessState != nil {
		cr.ExitCode = cmd.ProcessState.ExitCode()
	}
	if killed {
		return cr, true, nil
	}
	if werr != nil {
		return cr, false, fmt.Errorf("store-migrate exited %d (log %s): %w", cr.ExitCode, logPath, werr)
	}
	return cr, false, nil
}

// checkMigrated checks an activated store: the markers and REBUILD verify.
func checkMigrated(store string) []string {
	var out []string
	m, err := marker.Read(store)
	switch {
	case err != nil:
		out = append(out, "markers: "+err.Error())
	case !m.Activated() || m.NeedsFinish():
		out = append(out, fmt.Sprintf("markers: activated=%v needs-finish=%v", m.Activated(), m.NeedsFinish()))
	}
	return append(out, VerifyFormat4Store(store)...)
}

// MigrateCrashLoop runs the reference migration (kept at ReferenceStore) and
// the killed one, and writes a crash run labelled "migrate".
func MigrateCrashLoop(ctx context.Context, spec MigrateLoopSpec, logf Logf) (*Run, error) {
	if spec.Kills <= 0 {
		spec.Kills = 5
	}
	if len(spec.Schemas) == 0 {
		spec.Schemas = pointSchemas
	}
	r := &Run{Kind: KindCrash, Arm: ArmS, Format: "4", Label: "migrate", Started: time.Now().UTC().Format(time.RFC3339),
		Machine: ThisMachine(), LoadStart: Load(), Extra: map[string]any{}}
	var violations []string
	fail := func(f string, a ...any) { violations = append(violations, fmt.Sprintf(f, a...)) }
	logs := filepath.Join(spec.Out, "logs")

	// 1. The reference: a clean migration (its time, RSS and load are the
	// migration evidence), digested and checked against format 1, then
	// settled. P4PROOF_MIGRATE_REUSE_REF=1 reuses a settled reference a
	// previous run left (its clean run's seconds in P4PROOF_MIGRATE_REF_S)
	// and digests a clone of it, so a kill-loop re-run skips the reference.
	ref := ReferenceStore(spec.Work)
	var refWall time.Duration
	var refDigest map[string]TypeDigest
	var err error
	if os.Getenv("P4PROOF_MIGRATE_REUSE_REF") == "1" {
		refWall = time.Duration(envInt("P4PROOF_MIGRATE_REF_S", 501)) * time.Second
		refDigest, err = digestClone(ref, spec.Work, spec.Schemas)
		if err != nil {
			return r, fmt.Errorf("digest the reference: %w", err)
		}
		r.Extra["reference_reused"], r.Extra["reference_seconds"] = ref, refWall.Seconds()
		logf("migrate: reusing the settled reference %s (clean run %s)", ref, refWall)
	} else if refWall, refDigest, err = migrateReference(ctx, spec, ref, logs, r, fail, logf); err != nil {
		return r, err
	}

	// 2. The killed migration, resumed until a run completes.
	crash := filepath.Join(spec.Work, "migrate-crash")
	_ = os.RemoveAll(crash)
	if err := CloneStore(spec.Source, crash); err != nil {
		return r, err
	}
	defer discardStore(crash)
	maxDelay := spec.MaxKillDelay
	if maxDelay <= 0 {
		maxDelay = time.Duration(float64(refWall) * 0.9)
	}
	if maxDelay < 2*time.Second {
		maxDelay = 2 * time.Second
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	kills := 0
	// A resumed run only does what the killed ones left (progress is
	// journalled), so a delay drawn against the whole clean run often lands
	// after the resumed run is done (GATES-feed-r1 N6: 2 kills of 5). Each
	// delay is drawn against what the clean run has left after the killed
	// runs' time, at least 2 s (unless MaxKillDelay is set).
	var killedFor time.Duration
	for i := 1; ; i++ {
		var delay time.Duration
		if kills < spec.Kills {
			bound := maxDelay
			if spec.MaxKillDelay <= 0 {
				if left := time.Duration(float64(refWall-killedFor) * 0.9); left < bound {
					bound = left
				}
				if bound < 2*time.Second {
					bound = 2 * time.Second
				}
			}
			delay = time.Second + time.Duration(rng.Int63n(int64(bound-time.Second)))
		}
		run, killed, err := migrateRun(ctx, spec.Bin, crash, filepath.Join(logs, fmt.Sprintf("migrate-crash-%02d.log", i)), delay)
		if killed {
			kills++
			killedFor += run.Wall
			logf("migrate: run %d killed after %s", i, run.Wall.Round(time.Millisecond))
			continue
		}
		if err != nil {
			fail("run %d after %d kills: %v", i, kills, err)
			break
		}
		logf("migrate: run %d completed after %d kills in %s", i, kills, run.Wall.Round(time.Second))
		break
	}
	r.Extra["kills"] = float64(kills)
	for _, p := range checkMigrated(crash) {
		fail("resumed: %s", p)
	}
	got, err := DigestStore(ArmS, crash, spec.Schemas)
	if err != nil {
		fail("digest the resumed store: %v", err)
	} else {
		for _, schema := range spec.Schemas {
			for _, d := range DigestDiff(refDigest[schema], got[schema]) {
				fail("resumed vs reference %s: %s", schema, d)
			}
			if refDigest[schema].TagsAt != got[schema].TagsAt {
				fail("resumed vs reference %s: tag times differ", schema)
			}
		}
	}
	r.Extra["violations"] = violations
	r.Extra["reference_digest"] = refDigest
	r.Extra["integrity"] = IntegrityNote
	r.LoadEnd = Load()
	if _, err := WriteRun(spec.Out, r); err != nil {
		return r, err
	}
	if len(violations) > 0 {
		return r, fmt.Errorf("%d violations, first: %s", len(violations), violations[0])
	}
	return r, nil
}

// migrateReference migrates a clone of spec.Source into ref, checks it,
// compares it with format 1 (tag times included) and settles it. It returns
// the clean run's wall time and the reference digest.
func migrateReference(ctx context.Context, spec MigrateLoopSpec, ref, logs string, r *Run, fail func(string, ...any), logf Logf) (time.Duration, map[string]TypeDigest, error) {
	_ = os.RemoveAll(ref)
	if err := CloneStore(spec.Source, ref); err != nil {
		return 0, nil, err
	}
	cr, _, err := migrateRun(ctx, spec.Bin, ref, filepath.Join(logs, "migrate-reference.log"), 0)
	if err != nil {
		return 0, nil, err
	}
	r.Extra["reference_seconds"], r.Extra["reference_max_rss_mb"], r.Extra["reference_load_end"] = cr.Wall.Seconds(), cr.MaxRSSMB, Load()
	logf("migrate: reference in %s, max RSS %.0f MB, load %s", cr.Wall.Round(time.Second), cr.MaxRSSMB, Load())
	for _, p := range checkMigrated(ref) {
		fail("reference: %s", p)
	}
	// The owner's layout (C-37): one file per source feed x standard, no
	// provider or source column in any feed file.
	f1Layout := filepath.Join(spec.Work, "migrate-f1-layout")
	_ = os.RemoveAll(f1Layout)
	if err := CloneStore(spec.Source, f1Layout); err != nil {
		return 0, nil, err
	}
	lay, err := CheckFeedLayout(f1Layout, ref, spec.Work)
	_ = os.RemoveAll(f1Layout)
	if err != nil {
		return 0, nil, fmt.Errorf("check the feed layout: %w", err)
	}
	r.Extra["layout"] = lay
	for _, p := range lay.Problems {
		fail("layout: %s", p)
	}
	logf("migrate: layout %v; %d problems", lay.Files, len(lay.Problems))
	refDigest, err := DigestStore(ArmS, ref, spec.Schemas)
	if err != nil {
		return 0, nil, fmt.Errorf("digest the reference: %w", err)
	}
	// The reference against format 1 itself.
	f1 := filepath.Join(spec.Work, "migrate-f1-digest")
	_ = os.RemoveAll(f1)
	if err := CloneStore(spec.Source, f1); err != nil {
		return 0, nil, err
	}
	f1Digest, err := DigestFormat1(f1, spec.Schemas)
	_ = os.RemoveAll(f1)
	if err != nil {
		return 0, nil, fmt.Errorf("digest format 1: %w", err)
	}
	for _, schema := range spec.Schemas {
		for _, d := range DigestDiff(f1Digest[schema], refDigest[schema]) {
			fail("format 1 vs migrated %s: %s", schema, d)
		}
		if f1Digest[schema].TagsAt != refDigest[schema].TagsAt {
			fail("format 1 vs migrated %s: tag times differ (f1 %s, s %s)", schema, f1Digest[schema].TagsAt, refDigest[schema].TagsAt)
		}
	}
	// The reference becomes the store a daemon leaves after its first start
	// (every full-text index SDN enables built, WAL at rest), so reads time
	// no background build and bytes count every file.
	states, took, err := SettleStore(ref)
	r.Extra["reference_fts_states"], r.Extra["reference_settle_s"] = states, took.Seconds()
	if err != nil {
		return 0, nil, fmt.Errorf("settle the reference: %w", err)
	}
	logf("migrate: reference settled in %s (full text %v)", took.Round(time.Second), states)
	return cr.Wall, refDigest, nil
}

// digestClone digests a clone of store (a digest opens the store, which
// must stay as it is).
func digestClone(store, work string, schemas []string) (map[string]TypeDigest, error) {
	tmp := filepath.Join(work, "migrate-ref-digest")
	_ = os.RemoveAll(tmp)
	if err := CloneStore(store, tmp); err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	return DigestStore(ArmS, tmp, schemas)
}

// envWithout is this process's environment without key.
func envWithout(key string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// ReferenceStore is where the clean migration is kept: the format-4 fixture
// the read runs use when P4_FIXTURE is not set.
func ReferenceStore(work string) string { return filepath.Join(work, "p4-fixture") }
