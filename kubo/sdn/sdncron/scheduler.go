package sdncron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/kubo/sdn/plugins"
)

// Sentinel errors the settings API maps to HTTP status codes: ErrUnknownModule
// -> 404, ErrInvalidConfig -> 400 (anything else -> 500).
var (
	ErrUnknownModule = errors.New("sdncron: unknown module")
	ErrInvalidConfig = errors.New("sdncron: invalid module config")
)

// CronModule is the seam the scheduler drives. *modulert.Module satisfies it
// (ID/CronMethods/InvokeCron); a native or test module can too. InvokeCron is
// the host-granted scheduled-fire entry point — the module's timer method runs
// through it on each tick.
type CronModule interface {
	ID() string
	CronMethods() []plugins.CronMethodSpec
	InvokeCron(ctx context.Context, method string, input []byte) ([]byte, error)
}

// Registration adds a module to the scheduler with its display metadata (the
// CronModule seam carries no name/version, so the caller supplies them from the
// module manifest).
type Registration struct {
	Module  CronModule
	Name    string
	Version string
}

// Logger is a minimal printf-style sink (e.g. a go-log Logger's Infof). Nil is
// a silent logger.
type Logger func(format string, args ...interface{})

// TimerView is one timer's effective schedule in a ModuleView.
type TimerView struct {
	ID         string `json:"id"`
	IntervalMs int64  `json:"interval_ms"`
}

// ModuleView is the settings-API read model for one registered module.
type ModuleView struct {
	ID      string       `json:"id"`
	Name    string       `json:"name,omitempty"`
	Version string       `json:"version,omitempty"`
	Timers  []TimerView  `json:"timers"`
	Config  ModuleConfig `json:"config"`
	Running bool         `json:"running"`
	LastRun string       `json:"last_run,omitempty"`
}

// timerCtl is the live control block for one (module, timer method): the
// scheduler pushes new effective intervals over resetCh to a running ticker.
type timerCtl struct {
	method    string
	defaultMs int64
	resetCh   chan int64 // buffered(1); single sender under Scheduler.mu
}

// registered is one module's scheduler state.
type registered struct {
	id      string
	name    string
	version string
	mod     CronModule
	specs   []plugins.CronMethodSpec
	timers  map[string]*timerCtl
	config  ModuleConfig // cached; source of truth for effective intervals
	running bool

	statsMu sync.Mutex
	lastRun time.Time
}

// Scheduler fires each registered module's timers on their effective interval
// and re-schedules live when a module's config changes (via a schedule_cron
// host call or the settings API). One goroutine per (module, timer) owns a
// ticker; a config change drains+sends the new interval over the timer's
// resetCh.
type Scheduler struct {
	store *ConfigStore
	log   Logger

	mu      sync.Mutex
	modules map[string]*registered
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewScheduler creates a scheduler backed by store (which may be a
// no-persistence store). log may be nil.
func NewScheduler(store *ConfigStore, log Logger) *Scheduler {
	return &Scheduler{
		store:   store,
		log:     log,
		modules: make(map[string]*registered),
	}
}

func (s *Scheduler) logf(format string, args ...interface{}) {
	if s.log != nil {
		s.log(format, args...)
	}
}

// Register adds a module: it loads the module's persisted config, computes each
// timer's effective interval, and — if the scheduler is already started — begins
// firing immediately. This is the cron half of the module registration process.
func (s *Scheduler) Register(reg Registration) error {
	if reg.Module == nil {
		return errors.New("sdncron: nil module")
	}
	id := reg.Module.ID()
	if id == "" {
		return errors.New("sdncron: empty module id")
	}
	specs := reg.Module.CronMethods()

	cfg, err := s.store.Load(id)
	if err != nil {
		return fmt.Errorf("sdncron: load config for %q: %w", id, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.modules[id]; exists {
		return fmt.Errorf("sdncron: module %q already registered", id)
	}
	r := &registered{
		id:      id,
		name:    reg.Name,
		version: reg.Version,
		mod:     reg.Module,
		specs:   specs,
		timers:  make(map[string]*timerCtl, len(specs)),
		config:  cfg,
	}
	for _, spec := range specs {
		defaultMs, err := parseIntervalMs(spec.DefaultInterval)
		if err != nil {
			return fmt.Errorf("sdncron: module %q timer %q: %w", id, spec.Method, err)
		}
		r.timers[spec.Method] = &timerCtl{
			method:    spec.Method,
			defaultMs: defaultMs,
			resetCh:   make(chan int64, 1),
		}
	}
	s.modules[id] = r
	if s.started {
		s.startModuleLocked(r)
	}
	s.logf("sdncron: registered module %q (%d timer(s))", id, len(r.timers))
	return nil
}

// Start begins the scheduler under ctx (derived from the node context). Timers
// for already-registered modules start firing; modules registered later start
// on registration. Idempotent.
func (s *Scheduler) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.started = true
	for _, r := range s.modules {
		s.startModuleLocked(r)
	}
	s.logf("sdncron: scheduler started (%d module(s))", len(s.modules))
}

// Stop cancels all timer goroutines. Idempotent.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.started = false
	for _, r := range s.modules {
		r.running = false
	}
}

// Summary returns the count of registered modules and total timers (for a
// startup log line).
func (s *Scheduler) Summary() (modules, timers int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.modules {
		modules++
		timers += len(r.timers)
	}
	return
}

// startModuleLocked launches one goroutine per timer. Caller holds s.mu.
func (s *Scheduler) startModuleLocked(r *registered) {
	if r.running {
		return
	}
	r.running = true
	for _, tc := range r.timers {
		go s.runTimer(s.ctx, r, tc)
	}
}

// runTimer owns one (module, timer) ticker until ctx is cancelled, firing the
// cron method on each tick and adopting a new interval whenever one arrives on
// resetCh.
func (s *Scheduler) runTimer(ctx context.Context, r *registered, tc *timerCtl) {
	s.mu.Lock()
	ms := s.effectiveMsLocked(r, tc)
	s.mu.Unlock()

	ticker := time.NewTicker(clampDuration(ms))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case newMs := <-tc.resetCh:
			ticker.Reset(clampDuration(newMs))
		case <-ticker.C:
			s.fire(ctx, r, tc.method)
		}
	}
}

