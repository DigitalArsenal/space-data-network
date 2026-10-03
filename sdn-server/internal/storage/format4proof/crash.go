package format4proof

import (
	"bufio"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// kill -9 loops through SDN's storage layer (contract §4): a writer child
// ingests (or supersedes) and logs every ACK — a call that returned — to a
// file off the store, fsynced; a follower in the same child reads the
// datasync cursor and logs every (seq, cid) it saw. The parent kills the
// child with SIGKILL at a random point, then a verifier child reopens the
// store (recovery runs inside the open) and checks:
//
//  1. every acked record is present, with its bytes and tag;
//  2. every (seq, cid) a follower saw still names that CID: a seq that was
//     visible is never reassigned;
//  3. new writes after recovery get seqs above every seq on disk and every
//     seq a follower saw;
//  4. format 4: the engine's derived state rebuilds equal to the live state
//     (REBUILD verify, 0 mismatches) and every file passes integrity_check;
//  5. format 4: the type directory holds the crash and verify feeds' files
//     only, under the engine's names (feedFileName), and the crash feed's
//     records are in its file (CrashSpec.Source may be any feed name).
//
// Records are synthetic OMMs (sds.NewOMMBuilder), so the loops need no
// fixture and run on a fresh store of any format.

// CrashSpec is one crash round (writer or verifier).
type CrashSpec struct {
	Arm      string `json:"arm"`
	Scenario string `json:"scenario"` // "ingest" or "supersede"
	Store    string `json:"store"`
	Logs     string `json:"logs"` // the ack and follower logs (off the store)
	Out      string `json:"out"`
	Round    int    `json:"round"`
	Calls    int    `json:"calls"` // writer calls per round (the kill usually comes first)
	Batch    int    `json:"batch"` // records per call
	// Source is the feed's source name ("" = crashSource): the writer's
	// lane, the follower's cursor filter and the verifier's reads.
	Source string `json:"source,omitempty"`
}

// source is the round's feed source (crashSource unless Source is set).
func (spec CrashSpec) source() string {
	if spec.Source != "" {
		return spec.Source
	}
	return crashSource
}

// Crash scenarios.
const (
	ScenarioIngest    = "ingest"
	ScenarioSupersede = "supersede"
)

const (
	crashSchema   = "OMM.fbs"
	crashSource   = "proof-crash"
	crashPeer     = "source:proof-crash"
	verifySource  = "proof-crash-verify"
	crashEpoch0   = 1788220801 // 2026-09-01T00:00:01Z
	crashCallsMax = 1000
)

func crashTags(source, batch string) storage.SourceTags {
	return storage.SourceTags{ProviderID: FixtureProvider, SourceName: source, BatchID: batch}
}

// crashBatchID names a writer call's batch: one per call when ingesting (so
// each call's records are found by batch), one per round when superseding
// (the supersede keeps the round's batch).
func crashBatchID(scenario string, round, call int) string {
	if scenario == ScenarioSupersede {
		return fmt.Sprintf("sup-%04d", round)
	}
	return fmt.Sprintf("crash-%04d-%04d", round, call)
}

// CrashRecords is call `call` of round `round`: n distinct OMMs, the same
// bytes in every process (every builder field is set; none takes the clock).
func CrashRecords(round, call, n int) [][]byte {
	out := make([][]byte, n)
	for i := 0; i < n; i++ {
		k := int64(call*n + i)
		norad := uint32(round*1_000_000 + call*1024 + i + 1)
		epoch := time.Unix(crashEpoch0+k*37, 0).UTC()
		b := sds.NewOMMBuilder().
			WithNoradCatID(norad).
			WithObjectName(fmt.Sprintf("PROOF CRASH %d", norad)).
			WithObjectID(fmt.Sprintf("2026-%03dA", norad%1000)).
			WithEpoch(epoch.Format("2006-01-02T15:04:05Z")).
			WithEpochTimestamp(float64(epoch.Unix())).
			WithMeanMotion(15.5).
			WithEccentricity(0.0001).
			WithInclination(53.0).
			WithCreationDate("2026-10-01T00:00:00Z"). // the builder's default is the wall clock
			Build()
		out[i] = b[4:] // the builders size-prefix; stores take bare buffers
	}
	return out
}

// appendLine appends one line to a log and fsyncs it (an ACK is durable in
// the log before the next call starts).
type syncLog struct{ f *os.File }

func openSyncLog(path string) (*syncLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &syncLog{f: f}, nil
}

func (l *syncLog) line(s string) error {
	if _, err := l.f.WriteString(s + "\n"); err != nil {
		return err
	}
	return l.f.Sync()
}

func (l *syncLog) close() { l.f.Close() }

func ackPath(logs string) string    { return filepath.Join(logs, "acks.log") }
func followPath(logs string) string { return filepath.Join(logs, "follow.log") }

// CrashWriter is the child the parent kills.
func CrashWriter(spec CrashSpec, mode string) error {
	if spec.Batch <= 0 {
		spec.Batch = 1024
	}
	if spec.Calls <= 0 {
		spec.Calls = crashCallsMax
	}
	acks, err := openSyncLog(ackPath(spec.Logs))
	if err != nil {
		return err
	}
	defer acks.close()
	s, _, err := OpenArm(spec.Arm, spec.Store)
	if err != nil {
		return err
	}
	stopFollow := make(chan struct{})
	followDone := make(chan struct{})
	if mode == ModeCrashIngest {
		go follow(s, spec, stopFollow, followDone)
	} else {
		close(followDone)
	}
	_ = acks.line(fmt.Sprintf("START %d", spec.Round))
	for call := 0; call < spec.Calls; call++ {
		recs := CrashRecords(spec.Round, call, spec.Batch)
		n, err := s.StoreBatchWithSourceTags(crashSchema, recs, crashPeer, nil,
			crashTags(spec.source(), crashBatchID(spec.Scenario, spec.Round, call)))
		if err != nil {
			_ = acks.line(fmt.Sprintf("ERR %d %d %s", spec.Round, call, strings.ReplaceAll(err.Error(), "\n", " ")))
			continue
		}
		if err := acks.line(fmt.Sprintf("ACK %d %d %d", spec.Round, call, n)); err != nil {
			return err
		}
	}
	if mode == ModeCrashSupersede {
		_ = acks.line(fmt.Sprintf("SUPSTART %d", spec.Round))
		res, err := s.SupersedeSourceBatches(crashSchema, FixtureProvider, spec.source(), crashBatchID(spec.Scenario, spec.Round, 0))
		if err != nil {
			_ = acks.line(fmt.Sprintf("ERR %d supersede %s", spec.Round, err))
		} else {
			_ = acks.line(fmt.Sprintf("SUPDONE %d %d %d", spec.Round, res.TagsDeleted, res.RecordsDeleted))
		}
	}
	close(stopFollow)
	<-followDone
	return s.Close()
}

// follow reads the lane's datasync cursor and logs every (seq, cid) seen.
func follow(s *storage.FlatSQLStore, spec CrashSpec, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	l, err := openSyncLog(followPath(spec.Logs))
	if err != nil {
		return
	}
	defer l.close()
	after := int64(0)
	for {
		select {
		case <-stop:
			return
		default:
		}
		recs, err := s.QueryRawRecordRefs(storage.RawRecordQuery{SchemaName: crashSchema, SourceName: spec.source(),
			Limit: 1000, UseRowIDCursor: true, AfterRowID: after})
		if err != nil || len(recs) == 0 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		var b strings.Builder
		for _, r := range recs {
			fmt.Fprintf(&b, "SEQ %d %s\n", r.RowID, r.CID)
			after = r.RowID
		}
		if _, err := l.f.WriteString(b.String()); err != nil {
			return
		}
		_ = l.f.Sync()
	}
}

// crashLog is what the logs say.
type crashLog struct {
	acked    map[int][]int    // round -> acked calls
	seen     map[int64]string // seq -> cid (follower)
	errs     []string
	supDone  map[int]bool
	maxRound int
}

func readCrashLogs(logs string) (crashLog, error) {
	cl := crashLog{acked: map[int][]int{}, seen: map[int64]string{}, supDone: map[int]bool{}}
	scan := func(path string, fn func([]string)) error {
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			fn(strings.Fields(sc.Text()))
		}
		return sc.Err() // a torn last line is skipped by the field checks
	}
	err := scan(ackPath(logs), func(f []string) {
		if len(f) < 2 {
			return
		}
		round, _ := strconv.Atoi(f[1])
		if round > cl.maxRound {
			cl.maxRound = round
		}
		switch f[0] {
		case "ACK":
			if len(f) >= 4 {
				call, err := strconv.Atoi(f[2])
				if err == nil {
					cl.acked[round] = append(cl.acked[round], call)
				}
			}
		case "SUPDONE":
			cl.supDone[round] = true
		case "ERR":
			cl.errs = append(cl.errs, strings.Join(f, " "))
		}
	})
	if err != nil {
		return cl, err
	}
	err = scan(followPath(logs), func(f []string) {
		if len(f) != 3 || f[0] != "SEQ" {
			return
		}
		seq, err := strconv.ParseInt(f[1], 10, 64)
		if err == nil && strings.HasPrefix(f[2], "bafkrei") && len(f[2]) == 59 {
			cl.seen[seq] = f[2]
		}
	})
	return cl, err
}

