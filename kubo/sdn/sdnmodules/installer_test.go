package sdnmodules_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdnapi"
	"github.com/ipfs/kubo/sdn/sdncron"
	"github.com/ipfs/kubo/sdn/sdnmodules"
	"github.com/ipfs/kubo/sdn/sdnservices"
	"github.com/ipfs/kubo/sdn/sdnstore"
	"github.com/ipfs/kubo/sdn/testsupport"
)

const ommSchema = `
  table OMM {
    CCSDS_OMM_VERS:double;
    CREATION_DATE:string;
    ORIGINATOR:string;
    OBJECT_NAME:string;
    OBJECT_ID:string;
    EPOCH:string;
    MEAN_MOTION:double;
    ECCENTRICITY:double;
    INCLINATION:double;
    NORAD_CAT_ID:uint32;
  }
  root_type OMM;
  file_identifier "$OMM";
`

func ommSchemas() sdnstore.SchemaProvider {
	return sdnstore.SchemaProviderFunc(func(t string) (schema, fileID, tableName string, ok bool) {
		if t == "OMM" {
			return ommSchema, "$OMM", "OMM", true
		}
		return "", "", "", false
	})
}

func sharedAOTDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		return t.TempDir()
	}
	return filepath.Join(base, "sdn-flatsqlrt-test-aot")
}

