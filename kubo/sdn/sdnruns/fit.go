package sdnruns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"sync"

	"github.com/ipfs/kubo/sdn/modulert"
)

// FitOptions is the analysis/od "fit" options port (UTF-8 JSON). InputFormat
// selects the ephemeris parser ("meme" for SpaceX MEME text, "oem" for CCSDS OEM
// KVN). The Ref* fields, supplied together, enable the module's same-ephemeris
// REFERENCE_RMS: the reference's OWN mean elements propagated via the SAME SGP4
// over the SAME fit points (the parity comparison — they do NOT perturb our fit).
type FitOptions struct {
	InputFormat string `json:"inputFormat,omitempty"`
	DataSource  string `json:"dataSource,omitempty"`
	ObjectName  string `json:"objectName,omitempty"`
	ObjectID    string `json:"objectId,omitempty"`
	NoradCatID  uint32 `json:"noradCatId,omitempty"`

	RefEpoch          string   `json:"refEpoch,omitempty"`
	RefMeanMotion     *float64 `json:"refMeanMotion,omitempty"`
	RefEccentricity   *float64 `json:"refEccentricity,omitempty"`
	RefInclination    *float64 `json:"refInclination,omitempty"`
	RefRaan           *float64 `json:"refRaan,omitempty"`
	RefArgPericenter  *float64 `json:"refArgPericenter,omitempty"`
	RefMeanAnomaly    *float64 `json:"refMeanAnomaly,omitempty"`
	RefBstar          *float64 `json:"refBstar,omitempty"`
	RefMeanMotionDot  *float64 `json:"refMeanMotionDot,omitempty"`
	RefMeanMotionDdot *float64 `json:"refMeanMotionDdot,omitempty"`
}

// FitResult is the analysis/od "fit" result-port JSON. RMS and REFERENCE_RMS are
// emitted as STRINGS by the module (fixed 3-dp km); ReferenceRMS is present only
// when Ref* options were supplied.
type FitResult struct {
	ObjectName      string  `json:"OBJECT_NAME"`
	ObjectID        string  `json:"OBJECT_ID"`
	Epoch           string  `json:"EPOCH"`
	MeanMotion      float64 `json:"MEAN_MOTION"`
	Eccentricity    float64 `json:"ECCENTRICITY"`
	Inclination     float64 `json:"INCLINATION"`
	RaOfAscNode     float64 `json:"RA_OF_ASC_NODE"`
	ArgOfPericenter float64 `json:"ARG_OF_PERICENTER"`
	MeanAnomaly     float64 `json:"MEAN_ANOMALY"`
	EphemerisType   int     `json:"EPHEMERIS_TYPE"`
	Classification  string  `json:"CLASSIFICATION_TYPE"`
	NoradCatID      uint32  `json:"NORAD_CAT_ID"`
	ElementSetNo    uint32  `json:"ELEMENT_SET_NO"`
	RevAtEpoch      float64 `json:"REV_AT_EPOCH"`
	Bstar           float64 `json:"BSTAR"`
	MeanMotionDot   float64 `json:"MEAN_MOTION_DOT"`
	MeanMotionDdot  float64 `json:"MEAN_MOTION_DDOT"`
	RMS             string  `json:"RMS"`
	ReferenceRMS    string  `json:"REFERENCE_RMS"`
	Iterations      int     `json:"ITERATIONS"`
	MaxIterations   int     `json:"MAX_ITERATIONS"`
	Converged       bool    `json:"CONVERGED"`
	DataSource      string  `json:"DATA_SOURCE"`

	// Error is set when the module returned a {"error":...} document.
	Error string `json:"error"`
}

// RMSKm parses the string RMS into km.
func (r *FitResult) RMSKm() (float64, error) { return parseKm(r.RMS) }

// ReferenceRMSKm parses the string REFERENCE_RMS into km; ok is false when the
// module emitted no reference score (no Ref* options were supplied).
func (r *FitResult) ReferenceRMSKm() (float64, bool) {
	if r.ReferenceRMS == "" {
		return 0, false
	}
	v, err := parseKm(r.ReferenceRMS)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseKm(s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("sdnruns: empty RMS")
	}
	return strconv.ParseFloat(s, 64)
}

// elements projects a fit result into the persisted Elements set.
func (r *FitResult) elements() Elements {
	rms, _ := r.RMSKm()
	return Elements{
		ObjectName:      r.ObjectName,
		ObjectID:        r.ObjectID,
		Epoch:           r.Epoch,
		NoradCatID:      r.NoradCatID,
		Classification:  r.Classification,
		MeanMotion:      r.MeanMotion,
		Eccentricity:    r.Eccentricity,
		Inclination:     r.Inclination,
		RaOfAscNode:     r.RaOfAscNode,
		ArgOfPericenter: r.ArgOfPericenter,
		MeanAnomaly:     r.MeanAnomaly,
		Bstar:           r.Bstar,
		MeanMotionDot:   r.MeanMotionDot,
		MeanMotionDdot:  r.MeanMotionDdot,
		EphemerisType:   r.EphemerisType,
		ElementSetNo:    r.ElementSetNo,
		RevAtEpoch:      r.RevAtEpoch,
		RMSKm:           rms,
		Converged:       r.Converged,
		DataSource:      r.DataSource,
	}
}