// laneRecords reads every record of a lane (optionally one batch) in seq order.
func laneRecords(s *storage.FlatSQLStore, source, batch string) ([]*storage.Record, error) {
	var out []*storage.Record
	after := int64(0)
	for {
		recs, err := s.QueryRawRecordRefs(storage.RawRecordQuery{SchemaName: crashSchema, SourceName: source, BatchID: batch,
			Limit: 5000, UseRowIDCursor: true, AfterRowID: after})
		if err != nil {
			return nil, err
		}
		if len(recs) == 0 {
			return out, nil
		}
		out = append(out, recs...)
		after = recs[len(recs)-1].RowID
	}
}

// CrashVerify reopens the store after a kill and checks the round.
func CrashVerify(spec CrashSpec) (*Run, error) {
	if spec.Batch <= 0 {
		spec.Batch = 1024
	}
	r := &Run{Kind: KindCrash, Arm: spec.Arm, Format: ArmFormat(spec.Arm), Label: spec.Scenario,
		Class: fmt.Sprintf("r%04d", spec.Round), Started: time.Now().UTC().Format(time.RFC3339), Machine: ThisMachine(),
		LoadStart: Load(), Extra: map[string]any{}}
	var violations []string
	fail := func(f string, a ...any) {
		if len(violations) < 50 {
			violations = append(violations, fmt.Sprintf(f, a...))
		}
	}
	cl, err := readCrashLogs(spec.Logs)
	if err != nil {
		return r, err
	}
	s, openMs, err := OpenArm(spec.Arm, spec.Store)
	r.OpenMs = openMs // recovery runs inside the open
	if err != nil {
		return r, err
	}
	all, err := laneRecords(s, spec.source(), "")
	if err != nil {
		return r, err
	}
	bySeq := map[int64]*storage.Record{}
	byCID := map[string]*storage.Record{}
	var maxSeq int64
	for _, rec := range all {
		bySeq[rec.RowID] = rec
		byCID[rec.CID] = rec
		if rec.RowID > maxSeq {
			maxSeq = rec.RowID
		}
	}
	// 1. Acked records are present, with their bytes and tag.
	checked := 0
	rounds := make([]int, 0, len(cl.acked))
	for round := range cl.acked {
		rounds = append(rounds, round)
	}
	sort.Ints(rounds)
	lastSupRound := 0
	for _, round := range rounds {
		if spec.Scenario == ScenarioSupersede && round > lastSupRound {
			lastSupRound = round
		}
	}
	for _, round := range rounds {
		// After a supersede that kept a later round's batch started, an
		// earlier round's records may be gone: only the newest acked round
		// must be whole.
		if spec.Scenario == ScenarioSupersede && round != lastSupRound {
			continue
		}
		for _, call := range cl.acked[round] {
			for i, want := range CrashRecords(round, call, spec.Batch) {
				cid := storage.ComputeCID(want)
				rec := byCID[cid]
				checked++
				switch {
				case rec == nil:
					fail("round %d call %d record %d %s: acked, missing after recovery", round, call, i, cid)
				case string(rec.Data) != string(want):
					fail("round %d call %d %s: bytes differ after recovery", round, call, cid)
				case rec.SourceTags.BatchID != crashBatchID(spec.Scenario, round, call):
					fail("round %d call %d %s: tag batch %q after recovery", round, call, cid, rec.SourceTags.BatchID)
				}
			}
		}
	}
	// 2. Every seq a follower saw still names its CID.
	seen := 0
	for seq, cid := range cl.seen {
		seen++
		if seq > maxSeq {
			maxSeq = seq
		}
		rec := bySeq[seq]
		if rec == nil {
			if spec.Scenario != ScenarioSupersede {
				fail("seq %d (%s) was visible to a follower and is gone after recovery", seq, cid)
			}
			continue
		}
		if rec.CID != cid {
			fail("seq %d named %s before the crash and %s after it", seq, cid, rec.CID)
		}
	}
	// Supersede: finish the interrupted supersede, then the lane must hold
	// exactly the kept batch, every acked record of it included.
	if spec.Scenario == ScenarioSupersede && lastSupRound > 0 {
		keep := crashBatchID(spec.Scenario, lastSupRound, 0)
		res, err := s.SupersedeSourceBatches(crashSchema, FixtureProvider, spec.source(), keep)
		if err != nil {
			fail("finishing the supersede keeping %s: %v", keep, err)
		} else {
			r.Extra["finish_records_deleted"] = float64(res.RecordsDeleted)
		}
		lane, err := laneRecords(s, spec.source(), "")
		if err != nil {
			return r, err
		}
		n, err := s.CountRawRecords(storage.RawRecordQuery{SchemaName: crashSchema, SourceName: spec.source()})
		if err != nil {
			fail("count after the supersede: %v", err)
		} else if n != int64(len(lane)) {
			fail("the lane counts %d records and reads %d after the supersede", n, len(lane))
		}
		for _, rec := range lane {
			if rec.SourceTags.BatchID != keep {
				fail("%s of batch %s survived the supersede keeping %s", rec.CID, rec.SourceTags.BatchID, keep)
				break
			}
		}
		if acked := len(cl.acked[lastSupRound]) * spec.Batch; len(lane) < acked {
			fail("after the supersede the kept batch holds %d records, %d were acked", len(lane), acked)
		}
	}
	// 3. New writes take seqs above everything seen: on disk now, seen by a
	// follower, or seen by an earlier round's verifier (a deleted record's
	// seq is never reused either).
	head, err := s.RawRecordHead(storage.RawRecordQuery{SchemaName: crashSchema})
	if err != nil {
		return r, err
	}
	floor := maxSeq
	if head.MaxRowID > floor {
		floor = head.MaxRowID
	}
	if f := readFloor(spec.Logs); f > floor {
		floor = f
	}
	vb := fmt.Sprintf("verify-%04d", spec.Round)
	if _, err := s.StoreBatchWithSourceTags(crashSchema, CrashRecords(spec.Round, crashCallsMax+1, 64), crashPeer, nil,
		crashTags(verifySource, vb)); err != nil {
		fail("a write after recovery failed: %v", err)
	} else {
		fresh, err := laneRecords(s, verifySource, vb)
		if err != nil {
			return r, err
		}
		if len(fresh) != 64 {
			fail("the write after recovery stored %d of 64 records", len(fresh))
		}
		top := floor
		for _, rec := range fresh {
			if rec.RowID <= floor {
				fail("a write after recovery took seq %d, at or below %d (on disk or seen)", rec.RowID, floor)
				break
			}
			if rec.RowID > top {
				top = rec.RowID
			}
		}
		writeFloor(spec.Logs, top)
	}
	held, err := s.CountRawRecords(storage.RawRecordQuery{SchemaName: crashSchema, SourceName: spec.source()})
	if err != nil {
		fail("count the lane after verification: %v", err)
	}
	if err := s.Close(); err != nil {
		fail("close after verification: %v", err)
	}
	// 4. Format 4: the derived state equals a rebuild from the files.
	if spec.Arm == ArmS {
		for _, v := range VerifyFormat4Store(spec.Store) {
			fail("%s", v)
		}
		r.Extra["integrity"] = IntegrityNote
		// 5. The feeds' files (C-37 (1)): the type directory holds the files
		// of the crash and verify feeds only, under the engine's names, and
		// the crash feed's records are in its file.
		feed := feedFileName(FixtureProvider, spec.source())
		var nonEmpty []string
		if held > 0 {
			nonEmpty = []string{feed}
		}
		for _, v := range feedDirProblems(spec.Store, strings.TrimSuffix(crashSchema, ".fbs"),
			[]string{feed, feedFileName(FixtureProvider, verifySource)}, nonEmpty) {
			fail("%s", v)
		}
	}
	r.Extra["acked_records_checked"] = float64(checked)
	r.Extra["follower_seqs_checked"] = float64(seen)
	r.Extra["writer_errors"] = cl.errs
	r.Extra["violations"] = violations
	r.Extra["lane_records"] = float64(len(all))
	r.LoadEnd = Load()
	if spec.Out != "" {
		if _, err := WriteRun(spec.Out, r); err != nil {
			return r, err
		}
	}
	if len(violations) > 0 {
		return r, fmt.Errorf("round %d: %d violations, first: %s", spec.Round, len(violations), violations[0])
	}
	return r, nil
}