// buildServices builds an SDN Services bundle over the given durable stores, a
// persisted operator policy (so approvals survive a reopen), and a persisted
// per-module config dir. PubSub is nil (storage-only) — the fixture's pubsub
// capability is approved for the policy gate but simply not provisioned, which
// does not block load.
func buildServices(t *testing.T, bs blockstore.Blockstore, mds ds.Datastore, modulesDir, policyPath string) (*sdnservices.Services, *modulert.CapabilityPolicyStore) {
	t.Helper()
	policy, err := modulert.NewCapabilityPolicyStore(policyPath)
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore: %v", err)
	}
	svc, err := sdnservices.BuildServices(sdnservices.Deps{
		Blockstore:       bs,
		Datastore:        mds,
		PubSub:           nil,
		Schemas:          ommSchemas(),
		RuntimeOptions:   []flatsqlrt.Option{flatsqlrt.WithAOTCache(sharedAOTDir(t))},
		Policy:           policy,
		PeerID:           "test-node",
		FallbackSource:   "test-node",
		ModulesConfigDir: modulesDir,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	return svc, policy
}

func approveAll(t *testing.T, policy *modulert.CapabilityPolicyStore, hash string, caps []string) {
	t.Helper()
	for _, c := range caps {
		if _, err := policy.Approve(modulert.CapabilityApproval{
			ModuleHash: hash, Capability: c, PluginID: "test", ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("Approve(%s): %v", c, err)
		}
	}
}

func waitFor(cond func() bool, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// TestInstallAndScheduleRealWasmModule is the primary acceptance test. It
// exercises a REAL module-sdk WASM artifact (the celestrak-supgp data-source
// module, which declares a cron timer "pull") end to end through the install +
// register pipeline:
//
//	(1) install the real .wasm -> loads through the fail-closed capability gate,
//	    registers the real *modulert.Module with the cron scheduler;
//	(2) a PUT .../config reschedules the LIVE real module to a fast cadence and
//	    the scheduler fires the REAL module's InvokeCron at that cadence
//	    (observed via the module's own TimerRunCount AND the scheduler LastRun);
//	(3) GET /sdn/v1/modules lists the real module with its timer;
//	(4) the installed-modules registry + per-module config persist under
//	    <repo>/sdn/modules/;
//	(5) a fresh Services build re-registers the module from that persisted state
//	    (Boot) and the persisted fast cadence drives its cron again.
func TestInstallAndScheduleRealWasmModule(t *testing.T) {
	wasmPath := testsupport.SkipIfNoTimerModuleWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read fixture wasm: %v", err)
	}
	hash := modulert.ContentHashHex(wasmBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	repoDir := t.TempDir()
	modulesDir := filepath.Join(repoDir, "sdn", "modules")
	policyPath := filepath.Join(repoDir, "sdn", "capability_policy.json")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Durable stores shared across the reopen.
	mds := dssync.MutexWrap(ds.NewMapDatastore())
	bs := blockstore.NewBlockstore(mds)

	// ── (1) First Services build + install ──────────────────────────────────
	svc1, policy1 := buildServices(t, bs, mds, modulesDir, policyPath)
	approveAll(t, policy1, hash, testsupport.TimerModuleSensitiveCaps)

	reg1, err := sdnmodules.NewRegistry(modulesDir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	inst1, err := sdnmodules.New(sdnmodules.Config{
		Services: svc1, Blockstore: bs, Registry: reg1, Log: t.Logf,
	})
	if err != nil {
		t.Fatalf("New installer: %v", err)
	}

	installed, err := inst1.InstallBytes(ctx, wasmBytes, "test")
	if err != nil {
		t.Fatalf("InstallBytes(real wasm): %v", err)
	}
	id := installed.ID
	if id == "" {
		t.Fatalf("installed module has empty id")
	}
	if installed.ContentHash != hash {
		t.Fatalf("installed content hash %q != %q", installed.ContentHash, hash)
	}
	if !containsStr(installed.Timers, testsupport.TimerModuleTimerMethod) {
		t.Fatalf("installed module timers %v missing %q", installed.Timers, testsupport.TimerModuleTimerMethod)
	}

	// The real *modulert.Module handle (its own TimerRunCount is the ground-truth
	// "InvokeCron fired on the REAL module" signal).
	mod := inst1.Module(id)
	if mod == nil {
		t.Fatalf("no live module handle for %q", id)
	}

	// Start the scheduler. The manifest default interval is 2h, so nothing fires
	// yet — proving fires only begin once we reschedule (2).
	svc1.Scheduler.Start(ctx)
	t.Cleanup(svc1.Scheduler.Stop)

	// ── (2) PUT .../config reschedules the LIVE real module to a fast cadence ──
	apiH := sdnapi.NewHandler(sdnapi.Deps{
		Modules: func() sdnapi.ModuleAdmin { return svc1.Scheduler },
	})
	putConfig(t, apiH, id, `{"interval_ms": 60}`)

	// The scheduler now fires the REAL module's InvokeCron at ~60ms. Observe it
	// two independent ways: the module's own timer-run counter, and the
	// scheduler's per-module LastRun (both must advance).
	if !waitFor(func() bool { return mod.RuntimeDescriptor().Stats.TimerRunCount >= 2 }, 5*time.Second) {
		t.Fatalf("real module InvokeCron did not fire >=2 times; TimerRunCount=%d", mod.RuntimeDescriptor().Stats.TimerRunCount)
	}
	if !waitFor(func() bool { return moduleView(t, apiH, id).LastRun != "" }, 3*time.Second) {
		t.Fatalf("scheduler LastRun never advanced for the real module")
	}
	t.Logf("real module %q fired InvokeCron %d time(s); last_run=%s lastTimerStatus=%q",
		id, mod.RuntimeDescriptor().Stats.TimerRunCount, moduleView(t, apiH, id).LastRun, mod.RuntimeDescriptor().Stats.LastTimerStatus)

	// ── (3) GET /sdn/v1/modules lists the real module with its timer ──────────
	mv := moduleView(t, apiH, id)
	if !mv.Running {
		t.Fatalf("module not reported running: %+v", mv)
	}
	if len(mv.Timers) != 1 || mv.Timers[0].ID != testsupport.TimerModuleTimerMethod {
		t.Fatalf("module timers = %+v, want one %q timer", mv.Timers, testsupport.TimerModuleTimerMethod)
	}
	if mv.Timers[0].IntervalMs != 60 {
		t.Fatalf("effective interval = %d, want 60 (reschedule did not take effect)", mv.Timers[0].IntervalMs)
	}

	// ── (4) Registry + per-module config persisted under <repo>/sdn/modules ───
	regPath := filepath.Join(modulesDir, "installed.json")
	regData, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("expected installed registry at %s: %v", regPath, err)
	}
	if !strings.Contains(string(regData), hash) || !strings.Contains(string(regData), id) {
		t.Fatalf("registry missing module hash/id: %s", string(regData))
	}
	cfgPath := svc1.ConfigStore.Path(id)
	if cfgPath == "" {
		t.Fatalf("no config path for %q", id)
	}
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("expected per-module config at %s: %v", cfgPath, err)
	}
	if !strings.Contains(string(cfgData), "60") {
		t.Fatalf("per-module config missing rescheduled interval: %s", string(cfgData))
	}

	// ── (5) Reopen: fresh Services build re-registers from persisted state ────
	svc1.Scheduler.Stop()
	inst1.Close()
	svc1.Close()

	svc2, _ := buildServices(t, bs, mds, modulesDir, policyPath) // policy reloaded from disk (approvals persist)
	defer svc2.Close()
	reg2, err := sdnmodules.NewRegistry(modulesDir)
	if err != nil {
		t.Fatalf("NewRegistry (reopen): %v", err)
	}
	inst2, err := sdnmodules.New(sdnmodules.Config{Services: svc2, Blockstore: bs, Registry: reg2, Log: t.Logf})
	if err != nil {
		t.Fatalf("New installer (reopen): %v", err)
	}
	defer inst2.Close()

	n, err := inst2.Boot(ctx)
	if err != nil {
		t.Fatalf("Boot (reopen): %v", err)
	}
	if n < 1 {
		t.Fatalf("Boot re-registered %d modules, want >=1", n)
	}
	mod2 := inst2.Module(id)
	if mod2 == nil {
		t.Fatalf("module %q not re-registered after reopen", id)
	}

	svc2.Scheduler.Start(ctx)
	t.Cleanup(svc2.Scheduler.Stop)

	// The persisted 60ms cadence drives the re-registered real module's cron
	// again with no re-approval and no reschedule call.
	if !waitFor(func() bool { return mod2.RuntimeDescriptor().Stats.TimerRunCount >= 2 }, 5*time.Second) {
		t.Fatalf("re-registered real module did not fire after reopen; TimerRunCount=%d", mod2.RuntimeDescriptor().Stats.TimerRunCount)
	}
	apiH2 := sdnapi.NewHandler(sdnapi.Deps{Modules: func() sdnapi.ModuleAdmin { return svc2.Scheduler }})
	mv2 := moduleView(t, apiH2, id)
	if len(mv2.Timers) != 1 || mv2.Timers[0].IntervalMs != 60 {
		t.Fatalf("persisted cadence not restored after reopen: %+v", mv2.Timers)
	}
	t.Logf("after reopen: real module %q re-registered and fired %d time(s) on the persisted 60ms cadence",
		id, mod2.RuntimeDescriptor().Stats.TimerRunCount)
}

// TestInstallDeniedForUnapprovedSensitiveCapability is the fail-closed gate: a
// real module whose declared sensitive capabilities are NOT fully approved must
// NOT install, register, run or persist — the whole install is refused.
func TestInstallDeniedForUnapprovedSensitiveCapability(t *testing.T) {
	wasmPath := testsupport.SkipIfNoTimerModuleWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read fixture wasm: %v", err)
	}
	hash := modulert.ContentHashHex(wasmBytes)

	ctx := context.Background()
	repoDir := t.TempDir()
	modulesDir := filepath.Join(repoDir, "sdn", "modules")
	policyPath := filepath.Join(repoDir, "sdn", "capability_policy.json")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mds := dssync.MutexWrap(ds.NewMapDatastore())
	bs := blockstore.NewBlockstore(mds)

	svc, policy := buildServices(t, bs, mds, modulesDir, policyPath)
	defer svc.Close()

	// Approve all-but-one sensitive capability (omit the first), leaving the
	// module short one grant -> the fail-closed gate must refuse it.
	approveAll(t, policy, hash, testsupport.TimerModuleSensitiveCaps[1:])

	reg, err := sdnmodules.NewRegistry(modulesDir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	inst, err := sdnmodules.New(sdnmodules.Config{Services: svc, Blockstore: bs, Registry: reg, Log: t.Logf})
	if err != nil {
		t.Fatalf("New installer: %v", err)
	}

	_, err = inst.InstallBytes(ctx, wasmBytes, "test")
	if err == nil {
		t.Fatalf("expected install to be DENIED for an unapproved sensitive capability (fail closed)")
	}
	deniedCap := testsupport.TimerModuleSensitiveCaps[0]
	if !strings.Contains(err.Error(), deniedCap) {
		t.Fatalf("denial error should name the unapproved capability %q, got: %v", deniedCap, err)
	}

	// Nothing was registered, nothing persisted, nothing loaded.
	if mods := svc.Scheduler.List(); len(mods) != 0 {
		t.Fatalf("a denied module must not be registered with the scheduler, got %d", len(mods))
	}
	if got := inst.List(); len(got) != 0 {
		t.Fatalf("a denied module must not appear in the installer list, got %+v", got)
	}
	if entries, _ := reg.List(); len(entries) != 0 {
		t.Fatalf("a denied module must not be persisted, got %+v", entries)
	}
}

// TestAdminInstallRecordsGrantsAndInstalls exercises the AdminInstall path
// (operator grant + content-hash install) used by the loopback admin route: the
// bytes must already be in the blockstore; the grants admit the module; a grant
// short of the module's sensitive set is refused with ErrInstallDenied.
func TestAdminInstallRecordsGrantsAndInstalls(t *testing.T) {
	wasmPath := testsupport.SkipIfNoTimerModuleWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read fixture wasm: %v", err)
	}
	hash := modulert.ContentHashHex(wasmBytes)

	ctx := context.Background()
	repoDir := t.TempDir()
	modulesDir := filepath.Join(repoDir, "sdn", "modules")
	policyPath := filepath.Join(repoDir, "sdn", "capability_policy.json")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mds := dssync.MutexWrap(ds.NewMapDatastore())
	bs := blockstore.NewBlockstore(mds)

	svc, _ := buildServices(t, bs, mds, modulesDir, policyPath)
	defer svc.Close()
	reg, _ := sdnmodules.NewRegistry(modulesDir)
	inst, err := sdnmodules.New(sdnmodules.Config{Services: svc, Blockstore: bs, Registry: reg, Log: t.Logf})
	if err != nil {
		t.Fatalf("New installer: %v", err)
	}

	// The admin route installs by content hash, so the bytes must be resident in
	// the node's block store. Seed them directly to isolate the AdminInstall
	// grant behavior from the install-by-bytes path.
	if _, _, serr := appmanifest.StoreModuleBytes(ctx, bs, wasmBytes); serr != nil {
		t.Fatalf("seed blockstore: %v", serr)
	}

	// Incomplete grant -> denied.
	_, err = inst.AdminInstall(ctx, hash, grantsFor(testsupport.TimerModuleSensitiveCaps[1:]))
	if err == nil {
		t.Fatalf("AdminInstall with an incomplete grant must be denied (fail closed)")
	}

	// Full grant -> installed + registered.
	m, err := inst.AdminInstall(ctx, hash, grantsFor(testsupport.TimerModuleSensitiveCaps))
	if err != nil {
		t.Fatalf("AdminInstall with full grant: %v", err)
	}
	if m.ContentHash != hash || !containsStr(m.Timers, testsupport.TimerModuleTimerMethod) {
		t.Fatalf("AdminInstall result unexpected: %+v", m)
	}
	if svc.Scheduler.List() == nil || len(svc.Scheduler.List()) != 1 {
		t.Fatalf("expected exactly one scheduler registration after AdminInstall")
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func grantsFor(caps []string) []sdnmodules.CapabilityGrant {
	out := make([]sdnmodules.CapabilityGrant, 0, len(caps))
	for _, c := range caps {
		out = append(out, sdnmodules.CapabilityGrant{Capability: c, ApprovedBy: "operator"})
	}
	return out
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func putConfig(t *testing.T, h http.Handler, id, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/sdn/v1/modules/"+id+"/config", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func moduleView(t *testing.T, h http.Handler, id string) sdncron.ModuleView {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sdn/v1/modules", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /sdn/v1/modules status = %d", rec.Code)
	}
	var mods []sdncron.ModuleView
	if err := json.Unmarshal(rec.Body.Bytes(), &mods); err != nil {
		t.Fatalf("decode modules: %v; body=%s", err, rec.Body.String())
	}
	for _, m := range mods {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("module %q not present in GET /sdn/v1/modules: %+v", id, mods)
	return sdncron.ModuleView{}
}