// Fitter fits operator ephemeris to SGP4 mean elements. CommandFitter is the real
// implementation over the analysis/od WASM module.
type Fitter interface {
	Fit(ctx context.Context, ephemeris []byte, opts FitOptions) (*FitResult, error)
}

// The analysis/od invoke contract.
const (
	odMethodID    = "fit"
	odMemePort    = "meme"
	odOptionsPort = "options"
	odResultPort  = "result"
	odStartExport = "_start"
	pivIdentifier = "$PIV"
)

// odStartMu serializes command-module runs process-wide: driving a WASI COMMAND
// module (analysis/od) requires temporarily wiring the process stdin/stdout fds
// to per-invocation temp files around its _start run, which is global state.
var odStartMu sync.Mutex

// CommandFitter drives the REAL analysis/od WASM module in its native COMMAND
// surface: the module is an emscripten WASI command whose _start reads a $PIV
// invoke request from stdin, runs the OD fit, and writes the $PIV response to
// stdout (its plugin_invoke_stream reactor export cannot be driven directly
// because the emscripten runtime — including the guest stack pointer — is only
// initialized inside _start). Each fit loads a fresh module instance (the command
// proc_exits after one request), wires stdin/stdout to temp files, runs _start,
// and decodes the $PIV response. This mirrors the module-SDK's own standalone
// WasmEdge loader, so the OD fit the run records is the actual WASM module
// executing (not a Go reimplementation).
type CommandFitter struct {
	log Logger

	mu       sync.Mutex
	resolve  func() ([]byte, error)
	wasm     []byte
	policy   *modulert.CapabilityPolicyStore
	policyMu sync.Once
}

// NewCommandFitter builds a fitter over the analysis/od module bytes returned by
// resolve (cached after the first successful resolve). resolve returns an error
// (or empty bytes) when the module is not available yet — the fit then fails
// cleanly.
func NewCommandFitter(resolve func() ([]byte, error), log Logger) *CommandFitter {
	return &CommandFitter{resolve: resolve, log: log}
}

func (f *CommandFitter) logf(format string, args ...interface{}) {
	if f.log != nil {
		f.log(format, args...)
	}
}

// odWasm resolves + caches the module bytes.
func (f *CommandFitter) odWasm() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.wasm) > 0 {
		return f.wasm, nil
	}
	if f.resolve == nil {
		return nil, fmt.Errorf("sdnruns: no analysis/od module resolver configured")
	}
	b, err := f.resolve()
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("sdnruns: analysis/od module (orbit-determination) is not available")
	}
	f.wasm = b
	return b, nil
}

func (f *CommandFitter) denyPolicy() *modulert.CapabilityPolicyStore {
	f.policyMu.Do(func() {
		// analysis/od declares NO capabilities, so a default-deny policy admits it.
		f.policy, _ = modulert.NewCapabilityPolicyStore("")
	})
	return f.policy
}

// Fit invokes od.fit with the ephemeris on the "meme" port and the JSON options
// on the "options" port, returning the parsed result-port JSON.
func (f *CommandFitter) Fit(ctx context.Context, ephemeris []byte, opts FitOptions) (*FitResult, error) {
	wasm, err := f.odWasm()
	if err != nil {
		return nil, err
	}
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("sdnruns: encode fit options: %w", err)
	}
	request, err := modulert.EncodeInvokeRequestFrames(odMethodID, []modulert.InvokeInputFrame{
		{PortID: odMemePort, Payload: ephemeris},
		{PortID: odOptionsPort, Payload: optsJSON},
	})
	if err != nil {
		return nil, fmt.Errorf("sdnruns: encode fit request: %w", err)
	}

	mod, err := modulert.NewModule(wasm, nil, &modulert.NodeContext{CapabilityPolicy: f.denyPolicy()})
	if err != nil {
		return nil, fmt.Errorf("sdnruns: load analysis/od module: %w", err)
	}
	defer mod.Close()
	if id := mod.ID(); id != "orbit-determination" {
		f.logf("sdnruns: unexpected od module id %q", id)
	}

	respBytes, err := runCommandStart(ctx, mod, request)
	if err != nil {
		return nil, err
	}
	// The captured stdout may carry a leading runtime log line before the $PIV
	// response; anchor on the FlatBuffer file identifier (which sits 4 bytes into
	// a non-size-prefixed buffer) to isolate the response.
	idx := bytes.Index(respBytes, []byte(pivIdentifier))
	if idx < 4 {
		preview := respBytes
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("sdnruns: analysis/od produced no $PIV response (%d bytes: %.200q)", len(respBytes), preview)
	}
	payload, err := modulert.DecodeInvokeResponsePayload(respBytes[idx-4:], odResultPort)
	if err != nil {
		return nil, fmt.Errorf("sdnruns: decode fit response: %w", err)
	}
	var res FitResult
	if err := json.Unmarshal(payload, &res); err != nil {
		return nil, fmt.Errorf("sdnruns: decode fit result (%d bytes): %w", len(payload), err)
	}
	if res.Error != "" {
		return nil, fmt.Errorf("sdnruns: analysis/od fit error: %s", res.Error)
	}
	return &res, nil
}