// CrashLoopSpec drives rounds of kill -9 and verification on one store.
type CrashLoopSpec struct {
	Arm, Scenario, Store, Work, Out string
	Rounds                          int
	Batch                           int
	SupersedeCalls                  int // calls ingested per supersede round before the supersede
	MaxKillDelay                    time.Duration
	// AfterKill runs between the kill and the verifier: the LazyFS power
	// loss (unsynced bytes dropped).
	AfterKill func() error
	// WriterEnv is added to the writer's environment (the negative
	// control's LD_PRELOAD that turns fsync into a no-op).
	WriterEnv []string
	// Logs is where the ack and follower logs go (default under Work); they
	// must not live on the file system that loses power.
	Logs string
	// StopAtLoss ends the loop at the first violating round (the negative
	// control: the expected loss is seen, and a lost store cannot be
	// written again).
	StopAtLoss bool
	// Source is the rounds' feed source (CrashSpec.Source).
	Source string
}

// CrashLoopResult sums a loop.
type CrashLoopResult struct {
	Rounds, Violating, Killed int
	FirstViolation            string
}

// CrashLoop runs the rounds: writer child, SIGKILL at a random moment after
// it starts writing, verifier child.
func CrashLoop(ctx context.Context, spec CrashLoopSpec, logf func(string, ...any)) (CrashLoopResult, error) {
	var res CrashLoopResult
	if spec.MaxKillDelay <= 0 {
		// After the writer's marker: inside a stream of 1,024-record calls,
		// or inside a supersede of the previous round's 32,768 records.
		spec.MaxKillDelay = 3 * time.Second
		if spec.Scenario == ScenarioSupersede {
			spec.MaxKillDelay = time.Second
		}
	}
	logs := spec.Logs
	if logs == "" {
		logs = filepath.Join(spec.Work, "crash-logs-"+spec.Arm+"-"+spec.Scenario)
	}
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return res, err
	}
	// Every child opens a store; none may miss an AOT artifact or compile
	// one while it can be killed (the LazyFS container starts with none).
	if err := PrewarmAOT(); err != nil {
		return res, fmt.Errorf("harness: %w", err)
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	mode := ModeCrashIngest
	calls := crashCallsMax
	if spec.Scenario == ScenarioSupersede {
		mode = ModeCrashSupersede
		calls = spec.SupersedeCalls
		if calls <= 0 {
			calls = 32 // a GP fetch's size: the previous batch it retires
		}
	}
	for round := 1; round <= spec.Rounds; round++ {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		cs := CrashSpec{Arm: spec.Arm, Scenario: spec.Scenario, Store: spec.Store, Logs: logs, Out: spec.Out, Round: round,
			Calls: calls, Batch: spec.Batch, Source: spec.Source}
		before := countLines(ackPath(logs))
		writerLog := filepath.Join(logs, fmt.Sprintf("writer-%04d.log", round))
		cmd, log, err := StartChild(ChildSpec{Mode: mode, Crash: &cs}, writerLog, spec.WriterEnv...)
		if err != nil {
			return res, err
		}
		exited := make(chan struct{})
		go func() { _ = cmd.Wait(); close(exited) }()
		// Wait for the writer to start writing (its START line), then kill
		// at a random point inside the writes (or the supersede).
		deadline := time.Now().Add(5 * time.Minute)
		marker := "START"
		if spec.Scenario == ScenarioSupersede {
			marker = "SUPSTART"
		}
		for time.Now().Before(deadline) && !hasLineAfter(ackPath(logs), before, marker, round) {
			select {
			case <-exited:
				deadline = time.Now()
			case <-time.After(20 * time.Millisecond):
			}
		}
		killed := false
		select {
		case <-exited:
		case <-time.After(time.Duration(rng.Int63n(int64(spec.MaxKillDelay)))):
			_ = cmd.Process.Kill() // SIGKILL
			<-exited
			killed = true
			res.Killed++
		}
		log.Close()
		if !killed && !cmd.ProcessState.Success() {
			// The writer failed on its own (it could not open the store, or a
			// write failed): nothing was crashed, so this round tests nothing.
			// A harness error stops the loop; it is never a store violation.
			return res, fmt.Errorf("harness: round %d: the writer exited %d before the kill (log %s)", round, cmd.ProcessState.ExitCode(), writerLog)
		}
		if spec.AfterKill != nil {
			if err := spec.AfterKill(); err != nil {
				return res, fmt.Errorf("round %d: after the kill: %w", round, err)
			}
		}
		vr, verr := RunChild(ctx, ChildSpec{Mode: ModeCrashVerify, Crash: &cs}, filepath.Join(logs, fmt.Sprintf("verify-%04d.log", round)))
		res.Rounds++
		if verr != nil {
			res.Violating++
			if res.FirstViolation == "" {
				res.FirstViolation = fmt.Sprintf("round %d: %v", round, verr)
			}
		}
		logf("crash %s %s round %d: killed=%v verify exit %d (%s)", spec.Arm, spec.Scenario, round, killed, vr.ExitCode, vr.Wall.Round(time.Millisecond))
		if verr != nil && spec.StopAtLoss {
			logf("crash %s %s: loss seen in round %d, the loop ends (negative control)", spec.Arm, spec.Scenario, round)
			break
		}
	}
	return res, nil
}

func floorPath(logs string) string { return filepath.Join(logs, "floor.log") }

// readFloor is the highest seq any earlier verifier saw.
func readFloor(logs string) int64 {
	b, err := os.ReadFile(floorPath(logs))
	if err != nil {
		return 0
	}
	var top int64
	for _, f := range strings.Fields(string(b)) {
		if v, err := strconv.ParseInt(f, 10, 64); err == nil && v > top {
			top = v
		}
	}
	return top
}

func writeFloor(logs string, seq int64) {
	if l, err := openSyncLog(floorPath(logs)); err == nil {
		_ = l.line(strconv.FormatInt(seq, 10))
		l.close()
	}
}

func countLines(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

// hasLineAfter reports whether path has, past its first `skip` lines, a line
// "<marker> <round>".
func hasLineAfter(path string, skip int, marker string, round int) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lines := strings.Split(string(b), "\n")
	want := fmt.Sprintf("%s %d", marker, round)
	for i := skip; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], want) {
			return true
		}
	}
	return false
}
