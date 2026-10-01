package format4

import (
	"context"
	"errors"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// Options opens a format-4 engine.
type Options struct {
	DataRoot      string // <data>; the engine root is <data>/fsql4
	Create        CreateMode
	GseqFloor     uint64 // CreateFresh / CreateForMigration
	Cores         int    // 0 = runtime.NumCPU()
	Tuning        Tuning
	Wasm          []byte // nil = flatsqlrt.P4ThreadsWasm()
	AOTCacheDir   string
	CompileOnMiss bool                   // tests and prewarm only
	Store         *flatsqlrt.NativeStore // the node's shared native I/O store, as format 2
	OnFailure     func(error)            // instance trapped or hung; the Engine is fenced
}

// errNotBuilt is the hand-off stub's answer (contract §8): the engine
// binding lands in the commits that follow this one.
var errNotBuilt = errors.New("format4: the engine binding is not built yet")

// Open initializes and starts the engine (init + start). *Engine implements
// API.
func Open(ctx context.Context, opt Options) (*Engine, error) { return nil, errNotBuilt }

// Engine is the format-4 store over the real engine.
type Engine struct{}

var _ API = (*Engine)(nil)

func (e *Engine) RegisterType(TypeSpec) error                   { return errNotBuilt }
func (e *Engine) SetQuota(int64) error                          { return errNotBuilt }
func (e *Engine) Activate(context.Context) error                { return errNotBuilt }
func (e *Engine) Stats() ([]uint64, error)                      { return nil, errNotBuilt }
func (e *Engine) Put(context.Context, Batch) ([]Outcome, error) { return nil, errNotBuilt }
func (e *Engine) Supersede(context.Context, string, string, string, string, bool) (SupersedeResult, error) {
	return SupersedeResult{}, errNotBuilt
}
func (e *Engine) Delete(context.Context, string, []string) (int64, error) { return 0, errNotBuilt }
func (e *Engine) QuotaGC(context.Context, int64) (QuotaResult, error) {
	return QuotaResult{}, errNotBuilt
}
func (e *Engine) Rebuild(context.Context, string, RebuildWhat) ([]RebuildRow, error) {
	return nil, errNotBuilt
}
func (e *Engine) Get(context.Context, string, []string, bool, bool) ([]Rec, error) {
	return nil, errNotBuilt
}
func (e *Engine) Tags(context.Context, string, []string) ([]TagRow, error) { return nil, errNotBuilt }
func (e *Engine) Scan(context.Context, Query) ([]Rec, error)               { return nil, errNotBuilt }
func (e *Engine) Head(context.Context, Query) (Head, error)                { return Head{}, errNotBuilt }
func (e *Engine) Window(context.Context, Query) ([]Rec, error)             { return nil, errNotBuilt }
func (e *Engine) IndexPage(context.Context, Query) ([]IndexRow, error)     { return nil, errNotBuilt }
func (e *Engine) Epoch(context.Context, EpochQuery) ([]Rec, error)         { return nil, errNotBuilt }
func (e *Engine) EpochCount(context.Context, EpochQuery) (int64, error)    { return 0, errNotBuilt }
func (e *Engine) Coverage(context.Context, EpochQuery) ([]CoverageBucket, error) {
	return nil, errNotBuilt
}
func (e *Engine) Types(context.Context) ([]TypeSummary, error)           { return nil, errNotBuilt }
func (e *Engine) Partitions(context.Context) ([]PartitionSummary, error) { return nil, errNotBuilt }
func (e *Engine) Lanes(context.Context, string) ([]Lane, error)          { return nil, errNotBuilt }
func (e *Engine) Disk(context.Context) ([]DiskSummary, error)            { return nil, errNotBuilt }
func (e *Engine) FTS(context.Context) ([]FTSState, error)                { return nil, errNotBuilt }
func (e *Engine) SQL(context.Context, SQLRequest, func([]byte) error) (SQLStats, error) {
	return SQLStats{}, errNotBuilt
}
func (e *Engine) Surface(context.Context) ([]Relation, error) { return nil, errNotBuilt }
func (e *Engine) Close(context.Context) error                 { return nil }
