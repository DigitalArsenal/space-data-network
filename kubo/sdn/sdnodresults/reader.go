package sdnodresults

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/flowrt"
)

// backfillRunID is the one synthesized pseudo-run's id: rows a PRIOR process
// already stored, surfaced once per fresh process (empty FireHistory) so a
// completed drain is never invisible just because the node restarted. See
// the package doc and flowrt.BackfillRange.
const backfillRunID = "backfill"

// ODFlow is the seam this package reads through — every method
// *flowrt.ServiceFlow already exposes for this purpose. A narrow interface
// (not the concrete type) so a test can fake it over a real LinkedStore
// without a full wasm mount; *flowrt.ServiceFlow satisfies it as-is.
type ODFlow interface {
	Store() *flowrt.LinkedStore
	SourceProviderPluginIDs() []string
	FireHistory() []flowrt.FireRecord
	OngoingFire() (flowrt.FireRecord, bool)
}

// Reader is the supplemental-OMM board's real run-log/drill-down/download
// source, backed by the mounted OD ServiceFlow. flow is resolved per call
// (like every other sdnapi Deps accessor) so the reader works before the
// runtime is up and reflects the CURRENT mount — nil renders empty/unknown,
// never a crash or a fabricated row.
type Reader struct {
	flow func() ODFlow
}

// NewReader builds a Reader over flow (resolved lazily; a nil return is
// treated as "no OD flow mounted").
func NewReader(flow func() ODFlow) *Reader {
	return &Reader{flow: flow}
}

func (r *Reader) serviceFlow() ODFlow {
	if r == nil || r.flow == nil {
		return nil
	}
	return r.flow()
}

// storeRow is one arena row's provenance + payload. Provider/Source/PulledAt
// are all wrapper-schema columns declared in the SAME flowrt.flatsqlStoreSchema
// this binary compiles against (pulled_at is additive there — an OLDER row
// simply reads 0, never a query error — see that schema's own doc comment),
// so there is exactly one column set to read: this binary's LinkedStore
// schema and this package's queries are compiled together, never skewed.
type storeRow struct {
	CID      string
	Provider string
	Source   string
	PulledAt int64 // unix milliseconds; 0 when absent/unknown (an older row)
	Data     []byte
}

const (
	// One board summary may copy at most the same 256 MiB budget FlatSQL uses
	// for its bounded raw-stream mirror. Current retained OD runs are far below
	// this; the cap prevents a damaged/hostile arena from monopolizing memory.
	maxSummaryRecordStreamBytes = 256 * 1024 * 1024
	// Stay below the board's four-second poll cadence so a pathological query
	// cannot create an ever-growing convoy while it owns LinkedStore.mu.
	summaryRecordStreamTimeout = 3 * time.Second
)

// queryRange reads every row in rng from table, provenance + pulled_at
// included, in rowid order.
func queryRange(store *flowrt.LinkedStore, table string, rng flowrt.TableRange) []storeRow {
	if store == nil || rng.Through <= rng.After {
		return nil
	}
	res, err := store.Query(
		"SELECT cid, provider, source_name, pulled_at, data FROM "+table+" WHERE rowid > ? AND rowid <= ? ORDER BY rowid",
		rng.After, rng.Through,
	)
	if err != nil {
		return nil
	}
	out := make([]storeRow, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 5 {
			continue
		}
		sr := storeRow{}
		sr.CID, _ = row[0].(string)
		sr.Provider, _ = row[1].(string)
		sr.Source, _ = row[2].(string)
		sr.PulledAt = asInt64(row[3])
		data, _ := row[4].([]byte)
		sr.Data = data
		if sr.CID == "" || len(sr.Data) == 0 {
			continue
		}
		out = append(out, sr)
	}
	return out
}

// queryDataRange bulk-reads one table's record payloads as a single FlatSQL
// response artifact. This is the summary/live path: it needs only the `data`
// BLOB, so materializing cid/provider/source/pulled_at as separate cells would
// add tens of thousands of unnecessary WasmEdge calls for a full catalog.
func queryDataRange(store *flowrt.LinkedStore, table string, rng flowrt.TableRange) [][]byte {
	if store == nil || rng.Through <= rng.After {
		return nil
	}
	rowSpan := rng.Through - rng.After
	stream, err := store.QueryRecordStream(
		"SELECT data FROM "+table+" WHERE rowid > ? AND rowid <= ? AND cid != '' AND data IS NOT NULL AND length(data) > 0 ORDER BY rowid",
		flatsqlrt.SandboxCaps{
			MaxRows:  uint64(rowSpan),
			MaxBytes: maxSummaryRecordStreamBytes,
			Timeout:  summaryRecordStreamTimeout,
		},
		rng.After, rng.Through,
	)
	if err != nil || stream == nil || stream.Columns != 1 {
		return nil
	}
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil || len(frames) != stream.Rows || stream.FrameCount != len(frames) {
		return nil
	}
	return frames
}

