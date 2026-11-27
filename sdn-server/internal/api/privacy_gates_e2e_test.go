package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdsais "github.com/DigitalArsenal/spacedatastandards.org/lib/go/AIS"
	sdspmm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PMM"
	sdsqrp "github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/accessgate"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

type stubFlowDoc struct {
	id      string
	version string
	path    string
	doc     *flowrt.FlowAPIDoc
	private bool
}

func (f stubFlowDoc) ProgramID() string          { return f.id }
func (f stubFlowDoc) FlowVersion() string        { return f.version }
func (f stubFlowDoc) MountPath() string          { return f.path }
func (f stubFlowDoc) APIDoc() *flowrt.FlowAPIDoc { return f.doc }
func (f stubFlowDoc) IsPrivate() bool            { return f.private }

type stubPrivatePlugin struct {
	id      string
	private bool
}

func (p stubPrivatePlugin) ID() string                                                 { return p.id }
func (p stubPrivatePlugin) Start(ctx context.Context, rt plugins.RuntimeContext) error { return nil }
func (p stubPrivatePlugin) RegisterRoutes(mux *http.ServeMux)                          {}
func (p stubPrivatePlugin) Close() error                                               { return nil }
func (p stubPrivatePlugin) IsPrivate() bool                                            { return p.private }

func buildQRPRequestFrame(schema string) []byte {
	b := flatbuffers.NewBuilder(128)
	sOff := b.CreateString(schema)
	sdsqrp.QRPStart(b)
	sdsqrp.QRPAddKIND(b, sdsqrp.EnumValuesqrpKind["Request"])
	sdsqrp.QRPAddSCHEMA_NAME(b, sOff)
	sdsqrp.QRPAddLIMIT(b, 10)
	sdsqrp.FinishSizePrefixedQRPBuffer(b, sdsqrp.QRPEnd(b))
	return b.FinishedBytes()
}

