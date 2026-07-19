package sdnodresults

import (
	"sort"
	"strconv"
	"strings"
	"time"

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

// storeRow is one (cid, data) pair read from an arena table.
type storeRow struct {
	CID  string
	Data []byte
}

func queryRange(store *flowrt.LinkedStore, table string, rng flowrt.TableRange) []storeRow {
	if store == nil || rng.Through <= rng.After {
		return nil
	}
	res, err := store.Query("SELECT cid, data FROM "+table+" WHERE rowid > ? AND rowid <= ? ORDER BY rowid", rng.After, rng.Through)
	if err != nil {
		return nil
	}
	out := make([]storeRow, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 2 {
			continue
		}
		cid, _ := row[0].(string)
		data, _ := row[1].([]byte)
		if cid == "" || len(data) == 0 {
			continue
		}
		out = append(out, storeRow{CID: cid, Data: data})
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

// avgWRMS decodes every $OBD row in rng and averages its WRMS (0,0 when
// there are none or none decode).
func avgWRMS(store *flowrt.LinkedStore, rng flowrt.TableRange) (avg float64, n int) {
	var sum float64
	for _, row := range queryRange(store, "sds_obd", rng) {
		facts, ok := decodeOBD(row.Data)
		if !ok {
			continue
		}
		sum += facts.WRMS
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

// RunProviders is LEVEL 1 of the drill-down: one row per DECLARED provider
// (real $PLG topology metadata), stats honestly Unavailable — see the
// package doc for exactly why per-provider attribution does not exist yet.
func (r *Reader) RunProviders(id string) ([]ProviderStat, bool) {
	sf := r.serviceFlow()
	if sf == nil {
		return nil, false
	}
	rr, ok := r.findRun(id)
	if !ok {
		return nil, false
	}
	out := make([]ProviderStat, 0, len(rr.providers))
	for _, p := range rr.providers {
		out = append(out, ProviderStat{
			Provider:    p,
			Label:       providerLabel(p),
			Unavailable: true,
			Note:        noProviderAttributionNote,
		})
	}
	return out, true
}

// RunObjects is LEVEL 2 of the drill-down: real per-object rows for run id,
// decoded from $OMM (joined to $OBD by NORAD for fit telemetry), filtered by
// a plaintext search over norad/object name/object id. provider/source are
// accepted for API shape stability (the board's owner-specced "filterable by
// data source" control) but currently narrow nothing — every row is
// Unattributed=true (see the package doc) — so passing either simply returns
// the SAME full set; the caller should surface that honestly rather than
// implying a working filter.
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
			Unattributed: true,
			Epoch:        facts.Epoch,
			MeanMotion:   facts.MeanMotion,
			Eccentricity: facts.Eccentricity,
			Inclination:  facts.Inclination,
			OMMCid:       row.CID,
		}
		if obdRow, ok := obdByNorad[facts.Norad]; ok {
			bf := obdFactsByNorad[facts.Norad]
			obj.RMS, obj.HasRMS = bf.WRMS, true
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