// asInt coerces a flatsqlrt scalar cell (int64 or int, depending on the
// engine's marshaling path) into a plain int.
func asInt(v interface{}) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

// asInt64 is asInt's int64 counterpart (for the pulled_at column).
func asInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

// formatPulledAt renders a pulled_at cell (unix milliseconds — the OD flow's
// store node fills it from a [u64le unix_ms] host-trigger timestamp) as an
// RFC3339 UTC string, or "" for an absent/non-positive value.
func formatPulledAt(unixMs int64) string {
	if unixMs <= 0 {
		return ""
	}
	return time.UnixMilli(unixMs).UTC().Format(time.RFC3339)
}

// unattributedProvider is the synthesized bucket id for records with no
// provider tag (e.g. the pre-attribution backfill rows) — never a real
// declared provider id, so it can never collide with one.
const unattributedProvider = ""

// providerAggregates GROUP BYs table's rows in rng by provider, returning
// each provider's row count and (when the store carries the column) its
// latest pulled_at, in unix milliseconds. Cheap SQL-side aggregation — no
// BLOB decode — since totals/last-pulled need only the provenance columns.
// The "" key (present only when SOME row in range has no provider tag) is
// the unattributed bucket.
func providerAggregates(store *flowrt.LinkedStore, table string, rng flowrt.TableRange) (totals map[string]int, lastPulled map[string]int64) {
	totals = map[string]int{}
	lastPulled = map[string]int64{}
	if store == nil || rng.Through <= rng.After {
		return
	}
	res, err := store.Query(
		"SELECT provider, COUNT(*), COALESCE(MAX(pulled_at),0) FROM "+table+" WHERE rowid > ? AND rowid <= ? GROUP BY provider",
		rng.After, rng.Through,
	)
	if err != nil {
		return
	}
	for _, row := range res.Rows {
		if len(row) != 3 {
			continue
		}
		provider, _ := row[0].(string)
		totals[provider] = asInt(row[1])
		if ts := asInt64(row[2]); ts > 0 {
			lastPulled[provider] = ts
		}
	}
	return
}

// providerAvgWRMS decodes every $OBD row in rng and averages effectiveWRMS
// (WRMS, or BEST_PASS_WRMS when WRMS is unset) PER PROVIDER — the module-
// side ruling is "no wrms store column, read-side BLOB decode only," so this
// is necessarily a per-row decode (SQL cannot aggregate a value buried in a
// BLOB), grouped by the (SQL-column, always-present) provider field.
func providerAvgWRMS(store *flowrt.LinkedStore, rng flowrt.TableRange) map[string]float64 {
	sums := map[string]float64{}
	counts := map[string]int{}
	for _, row := range queryRange(store, "sds_obd", rng) {
		facts, ok := decodeOBD(row.Data)
		if !ok {
			continue
		}
		wrms, ok := facts.effectiveWRMS()
		if !ok {
			continue
		}
		sums[row.Provider] += wrms
		counts[row.Provider]++
	}
	out := make(map[string]float64, len(counts))
	for p, n := range counts {
		if n > 0 {
			out[p] = sums[p] / float64(n)
		}
	}
	return out
}

// providerShortID maps a declared provider's plugin id (com.orbpro.<x>-source)
// to the board's short provider id (<x>) — mirrors
// plugin/plugins/sdnapi/omm_compat.go's ommCompatProviderShortID exactly (a
// mechanical prefix/suffix strip kept duplicated here so this package has no
// dependency on the sdnapi plugin).
func providerShortID(pluginID string) string {
	id := strings.TrimPrefix(pluginID, "com.orbpro.")
	id = strings.TrimSuffix(id, "-source")
	return id
}

// declaredProviders returns sf's declared provider set, short-id form,
// sorted + deduplicated.
func declaredProviders(sf ODFlow) []string {
	ids := sf.SourceProviderPluginIDs()
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		short := providerShortID(id)
		if short == "" || seen[short] {
			continue
		}
		seen[short] = true
		out = append(out, short)
	}
	sort.Strings(out)
	return out
}