// runCommandStart wires the module's stdin to the request and its stdout to a
// capture, runs its _start command entry, and returns the captured stdout. It
// serializes process-wide (odStartMu) because it temporarily redirects the
// process stdin/stdout file descriptors — the only way to feed a WASI command
// module in-process without the wasmedge CLI.
func runCommandStart(ctx context.Context, mod *modulert.Module, request []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reqFile, err := os.CreateTemp("", "sdn-od-req-*")
	if err != nil {
		return nil, fmt.Errorf("sdnruns: temp request: %w", err)
	}
	defer os.Remove(reqFile.Name())
	defer reqFile.Close()
	if _, err := reqFile.Write(request); err != nil {
		return nil, fmt.Errorf("sdnruns: write request: %w", err)
	}
	if _, err := reqFile.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("sdnruns: seek request: %w", err)
	}
	outFile, err := os.CreateTemp("", "sdn-od-out-*")
	if err != nil {
		return nil, fmt.Errorf("sdnruns: temp response: %w", err)
	}
	defer os.Remove(outFile.Name())
	defer outFile.Close()

	odStartMu.Lock()
	saved0, dupErr0 := dupFD(0)
	saved1, dupErr1 := dupFD(1)
	if dupErr0 != nil || dupErr1 != nil {
		if saved0 >= 0 {
			closeFD(saved0)
		}
		if saved1 >= 0 {
			closeFD(saved1)
		}
		odStartMu.Unlock()
		return nil, fmt.Errorf("sdnruns: save stdio fds: %v / %v", dupErr0, dupErr1)
	}
	redirErr := dupOnto(int(reqFile.Fd()), 0)
	if redirErr == nil {
		redirErr = dupOnto(int(outFile.Fd()), 1)
	}
	var execErr error
	if redirErr == nil {
		// _start runs the command main: reads the request from stdin, fits, writes
		// the $PIV response to stdout, then proc_exits (a non-error exit surfaces as
		// nil here; a non-zero exit surfaces as an error we tolerate and diagnose
		// from the captured output).
		_, execErr = mod.Mod().Execute(odStartExport)
	}
	// Restore stdio no matter what.
	_ = dupOnto(saved0, 0)
	_ = dupOnto(saved1, 1)
	closeFD(saved0)
	closeFD(saved1)
	odStartMu.Unlock()

	if redirErr != nil {
		return nil, fmt.Errorf("sdnruns: redirect stdio: %w", redirErr)
	}

	if _, err := outFile.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("sdnruns: seek response: %w", err)
	}
	resp, err := io.ReadAll(outFile)
	if err != nil {
		return nil, fmt.Errorf("sdnruns: read response: %w", err)
	}
	if len(resp) == 0 && execErr != nil {
		return nil, fmt.Errorf("sdnruns: analysis/od _start produced no output: %w", execErr)
	}
	return resp, nil
}

// dupFD duplicates a file descriptor (portable across the fd-redirect helpers).
func dupFD(fd int) (int, error) { return syscallDup(fd) }

// closeFD closes a raw fd, ignoring errors.
func closeFD(fd int) { _ = syscallClose(fd) }

// ConcurrentFitter is a Fitter that can fit multiple objects at once. The Runner
// fans a run's object loop out to Concurrency() worker goroutines when its Fitter
// implements this — each worker drives its own resident instance in parallel. A
// plain Fitter (e.g. CommandFitter, which is process-wide serialized by
// odStartMu) runs the loop sequentially.
type ConcurrentFitter interface {
	Fitter
	// Concurrency is the number of Fit calls that can safely run at once.
	Concurrency() int
}