// fire runs one scheduled invocation. InvokeCron applies the module's own
// scheduled resource budget; errors are logged, never fatal. The module's
// configured timer_input (if any) is passed as the invoke payload — the
// config-driven seam for a data-source module's pull config (e.g. objectCap).
// The config is read under s.mu, then released before the (potentially minutes-
// long) guest execution.
func (s *Scheduler) fire(ctx context.Context, r *registered, method string) {
	s.mu.Lock()
	input := r.config.timerInput()
	s.mu.Unlock()

	_, err := r.mod.InvokeCron(ctx, method, input)
	r.statsMu.Lock()
	r.lastRun = time.Now().UTC()
	r.statsMu.Unlock()
	if err != nil {
		s.logf("sdncron: module %q timer %q invoke error: %v", r.id, method, err)
	}
}

// effectiveMsLocked resolves a timer's interval: per-timer override, else
// module-wide override, else the manifest default. Caller holds s.mu.
func (s *Scheduler) effectiveMsLocked(r *registered, tc *timerCtl) int64 {
	if tc == nil {
		return 0
	}
	if ms, ok := r.config.timerIntervalMs(tc.method); ok {
		return ms
	}
	if ms, ok := r.config.intervalMs(); ok {
		return ms
	}
	return tc.defaultMs
}

// pushIntervalLocked recomputes a timer's effective interval and hands it to the
// running ticker (drain any stale value first; single sender under s.mu).
func (s *Scheduler) pushIntervalLocked(r *registered, tc *timerCtl) {
	ms := s.effectiveMsLocked(r, tc)
	select {
	case <-tc.resetCh:
	default:
	}
	select {
	case tc.resetCh <- ms:
	default:
	}
}

// ---------------------------------------------------------------------------
// Settings surface (schedule_cron host call + operator API)
// ---------------------------------------------------------------------------