// avgWRMS decodes every $OBD row in rng and averages its effectiveWRMS (WRMS,
// or BEST_PASS_WRMS when WRMS is unset) — (0,0) when there are none or none
// decode.
func avgWRMS(store *flowrt.LinkedStore, rng flowrt.TableRange) (avg float64, n int) {
	var sum float64
	for _, data := range queryDataRange(store, "sds_obd", rng) {
		facts, ok := decodeOBD(data)
		if !ok {
			continue
		}
		wrms, ok := facts.effectiveWRMS()
		if !ok {
			continue
		}
		sum += wrms
		n++
	}
	if n == 0 {
		return 0, 0
	}
	return sum / float64(n), n
}

// resolvedRun is one run's ranges + display metadata, the shared basis for
// RunSummary/RunProviders/RunObjects so all three agree on exactly the same
// set of rows.
type resolvedRun struct {
	id            string
	started       *time.Time
	finished      *time.Time
	status        string
	fireErr       string
	note          string
	providers     []string
	omm, ocm, obd flowrt.TableRange
}

// findRun resolves id to its ranges: a FireHistory entry, the currently
// OngoingFire, or the one synthesized backfill pseudo-run. ok is false for
// an unknown id or when the OD flow is not mounted.
func (r *Reader) findRun(id string) (resolvedRun, bool) {
	sf := r.serviceFlow()
	if sf == nil {
		return resolvedRun{}, false
	}
	providers := declaredProviders(sf)

	if id == backfillRunID {
		store := sf.Store()
		if len(sf.FireHistory()) != 0 {
			return resolvedRun{}, false // backfill only exists while history is empty
		}
		omm := flowrt.BackfillRange(store, "sds_omm")
		if flowrt.CountInRange(store, "sds_omm", omm) == 0 {
			return resolvedRun{}, false // nothing to backfill — honest empty, no row at all
		}
		return resolvedRun{
			id:        backfillRunID,
			status:    "completed",
			note:      "pre-existing store contents observed at this node process's boot (exact run time unknown — the fire log starts empty on every restart; the stored records themselves persist across restarts independently)",
			providers: providers,
			omm:       omm,
			ocm:       flowrt.BackfillRange(store, "sds_ocm"),
			obd:       flowrt.BackfillRange(store, "sds_obd"),
		}, true
	}

	if ong, ok := sf.OngoingFire(); ok && ong.ID == id {
		started := ong.StartedAt
		store := sf.Store()
		return resolvedRun{
			id: ong.ID, started: &started, status: "ongoing", providers: providers,
			omm: flowrt.TableRange{After: ong.OMM.After, Through: flowrt.MaxRowid(store, "sds_omm")},
			ocm: flowrt.TableRange{After: ong.OCM.After, Through: flowrt.MaxRowid(store, "sds_ocm")},
			obd: flowrt.TableRange{After: ong.OBD.After, Through: flowrt.MaxRowid(store, "sds_obd")},
		}, true
	}

	for _, rec := range sf.FireHistory() {
		if rec.ID != id {
			continue
		}
		started, finished := rec.StartedAt, rec.FinishedAt
		status := "completed"
		if rec.Status == "error" {
			status = "failed"
		}
		return resolvedRun{
			id: rec.ID, started: &started, finished: &finished, status: status,
			fireErr: rec.Error, providers: providers,
			omm: rec.OMM, ocm: rec.OCM, obd: rec.OBD,
		}, true
	}
	return resolvedRun{}, false
}

func (rr resolvedRun) summary(store *flowrt.LinkedStore) RunSummary {
	avg, n := avgWRMS(store, rr.obd)
	return RunSummary{
		ID:             rr.id,
		Started:        rr.started,
		Finished:       rr.finished,
		Status:         rr.status,
		Providers:      rr.providers,
		ObjectsTotal:   flowrt.CountInRange(store, "sds_omm", rr.omm),
		ObjectsDone:    n,
		EphemerisFiles: flowrt.CountInRange(store, "sds_omm", rr.omm),
		AvgRMS:         avg,
		BeatCount:      nil, // always unavailable — see RunSummary's doc
		Error:          rr.fireErr,
		Note:           rr.note,
	}
}