// ReactorFitter drives the REAL analysis/od WASM module as a RESIDENT WASI
// REACTOR. The reactor build (dist/isomorphic/module.wasm) exports
// _initialize/__wasm_call_ctors + plugin_invoke_stream and has NO _start, so
// modulert hosts it as a live instance (Load runs _initialize once) and every
// fit reuses that instance via plugin_invoke_stream — no per-fit module reload,
// no temp-file stdio wiring, and NO process-wide odStartMu lock. It keeps a POOL
// of N resident instances so Concurrency() fits run truly in parallel (N
// single-threaded OD instances at once). It is safe for concurrent use.
//
// The request encoding is byte-identical to CommandFitter's (both call
// EncodeInvokeRequestFrames/encodePluginInvokeRequestFrames with the same meme +
// options frames), and the fit computation is the same C++ code, so a reactor
// fit returns the SAME result (RMS, converged, mean elements) as the command
// fit for the same ephemeris — see TestReactorCommandFitParity.
type ReactorFitter struct {
	log     Logger
	resolve func() ([]byte, error)
	n       int

	mu     sync.Mutex
	loaded bool
	free   chan *modulert.Module // buffered to n: semaphore + free list
	all    []*modulert.Module
	policy *modulert.CapabilityPolicyStore
}

// NewReactorFitter builds a resident-reactor fitter over the analysis/od reactor
// module bytes returned by resolve. n is the pool size (concurrent fits); n <= 0
// selects runtime.NumCPU(). The modules are created lazily on the first Fit.
func NewReactorFitter(resolve func() ([]byte, error), n int, log Logger) *ReactorFitter {
	if n <= 0 {
		n = runtime.NumCPU()
	}
	if n < 1 {
		n = 1
	}
	return &ReactorFitter{resolve: resolve, n: n, log: log}
}

// Concurrency reports the resident-instance pool size.
func (f *ReactorFitter) Concurrency() int { return f.n }

func (f *ReactorFitter) logf(format string, args ...interface{}) {
	if f.log != nil {
		f.log(format, args...)
	}
}

// ensureLoaded resolves the reactor wasm once and spins up the resident-instance
// pool. Each modulert.NewModule call instantiates the wasm and runs its
// _initialize (global ctors), so every pooled instance is ready to be driven via
// plugin_invoke_stream.
func (f *ReactorFitter) ensureLoaded() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loaded {
		return nil
	}
	if f.resolve == nil {
		return fmt.Errorf("sdnruns: no analysis/od reactor module resolver configured")
	}
	wasm, err := f.resolve()
	if err != nil {
		return err
	}
	if len(wasm) == 0 {
		return fmt.Errorf("sdnruns: analysis/od reactor module (orbit-determination) is not available")
	}
	// analysis/od declares NO capabilities, so a default-deny policy admits it.
	policy, _ := modulert.NewCapabilityPolicyStore("")
	f.policy = policy

	free := make(chan *modulert.Module, f.n)
	all := make([]*modulert.Module, 0, f.n)
	for i := 0; i < f.n; i++ {
		mod, err := modulert.NewModule(wasm, nil, &modulert.NodeContext{CapabilityPolicy: policy})
		if err != nil {
			for _, m := range all {
				m.Close()
			}
			return fmt.Errorf("sdnruns: load analysis/od reactor instance %d/%d: %w", i+1, f.n, err)
		}
		if id := mod.ID(); id != "orbit-determination" {
			f.logf("sdnruns: unexpected od reactor module id %q", id)
		}
		all = append(all, mod)
		free <- mod
	}
	f.free = free
	f.all = all
	f.loaded = true
	return nil
}

// Fit invokes od.fit on a free resident instance via plugin_invoke_stream with
// the ephemeris on the "meme" port and JSON options on the "options" port,
// returning the parsed "result"-port JSON. It blocks until a pooled instance is
// free, so at most Concurrency() fits execute concurrently.
func (f *ReactorFitter) Fit(ctx context.Context, ephemeris []byte, opts FitOptions) (*FitResult, error) {
	if err := f.ensureLoaded(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("sdnruns: encode fit options: %w", err)
	}

	// Acquire a resident instance (semaphore); return it no matter what.
	var mod *modulert.Module
	select {
	case mod = <-f.free:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { f.free <- mod }()

	payload, err := mod.InvokeMethodFramesPort(ctx, odMethodID, []modulert.InvokeInputFrame{
		{PortID: odMemePort, Payload: ephemeris},
		{PortID: odOptionsPort, Payload: optsJSON},
	}, odResultPort)
	if err != nil {
		return nil, fmt.Errorf("sdnruns: analysis/od reactor fit: %w", err)
	}

	var res FitResult
	if err := json.Unmarshal(payload, &res); err != nil {
		return nil, fmt.Errorf("sdnruns: decode fit result (%d bytes): %w", len(payload), err)
	}
	if res.Error != "" {
		return nil, fmt.Errorf("sdnruns: analysis/od fit error: %s", res.Error)
	}
	return &res, nil
}

// Close releases every resident instance. Safe to call once after the fitter is
// no longer in use.
func (f *ReactorFitter) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.all {
		m.Close()
	}
	f.all = nil
	f.free = nil
	f.loaded = false
}