// ApplyConfig validates and persists a module's full config, then re-schedules
// its live timers. It is the PUT /sdn/v1/modules/{id}/config path. Returns the
// applied (persisted) config. Errors: ErrUnknownModule, ErrInvalidConfig, or a
// persistence error.
func (s *Scheduler) ApplyConfig(id string, cfg ModuleConfig) (ModuleConfig, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	applied := cloneConfig(cfg)
	if applied == nil {
		applied = ModuleConfig{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.modules[id]
	if !ok {
		return nil, ErrUnknownModule
	}
	if err := s.store.Save(id, applied); err != nil {
		return nil, err
	}
	r.config = applied
	if s.started && r.running {
		for _, tc := range r.timers {
			s.pushIntervalLocked(r, tc)
		}
	}
	return cloneConfig(applied), nil
}

// SetInterval merges one interval override into a module's config, persists it,
// and re-schedules the affected timer(s). It is the schedule_cron host-call
// path. An empty method sets the module-wide interval_ms; otherwise it sets
// timers[method]. Errors: ErrUnknownModule, ErrInvalidConfig, or persistence.
func (s *Scheduler) SetInterval(id, method string, intervalMs int64) error {
	if intervalMs <= 0 {
		return fmt.Errorf("%w: interval_ms must be a positive integer", ErrInvalidConfig)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.modules[id]
	if !ok {
		return ErrUnknownModule
	}
	cfg := cloneConfig(r.config)
	if cfg == nil {
		cfg = ModuleConfig{}
	}
	if method == "" {
		cfg[configKeyIntervalMs] = float64(intervalMs)
	} else {
		timers, _ := cfg[configKeyTimers].(map[string]interface{})
		if timers == nil {
			timers = map[string]interface{}{}
		}
		timers[method] = float64(intervalMs)
		cfg[configKeyTimers] = timers
	}
	if err := s.store.Save(id, cfg); err != nil {
		return err
	}
	r.config = cfg
	if s.started && r.running {
		if method == "" {
			for _, tc := range r.timers {
				s.pushIntervalLocked(r, tc)
			}
		} else if tc := r.timers[method]; tc != nil {
			s.pushIntervalLocked(r, tc)
		}
	}
	return nil
}

// List returns every registered module's read model, sorted by id.
func (s *Scheduler) List() []ModuleView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ModuleView, 0, len(s.modules))
	for _, r := range s.modules {
		out = append(out, s.viewLocked(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Config returns a copy of a module's stored config. The bool is false for an
// unknown module (settings API 404).
func (s *Scheduler) Config(id string) (ModuleConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.modules[id]
	if !ok {
		return nil, false
	}
	cfg := cloneConfig(r.config)
	if cfg == nil {
		cfg = ModuleConfig{}
	}
	return cfg, true
}

func (s *Scheduler) viewLocked(r *registered) ModuleView {
	timers := make([]TimerView, 0, len(r.specs))
	for _, spec := range r.specs {
		timers = append(timers, TimerView{
			ID:         spec.Method,
			IntervalMs: s.effectiveMsLocked(r, r.timers[spec.Method]),
		})
	}
	sort.Slice(timers, func(i, j int) bool { return timers[i].ID < timers[j].ID })
	cfg := cloneConfig(r.config)
	if cfg == nil {
		cfg = ModuleConfig{}
	}
	v := ModuleView{
		ID:      r.id,
		Name:    r.name,
		Version: r.version,
		Timers:  timers,
		Config:  cfg,
		Running: r.running && s.started,
	}
	r.statsMu.Lock()
	if !r.lastRun.IsZero() {
		v.LastRun = r.lastRun.UTC().Format(time.RFC3339)
	}
	r.statsMu.Unlock()
	return v
}

// CronCapHandler returns the schedule_cron capability handler bound to a
// module id. It handles a running module's own hostcalls to register/update its
// cron schedule and to read it back. The returned function matches
// modulert.CapHandler's signature; the caller registers it under the "schedule"
// hostcall prefix. Responses are the plain {"ok":...} JSON envelope modulert
// frames for the guest.
//
// Operations (operation is the full hostcall op, e.g. "schedule.cron"):
//
//	schedule.cron / schedule.set  {"method":"<id?>","interval_ms":N}  set + reschedule
//	schedule.get                                                      read current schedule
func (s *Scheduler) CronCapHandler(moduleID string) func(operation string, payload []byte) ([]byte, error) {
	return func(operation string, payload []byte) ([]byte, error) {
		switch operation {
		case "schedule.cron", "schedule.set":
			var req struct {
				Method     string `json:"method"`
				IntervalMs int64  `json:"interval_ms"`
			}
			if len(payload) > 0 {
				if err := json.Unmarshal(payload, &req); err != nil {
					return errJSON(fmt.Sprintf("schedule_cron: malformed payload: %v", err)), nil
				}
			}
			if req.IntervalMs <= 0 {
				return errJSON("schedule_cron: interval_ms must be a positive integer (milliseconds)"), nil
			}
			if err := s.SetInterval(moduleID, req.Method, req.IntervalMs); err != nil {
				return errJSON(err.Error()), nil
			}
			return okJSON(s.scheduleResult(moduleID)), nil
		case "schedule.get":
			return okJSON(s.scheduleResult(moduleID)), nil
		default:
			return errJSON(fmt.Sprintf("schedule_cron: unsupported operation %q", operation)), nil
		}
	}
}

// scheduleResult builds the schedule summary a schedule_cron call returns to the
// guest: the module id and each timer's effective interval.
func (s *Scheduler) scheduleResult(moduleID string) map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.modules[moduleID]
	if !ok {
		return map[string]interface{}{"module": moduleID, "registered": false}
	}
	timers := make([]map[string]interface{}, 0, len(r.specs))
	for _, spec := range r.specs {
		timers = append(timers, map[string]interface{}{
			"id":          spec.Method,
			"interval_ms": s.effectiveMsLocked(r, r.timers[spec.Method]),
		})
	}
	return map[string]interface{}{
		"module":     moduleID,
		"registered": true,
		"timers":     timers,
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// parseIntervalMs parses a plugins.CronMethodSpec.DefaultInterval Go duration
// string ("30s", "1h", "50ms") into milliseconds. The artifact must declare
// an explicit positive interval; the host does not invent a cadence.
func parseIntervalMs(interval string) (int64, error) {
	interval = strings.TrimSpace(interval)
	if interval == "" {
		return 0, errors.New("default_interval is required")
	}
	d, err := time.ParseDuration(interval)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("default_interval %q must be a positive duration", interval)
	}
	ms := d.Milliseconds()
	if ms <= 0 {
		return 0, fmt.Errorf("default_interval %q is shorter than one millisecond", interval)
	}
	return ms, nil
}

func clampDuration(ms int64) time.Duration {
	if ms < 1 {
		ms = 1
	}
	return time.Duration(ms) * time.Millisecond
}

func okJSON(result interface{}) []byte {
	b, _ := json.Marshal(map[string]interface{}{"ok": true, "result": result})
	return b
}

func errJSON(msg string) []byte {
	b, _ := json.Marshal(map[string]interface{}{
		"ok":    false,
		"error": map[string]string{"message": msg},
	})
	return b
}