// Runs lists every run the board's run log renders: every FireHistory entry
// (newest last), or — while FireHistory is empty — the one synthesized
// backfill pseudo-run, exactly like a fresh process finding a prior process's
// completed full-catalog drain already sitting in the store. Never both: a
// process that has fired at least once always prefers its OWN observed
// history over the one-time backfill.
func (r *Reader) Runs() []RunSummary {
	sf := r.serviceFlow()
	if sf == nil {
		return nil
	}
	store := sf.Store()
	hist := sf.FireHistory()
	if len(hist) == 0 {
		if rr, ok := r.findRun(backfillRunID); ok {
			return []RunSummary{rr.summary(store)}
		}
		return nil
	}
	out := make([]RunSummary, 0, len(hist))
	for _, rec := range hist {
		rr, ok := r.findRun(rec.ID)
		if !ok {
			continue
		}
		out = append(out, rr.summary(store))
	}
	return out
}

// Live returns the currently-executing fire's LiveRun snapshot, or
// (LiveRun{}, false) when idle.
func (r *Reader) Live() (LiveRun, bool) {
	sf := r.serviceFlow()
	if sf == nil {
		return LiveRun{}, false
	}
	ong, ok := sf.OngoingFire()
	if !ok {
		return LiveRun{}, false
	}
	store := sf.Store()
	avg, n := avgWRMS(store, flowrt.TableRange{After: ong.OBD.After, Through: flowrt.MaxRowid(store, "sds_obd")})
	return LiveRun{
		ID:             ong.ID,
		Started:        ong.StartedAt,
		Providers:      declaredProviders(sf),
		ObjectsDone:    n,
		CurrentAvgRMS:  avg,
		ElapsedSeconds: time.Since(ong.StartedAt).Seconds(),
	}, true
}

// Run resolves one run's single-row detail stats (the drill-down's stats
// table above the breadcrumbed content).
func (r *Reader) Run(id string) (RunSummary, bool) {
	sf := r.serviceFlow()
	if sf == nil {
		return RunSummary{}, false
	}
	rr, ok := r.findRun(id)
	if !ok {
		return RunSummary{}, false
	}
	return rr.summary(sf.Store()), true
}

// RunProviders is LEVEL 1 of the drill-down: real per-provider totals/avg-RMS/
// last-pulled where the store's provider attribution covers this run (the
// module's cid-keyed provenance sidecar — see the package doc), backward-
// compatible with runs that predate it: when NO row in range carries a
// provider tag (the backfill run's pre-attribution rows), every declared
// provider is honestly Unavailable (nil, never a fabricated 0) and a single
// synthesized "unattributed" row carries the real total for those records.
// skipped/errors have no telemetry at any layer yet, so they stay nil with a
// note regardless of attribution.
func (r *Reader) RunProviders(id string) ([]ProviderStat, bool) {
	sf := r.serviceFlow()
	if sf == nil {
		return nil, false
	}
	rr, ok := r.findRun(id)
	if !ok {
		return nil, false
	}
	store := sf.Store()

	totals, lastPulled := providerAggregates(store, "sds_omm", rr.omm)
	avgRMS := providerAvgWRMS(store, rr.obd)

	// hasAttribution: at least one row in this run's OMM range carries a real
	// (non-empty) provider tag. Only then can a DECLARED provider absent from
	// totals be honestly reported as a real zero (it fired in a run we CAN
	// attribute, it just contributed nothing) rather than "unknown."
	hasAttribution := false
	for p := range totals {
		if p != unattributedProvider {
			hasAttribution = true
			break
		}
	}

	order := append([]string(nil), rr.providers...)
	seen := make(map[string]bool, len(order))
	for _, p := range order {
		seen[p] = true
	}
	for p := range totals {
		if p != unattributedProvider && !seen[p] {
			seen[p] = true
			order = append(order, p)
		}
	}
	sort.Strings(order)

	out := make([]ProviderStat, 0, len(order)+1)
	for _, p := range order {
		stat := ProviderStat{Provider: p, Label: providerLabel(p)}
		if total, ok := totals[p]; ok {
			t := total
			stat.Total = &t
		} else if hasAttribution {
			zero := 0
			stat.Total = &zero
		}
		if avg, ok := avgRMS[p]; ok {
			a := avg
			stat.AvgRMS = &a
		}
		if ts, ok := lastPulled[p]; ok {
			s := formatPulledAt(ts)
			if s != "" {
				stat.LastPulled = &s
			}
		}
		if stat.Total == nil {
			stat.Unavailable = true
			stat.Note = "this run predates per-provider attribution (or predates this node's rebuild) — total/avg RMS/last-pulled cannot be determined for this provider"
		} else {
			stat.Note = "skipped/errors have no per-provider telemetry yet — the OD module does not emit per-provider skip/error counts"
		}
		out = append(out, stat)
	}

	// The unattributed bucket: real records with no provider tag (pre-
	// attribution rows). Never vanished — surfaced as its own row.
	if total, ok := totals[unattributedProvider]; ok && total > 0 {
		stat := ProviderStat{
			Provider: "unattributed",
			Label:    "Unattributed (pre-attribution records)",
		}
		t := total
		stat.Total = &t
		if avg, ok := avgRMS[unattributedProvider]; ok {
			a := avg
			stat.AvgRMS = &a
		}
		stat.Note = "these records predate per-provider attribution (the OD flow's provenance sidecar) and cannot be attributed to a specific provider"
		out = append(out, stat)
	}

	return out, true
}