func TestGenericPrivacyGatesHidePrivateFlowsAndSchemas(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(
		t.TempDir(),
		validator,
		storage.WithPrivateSchemas([]string{"AIS.fbs"}),
	)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	defer store.Close()

	// Seed one public record (OMM) and one private-configured record (AIS).
	ommBytes := sds.NewOMMBuilder().
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithNoradCatID(25544).
		WithEpoch("2026-11-27T00:00:00.000Z").
		WithMeanMotion(15.5).
		WithEccentricity(0.0005).
		WithInclination(51.6).
		Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", ommBytes, "peer-test", nil, storage.SourceTags{
		ProviderID: "prov-public",
		SourceName: "public-gp",
		BatchID:    "batch-pub-1",
	}); err != nil {
		t.Fatalf("store OMM: %v", err)
	}
	fb := flatbuffers.NewBuilder(256)
	evtOff := fb.CreateString("evt-priv-1")
	sessOff := fb.CreateString("sess-priv-1")
	ipOff := fb.CreateString("203.0.113.42")
	promptOff := fb.CreateString("show me commercial SAR coverage of Paris")
	sdsais.AISStart(fb)
	sdsais.AISAddEVENT_ID(fb, evtOff)
	sdsais.AISAddSESSION_ID(fb, sessOff)
	sdsais.AISAddEVENT_KIND(fb, sdsais.EnumValuesaisEventKind["QUERY_SUBMITTED"])
	sdsais.AISAddRECEIVED_UNIX_MS(fb, 1795780000000)
	sdsais.AISAddCLIENT_IP(fb, ipOff)
	sdsais.AISAddPROMPT_TEXT(fb, promptOff)
	fb.FinishWithFileIdentifier(sdsais.AISEnd(fb), []byte(sdsais.AISIdentifier))
	aisBytes := fb.FinishedBytes()
	aisCID, err := store.StoreWithSourceTags("AIS.fbs", aisBytes, "peer-test", nil, storage.SourceTags{
		ProviderID: "prov-private",
		SourceName: "private-lane",
		BatchID:    "batch-priv-1",
	})
	if err != nil {
		t.Fatalf("store AIS: %v", err)
	}

	// 1. Storage-level public query surface + sandboxed SQL gates (including single-quoted SQLite table names).
	surface, err := store.PublicQuerySurface()
	if err != nil {
		t.Fatalf("PublicQuerySurface: %v", err)
	}
	for _, rel := range surface {
		base, _, _ := strings.Cut(rel.Name, "@")
		if strings.EqualFold(base, "AIS") {
			t.Fatalf("PublicQuerySurface leaked private schema relation %q", rel.Name)
		}
	}

	caps := flatsqlrt.SandboxCaps{MaxRows: 100, MaxBytes: 1 << 20}
	for _, badSQL := range []string{
		`SELECT _data FROM AIS`,
		`SELECT _data FROM ais`,
		`SELECT _data FROM "AIS"`,
		"SELECT _data FROM `AIS`",
		`SELECT _data FROM [AIS]`,
		`SELECT _data FROM "AIS@private-lane"`,
		`SELECT CLIENT_IP, PROMPT_TEXT FROM 'AIS'`,
		`SELECT CLIENT_IP, PROMPT_TEXT FROM 'ais'`,
		`SELECT CLIENT_IP, PROMPT_TEXT FROM main.'AIS'`,
		`SELECT CLIENT_IP, PROMPT_TEXT FROM "main".'AIS'`,
		`SELECT CLIENT_IP, PROMPT_TEXT FROM 'AIS@private-lane'`,
		`SELECT OMM.OBJECT_NAME FROM OMM, 'AIS'`,
		`SELECT OMM.OBJECT_NAME FROM OMM JOIN 'AIS'`,
		`SELECT t.CLIENT_IP, t.PROMPT_TEXT FROM (SELECT 1) AS z, 'AIS' AS t`,
		`SELECT t.CLIENT_IP, t.PROMPT_TEXT FROM OMM o JOIN OMM p ON 1=1, 'AIS' t`,
		`SELECT t.CLIENT_IP, t.PROMPT_TEXT FROM OMM o JOIN OMM p USING (NORAD_CAT_ID), 'AIS' t`,
		`SELECT t.CLIENT_IP, t.PROMPT_TEXT FROM (SELECT 1) AS z, 'ais' AS t`,
		`SELECT t.CLIENT_IP, t.PROMPT_TEXT FROM (SELECT 1) AS z, 'AIS@private-lane' AS t`,
		`SELECT t.CLIENT_IP, t.PROMPT_TEXT FROM ('AIS' AS t)`,
		`/* probe */ SELECT count(*) FROM AIS`,
		`WITH cte AS (SELECT _data FROM 'ais') SELECT * FROM cte`,
	} {
		if _, err := store.QuerySandboxedStream(badSQL, caps); err == nil {
			t.Fatalf("QuerySandboxedStream(%q) succeeded; want SandboxCodeNotAuthorized", badSQL)
		} else if se, ok := flatsqlrt.AsSandboxError(err); !ok || se.Code != flatsqlrt.SandboxCodeNotAuthorized {
			t.Fatalf("QuerySandboxedStream(%q) err = %v; want SandboxCodeNotAuthorized", badSQL, err)
		}
		if _, _, _, err := store.QuerySandboxedJSON(badSQL, caps); err == nil {
			t.Fatalf("QuerySandboxedJSON(%q) succeeded; want SandboxCodeNotAuthorized", badSQL)
		} else if se, ok := flatsqlrt.AsSandboxError(err); !ok || se.Code != flatsqlrt.SandboxCodeNotAuthorized {
			t.Fatalf("QuerySandboxedJSON(%q) err = %v; want SandboxCodeNotAuthorized", badSQL, err)
		}
		if _, err := store.QuerySandboxedRows(badSQL, caps); err == nil {
			t.Fatalf("QuerySandboxedRows(%q) succeeded; want SandboxCodeNotAuthorized", badSQL)
		} else if se, ok := flatsqlrt.AsSandboxError(err); !ok || se.Code != flatsqlrt.SandboxCodeNotAuthorized {
			t.Fatalf("QuerySandboxedRows(%q) err = %v; want SandboxCodeNotAuthorized", badSQL, err)
		}
	}
	// String literal mentioning the private table name in WHERE must NOT be falsely blocked.
	if _, _, _, err := store.QuerySandboxedJSON(`SELECT OBJECT_NAME FROM OMM WHERE OBJECT_NAME != 'AIS'`, caps); err != nil {
		t.Fatalf("QuerySandboxedJSON with string literal 'AIS' failed: %v", err)
	}
	// Trusted QueryRawStream still reads the private schema for approved internal flows.
	if stream, err := store.QueryRawStream(`SELECT _data FROM AIS`); err != nil {
		t.Fatalf("trusted QueryRawStream(AIS) failed: %v", err)
	} else if stream == nil || stream.Rows < 1 {
		t.Fatalf("trusted QueryRawStream(AIS) rows = %v, want >= 1", stream)
	}

	// 2. HTTP data endpoints: /catalog, /sources, /index, /summary, /local-replica-stats, /records/, /scan, /query.
	cfg := config.Default()
	cfg.Storage.PrivateSchemas = []string{"AIS.fbs"}
	if !cfg.Storage.IsPrivateSchema("ais") || !cfg.Storage.IsPrivateSchema("AIS.fbs") {
		t.Fatal("cfg.Storage.IsPrivateSchema failed to normalize AIS")
	}
	catHandler := NewCatalogHandler(store, "", cfg)
	catHandler.SetCacheTTL(0)
	catRec := httptest.NewRecorder()
	catHandler.handleCatalog(catRec, httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil))
	if catRec.Code != http.StatusOK {
		t.Fatalf("/api/v1/catalog status = %d: %s", catRec.Code, catRec.Body.String())
	}
	if strings.Contains(catRec.Body.String(), "\"AIS\"") || strings.Contains(catRec.Body.String(), "AIS.fbs") {
		t.Fatalf("/api/v1/catalog leaked private schema AIS: %s", catRec.Body.String())
	}

	dq := NewDataQueryHandler(store)
	srcRec := httptest.NewRecorder()
	dq.handleResolvedSources(srcRec, httptest.NewRequest(http.MethodGet, "/api/v1/data/sources", nil))
	if srcRec.Code != http.StatusOK {
		t.Fatalf("/api/v1/data/sources status = %d", srcRec.Code)
	}
	if strings.Contains(srcRec.Body.String(), "AIS.fbs") || strings.Contains(srcRec.Body.String(), "private-lane") {
		t.Fatalf("/api/v1/data/sources leaked private schema/lane")
	}

	sumRec := httptest.NewRecorder()
	dq.handleSummary(sumRec, httptest.NewRequest(http.MethodGet, "/api/v1/data/summary", nil))
	if sumRec.Code != http.StatusOK {
		t.Fatalf("/api/v1/data/summary status = %d", sumRec.Code)
	}
	if strings.Contains(sumRec.Body.String(), "AIS.fbs") || strings.Contains(sumRec.Body.String(), "private-lane") {
		t.Fatalf("/api/v1/data/summary leaked private schema/lane")
	}

	repRec := httptest.NewRecorder()
	dq.handleLocalReplicaStats(repRec, httptest.NewRequest(http.MethodGet, "/api/v1/data/local-replica-stats?schema=AIS.fbs", nil))
	if repRec.Code != http.StatusOK {
		t.Fatalf("/api/v1/data/local-replica-stats status = %d", repRec.Code)
	}
	if strings.Contains(repRec.Body.String(), "AIS.fbs") {
		t.Fatalf("/api/v1/data/local-replica-stats leaked AIS.fbs")
	}

	idxRec := httptest.NewRecorder()
	dq.handleRecordIndex(idxRec, httptest.NewRequest(http.MethodGet, "/api/v1/data/index?schema=AIS.fbs", nil))
	if idxRec.Code != http.StatusNotFound {
		t.Fatalf("/api/v1/data/index?schema=AIS.fbs status = %d, want 404", idxRec.Code)
	}

	rawRec := httptest.NewRecorder()
	dq.handleRawRecord(rawRec, httptest.NewRequest(http.MethodGet, "/api/v1/data/records/AIS.fbs/"+aisCID, nil))
	if rawRec.Code != http.StatusNotFound {
		t.Fatalf("/api/v1/data/records/AIS.fbs/%s status = %d, want 404", aisCID, rawRec.Code)
	}

	qrpBody := buildQRPRequestFrame("AIS.fbs")
	scanReq := httptest.NewRequest(http.MethodPost, "/api/v1/data/scan", bytes.NewReader(qrpBody))
	scanReq.Header.Set("Content-Type", StreamContentType)
	scanRec := httptest.NewRecorder()
	dq.handleScan(scanRec, scanReq)
	if scanRec.Code != http.StatusNotFound {
		t.Fatalf("/api/v1/data/scan(AIS.fbs) status = %d, want 404", scanRec.Code)
	}

	queryReq := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewReader(qrpBody))
	queryReq.Header.Set("Content-Type", StreamContentType)
	queryRec := httptest.NewRecorder()
	dq.handleDataQuery(queryRec, queryReq)
	if queryRec.Code != http.StatusNotFound {
		t.Fatalf("/api/v1/data/query(AIS.fbs) status = %d, want 404", queryRec.Code)
	}

	// 3. Real config.FlowMount{Private: true} -> flowrt.RegisterLazyFlowMounts -> GenerateOpenAPI.
	mux := http.NewServeMux()
	mounted, err := flowrt.RegisterLazyFlowMounts(mux, []config.FlowMount{
		{Path: "/api/v1/collect/ai-sessions/", Flow: "/nonexistent/private-flow.json", Private: true},
	}, flowrt.FlowMountDeps{})
	if err != nil {
		t.Fatalf("RegisterLazyFlowMounts: %v", err)
	}
	if len(mounted) != 1 || !mounted[0].IsPrivate() {
		t.Fatalf("mounted flow Private = false, want true")
	}

	specBytes, err := GenerateOpenAPI(DocsHandlerOptions{
		Version: "test",
		Flows: []FlowDocSource{
			stubFlowDoc{
				id: "com.digitalarsenal.flows.public-demo", path: "/api/v1/public-demo",
				doc: &flowrt.FlowAPIDoc{Tag: "public-demo", Routes: []flowrt.FlowAPIRoute{{Method: "GET", Path: "", Summary: "Public"}}},
			},
			mounted[0],
		},
	})
	if err != nil {
		t.Fatalf("GenerateOpenAPI: %v", err)
	}
	spec := string(specBytes)
	if !strings.Contains(spec, "/api/v1/public-demo") {
		t.Fatalf("OpenAPI missing public flow")
	}
	if strings.Contains(spec, "ai-sessions") {
		t.Fatalf("OpenAPI leaked private mounted flow: %s", spec)
	}

	// 4. PluginManager snapshot vs /api/v1/modules vs /api/apps.
	pm := plugins.New()
	if err := pm.Register(stubPrivatePlugin{id: "com.digitalarsenal.flows.public-svc", private: false}); err != nil {
		t.Fatalf("register public plugin: %v", err)
	}
	if err := pm.Register(stubPrivatePlugin{id: "com.digitalarsenal.flows.private-backup", private: true}); err != nil {
		t.Fatalf("register private plugin: %v", err)
	}
	snap := pm.RuntimeSnapshot()
	foundPrivAdmin := false
	for _, m := range snap.Modules {
		if m.ID == "com.digitalarsenal.flows.private-backup" && m.Private {
			foundPrivAdmin = true
		}
	}
	if !foundPrivAdmin {
		t.Fatalf("Admin RuntimeSnapshot missing private service with Private=true")
	}
	for _, entry := range pm.Manifest() {
		if entry.ID == "com.digitalarsenal.flows.private-backup" {
			t.Fatalf("PluginManager.Manifests() leaked private service")
		}
	}
	pmmFrame := BuildInstalledModulesFrame(snap, "sdn.spaceaware.io")
	pmmRoot := sdspmm.GetSizePrefixedRootAsPMM(pmmFrame, 0)
	var modEntry sdspmm.PMMModuleEntry
	for i := 0; i < pmmRoot.MODULESLength(); i++ {
		if pmmRoot.MODULES(&modEntry, i) && string(modEntry.MODULE_ID()) == "com.digitalarsenal.flows.private-backup" {
			t.Fatalf("/api/v1/modules leaked private service")
		}
	}
	appsHandler := NewAppsHandler(
		pm.RuntimeSnapshot,
		func() ([]sourcemetrics.Source, error) {
			return []sourcemetrics.Source{
				{SourceID: "pub/gp", AppID: "com.digitalarsenal.flows.public-svc", ProviderID: "pub", SourceName: "gp", LastSchemas: []string{"OMM.fbs"}},
				{SourceID: "priv/backup", AppID: "com.digitalarsenal.flows.private-backup", ProviderID: "priv", SourceName: "backup", LastSchemas: []string{"AIS.fbs"}},
			}, nil
		},
		store,
		nil,
	)
	appsRec := httptest.NewRecorder()
	appsHandler.handleApps(appsRec, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	if appsRec.Code != http.StatusOK {
		t.Fatalf("/api/apps status = %d", appsRec.Code)
	}
	var appsPayload appsResponse
	if err := json.Unmarshal(appsRec.Body.Bytes(), &appsPayload); err != nil {
		t.Fatalf("unmarshal /api/apps: %v", err)
	}
	for _, app := range appsPayload.Apps {
		if app.ID == "com.digitalarsenal.flows.private-backup" {
			t.Fatalf("/api/apps leaked private app")
		}
	}
	for _, src := range appsPayload.Sources {
		if src.SourceID == "priv/backup" || src.SchemaName == "AIS.fbs" {
			t.Fatalf("/api/apps leaked private source: %+v", src)
		}
	}

	// 5. Method-scoped write-only public_paths rule admits POST + OPTIONS, denies GET.
	gate, err := accessgate.New(accessgate.DefaultPublic, []string{"POST /api/v1/collect/ai-sessions/ingest"}, nil)
	if err != nil {
		t.Fatalf("accessgate.New: %v", err)
	}
	if !gate.Public(http.MethodPost, "/api/v1/collect/ai-sessions/ingest") {
		t.Fatal("POST /api/v1/collect/ai-sessions/ingest should be admitted")
	}
	if !gate.Public(http.MethodOptions, "/api/v1/collect/ai-sessions/ingest") {
		t.Fatal("OPTIONS /api/v1/collect/ai-sessions/ingest should be admitted for CORS preflight")
	}
	if gate.Public(http.MethodGet, "/api/v1/collect/ai-sessions/ingest") {
		t.Fatal("GET /api/v1/collect/ai-sessions/ingest must be denied")
	}
	if gate.Public(http.MethodGet, "/api/v1/collect/ai-sessions/query") {
		t.Fatal("GET /api/v1/collect/ai-sessions/query must be denied")
	}
}