// RunObjects is LEVEL 2 of the drill-down: real per-object rows for run id,
// decoded from $OMM (joined to $OBD by NORAD for fit telemetry), filtered by
// a plaintext search over norad/object name/object id. Provider/Source now
// come from the store's real provenance columns (the module's cid-keyed
// sidecar) when present; Unattributed is true only for a record that
// genuinely carries no provider tag (pre-attribution rows), never a blanket
// default. provider/source query params are accepted for API shape
// stability (the board's "filterable by data source" control) but do not
// yet narrow the result — every returned row already carries its REAL
// provider/source (or Unattributed=true), so the caller can filter/label
// client-side; a future server-side WHERE is additive, not a shape change.
func (r *Reader) RunObjects(id, search string) ([]ObjectRow, bool) {
	sf := r.serviceFlow()
	if sf == nil {
		return nil, false
	}
	rr, ok := r.findRun(id)
	if !ok {
		return nil, false
	}
	store := sf.Store()
	obdByNorad := make(map[uint32]storeRow, 0)
	obdFactsByNorad := make(map[uint32]obdFacts)
	for _, row := range queryRange(store, "sds_obd", rr.obd) {
		facts, ok := decodeOBD(row.Data)
		if !ok {
			continue
		}
		obdByNorad[facts.SatNo] = row
		obdFactsByNorad[facts.SatNo] = facts
	}

	needle := strings.ToLower(strings.TrimSpace(search))
	out := make([]ObjectRow, 0)
	for _, row := range queryRange(store, "sds_omm", rr.omm) {
		facts, ok := decodeOMM(row.Data)
		if !ok {
			continue
		}
		obj := ObjectRow{
			Norad:        facts.Norad,
			ObjectName:   facts.ObjectName,
			ObjectID:     facts.ObjectID,
			Provider:     row.Provider,
			Source:       row.Source,
			Unattributed: row.Provider == "" && row.Source == "",
			Epoch:        facts.Epoch,
			MeanMotion:   facts.MeanMotion,
			Eccentricity: facts.Eccentricity,
			Inclination:  facts.Inclination,
			OMMCid:       row.CID,
		}
		if obdRow, ok := obdByNorad[facts.Norad]; ok {
			bf := obdFactsByNorad[facts.Norad]
			if wrms, ok := bf.effectiveWRMS(); ok {
				obj.RMS, obj.HasRMS = wrms, true
			}
			obj.Iterations = bf.Iterations
			obj.FitSpanDays = bf.FitSpanDays
			obj.OBDCid = obdRow.CID
		}
		if needle != "" {
			haystack := strings.ToLower(obj.ObjectName + " " + obj.ObjectID + " " + strconv.FormatUint(uint64(obj.Norad), 10))
			if !strings.Contains(haystack, needle) {
				continue
			}
		}
		out = append(out, obj)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Norad < out[j].Norad })
	return out, true
}

// DownloadRecord returns the exact stored bytes for a content-addressed cid
// (checked across sds_omm/sds_ocm/sds_obd) plus which table it came from (a
// filename/content-type hint) — the canonical, byte-for-byte downloadable
// form: this package never re-encodes or re-derives a record for download.
func (r *Reader) DownloadRecord(cid string) (data []byte, table string, ok bool) {
	sf := r.serviceFlow()
	if sf == nil {
		return nil, "", false
	}
	store := sf.Store()
	if store == nil {
		return nil, "", false
	}
	for _, t := range []string{"sds_omm", "sds_ocm", "sds_obd"} {
		res, err := store.Query("SELECT data FROM "+t+" WHERE cid = ? LIMIT 1", cid)
		if err != nil || len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
			continue
		}
		if blob, ok := res.Rows[0][0].([]byte); ok && len(blob) > 0 {
			return blob, t, true
		}
	}
	return nil, "", false
}
