// Package format4 is SDN's binding to store format 4, "p4": one SQLite table
// file per source feed x standard in the FlatSQL engine (P/<TYPE>/<feed>.db,
// the feed being the record's (provider, source); untagged records in
// local.db), each indexed by arrival, object + epoch, epoch and CID (stack
// design docs/architecture/flatsql-sqlite-partitions.md; build-out contract
// §5.2, C-37, C-38). A row holds no provider or source string: they are the
// file's feed, and the batch and publishing node are small per-file ids. A
// feed file's CID index is the only one (C-38): a type has no per-record
// index across its feeds, only a small registry of its feed files and their
// counters, and nothing ties a record in one feed file to a record in
// another. A record held by N feeds is a row set in each of them and answers
// once per feed in type-wide reads, datasync pages and counts (C-38 (5)).
// The C ABI and this API are unchanged by the feed layout: a tag still names
// its (provider, source, batch, ...) and the engine files it by its feed.
//
// The engine is the published flatsql-p4-threads.wasm
// (flatsqlrt/p4artifact.go): ONE threaded WasmEdge instance holds the writer
// pool, the read lanes and the maintenance thread (flatsqlrt/p4instance.go).
// Go talks to it through a mailbox of request slots in the instance's shared
// memory (mailbox.go): a request is a TLV, a response is RB1 row blocks
// streamed through the slot's ring. Go never calls a guest export on a hot
// path; control exports (registration, quota, activation, stats, stop) run on
// the instance's exec thread.
//
// Everything here is behind SDN_STORE_FORMAT=4 (or "sqlite"): format 1 stays
// the default, and format 2 is unchanged.
//
// API is the whole surface; Engine implements it over the real engine, the
// one implementation (C-33).
package format4

import (
	"context"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

// CreateMode is how Open treats the engine root (config tag 2).
type CreateMode uint8

const (
	OpenExisting       CreateMode = 0 // an activated store, else ErrFormat
	CreateFresh        CreateMode = 1 // open, or create fresh (MIGRATED then STORE)
	CreateForMigration CreateMode = 2 // a migration target: create or reopen an unactivated store
)

// Tuning is the engine's sizing. Zero is the engine default; fields map 1:1
// onto config tags (contract §3.3). Quota has one mode, the oldest records by
// arrival (v11: config tag 47 is ignored), so there is no field for it.
type Tuning struct {
	WriterThreads, ReaderLanes, BulkLanes, SandboxLanes, WriteSlots, ReadSlots uint32
	WriteRequestBytes, ReadRequestBytes, RingBytes                             uint32
	EngineBytes, PendingMapBytes, SoftHeap, HardHeap                           uint64
	WriterConns, WriterCacheKiB, ReaderConns, ReaderCacheKiB                   uint32
	GroupCommitRecords, GroupCommitMs                                          uint32
	Extra                                                                      []byte // raw TLV appended (tests)
}

// Tag is a lane identity (C-3): format 1's tag key plus the source URL.
type Tag struct{ Provider, Source, SourceURL, Batch, ContentKeyID, ProducerPeer, ProducerPubkey string }

// TagInstance is a tag on a record, with the time it was materialized (format
// 1's created_at / MaterializedAt), Unix seconds.
type TagInstance struct {
	Tag
	At int64
}

// TagAt is an explicit tag instance of a migrated record: an index into
// Batch.Tags and its time.
type TagAt struct {
	Tag int
	At  int64
}

// Mode is a PUT's mode.
type Mode uint8

const (
	ModeIngest  Mode = 0
	ModeMigrate Mode = 1
)

// In is one record of a PUT.
type In struct {
	CID    string // bafkrei… text of the PLAINTEXT
	Plain  []byte // plaintext record (the wire frame is built by the client)
	Sealed []byte // nil, or the sealed bytes stored instead
	TS     int64  // Unix s
	Sig    []byte
	Ident  *[32]byte // IQC
	Seq    int64     // ModeMigrate only
	Peer   string    // "" = Batch.Peer
	Tags   []TagAt   // ModeMigrate only
}

// Batch is one PUT: records of one type from one writer peer.
type Batch struct {
	Type    string
	Peer    string
	Tags    []Tag
	At      int64 // ingest tag instances' at; 0 = engine clock
	Mode    Mode
	Records []In
	// OwnTS (tag 55): a COPY of a held record stores this write's TS, not
	// the holder's (format 1's StoreRoutedByProducer, C-39 E6).
	OwnTS bool
}

// Action is what a PUT did with one record.
type Action uint8

const (
	ActRejected Action = 0
	ActNew      Action = 1
	ActCopy     Action = 2
	ActRetag    Action = 3
	ActDup      Action = 4
	ActIdentDup Action = 5
	ActMigrated Action = 6
)

// Outcome is one record's PUT result; Reject is a -100..-113 code when
// Action is ActRejected (RejectReason names it).
type Outcome struct {
	Action Action
	Seq    int64
	Reject int32
}

// Field is a predicate's field (contract §3.5).
type Field uint8

const (
	FieldEpoch    Field = 1
	FieldTS       Field = 2
	FieldW        Field = 3
	FieldEpochDay Field = 4
	FieldCol0     Field = 10
	FieldCol1     Field = 11
	FieldCol2     Field = 12
	FieldCol3     Field = 13
)

// Op is a predicate's operator.
type Op uint8

const (
	OpEq      Op = 1
	OpNe      Op = 2
	OpLt      Op = 3
	OpLe      Op = 4
	OpGt      Op = 5
	OpGe      Op = 6
	OpBetween Op = 7
	OpLike    Op = 8
	OpIn      Op = 9
	OpNotNull Op = 10
)

// Pred is one predicate; predicates are AND-ed, with SQL NULL semantics.
type Pred struct {
	Field  Field
	Op     Op
	Values []format2.Cell
}

// LaneFilter selects records with a tag instance matching every given field
// (ANY-row semantics, as format 1). "" = any.
type LaneFilter struct{ Provider, Source, Batch, ContentKeyID, ProducerPeer, ProducerPubkey string }

// Order is a scan's or window's order.
type Order uint8

const (
	OrderSeqAsc  Order = 1
	OrderSeqDesc Order = 2
	OrderWDesc   Order = 3
	OrderCID     Order = 4
	// SCAN only (C-39 E2, E3): format 1's two-part pages, the tagged
	// records (the feed files) in the order with the offset, then, without
	// a lane filter, the untagged (local) records from the start, as many
	// as the limit leaves.
	OrderNewest Order = 5 // delivery time desc, CID asc; local: ts desc, CID asc (the raw default page)
	OrderRecent Order = 6 // delivery time desc, seq desc; local: seq desc (QueryRecentRecords)
	OrderWAsc   Order = 7 // w asc, CID asc (a raw page with a sync filter or a search)
)

// Part is which of a type's files a read covers (request tag 20, C-43
// B2): every file, or the local file (the untagged records) with or
// without the feed files the lane filter selects.
type Part uint8

const (
	PartAll Part = 0 // every file (the lane filter picks feed files; untagged records only without one)
	// PartLocal is the type's local file only.
	PartLocal Part = 2
	// PartLocalLane is the local file plus the feed files the lane filter
	// selects: with lane source "local", format 1's "local" partition (its
	// untagged records and any feed whose source is "local").
	PartLocalLane Part = 3
)

// Class is a request's service class.
type Class uint8

const (
	ClassWrite       Class = 1
	ClassInteractive Class = 2
	ClassBulk        Class = 3
	ClassSandbox     Class = 4
)

// Query is a read's filters, order and paging.
type Query struct {
	Type                 string
	CID, Peer, Producer  string
	Lane                 LaneFilter
	SeqAfter, SeqThrough int64
	Preds                []Pred
	Search               string
	Order                Order
	Limit, Offset        int64
	Hydrate              bool
	ByteCap              int64 // Head only
	Part                 Part  // Epoch only
	Bulk                 bool  // run on the bulk class
}

// EpochProfile is an EPOCH op's profile.
type EpochProfile uint8

const (
	EpochWindow   EpochProfile = 1
	EpochNearest  EpochProfile = 2
	EpochAsOf     EpochProfile = 3
	EpochForward  EpochProfile = 4
	EpochCoverage EpochProfile = 5
)

// EpochQuery is an EPOCH op: a Query plus the profile, its time and the max
// distance in seconds (0 = none).
type EpochQuery struct {
	Query
	Profile      EpochProfile
	At, MaxDelta int64
}

// Rec is a REC row (contract §3.6).
type Rec struct {
	Seq                 int64
	CID, Producer, Peer string
	TS                  int64
	Epoch               int64
	HasEpoch            bool
	Key                 string // text form ("" = none); ints as decimal
	Sig, Data           []byte // Data nil unless hydrated
	Len                 int64
	Tag                 *TagInstance // matched tag (§3.6); nil when none
}

// TagRow is a TAGS row: one per (cid, tag identity), merged over copies. A
// record held by several feed files lists each file's instances, each with
// that file's seq (C-38).
type TagRow struct {
	CID      string
	Seq      int64
	Producer string
	TagInstance
}

// Head is a HEAD row: counts, heads and the snapshot in one.
type Head struct {
	N, Bytes, MaxSeq, MaxTS, MaxAt, Through int64
	More                                    bool
}

// IndexRow is an INDEX_PAGE row.
type IndexRow struct {
	Col0, Epoch *int64
	CID         string
}

// CoverageBucket is one day of an epoch coverage.
type CoverageBucket struct {
	Day                   string
	N, MinEpoch, MaxEpoch int64
}

// TypeSummary is a SUMMARY kind 1 row: sums over the type's feed files, so a
// record held by N feeds counts N times (C-38 (5)).
type TypeSummary struct {
	Type                              string
	Records, Copies, Bytes, CopyBytes int64
	MinEpoch, MaxEpoch                *int64
	MinTS, MaxTS, MaxSeq, Through     int64
}

// PartitionSummary is a SUMMARY kind 2 row: one producer token of a type
// (its copies summed over the type's feed files, a copy in N feed files N
// times; Files counts those files).
type PartitionSummary struct {
	Type, Producer, Peer                        string
	Records, Bytes, MinTS, MaxTS, MaxSeq, Files int64
}

// Lane is a SUMMARY kind 3 row: one live tag instance of one feed file
// (provider and source from the file's feed; batch, content key, producer
// peer and key from the instance). Producer is "" (C-37: a feed file holds
// every producer's copies).
type Lane struct {
	Type, Producer string
	Tag
	Records, Bytes, MaxSeq, First, Updated, MinW, MaxW int64
}

// DiskSummary is a SUMMARY kind 4 row.
type DiskSummary struct {
	Type                                                                    string
	Files, DBBytes, WALBytes, JournalBytes, IndexBytes, FTSBytes, FreeBytes int64
}

// FTSState is a SUMMARY kind 5 row: "off", "building" or "ready".
type FTSState struct {
	Type, State string
	Through     int64
}

// SupersedeResult is a SUPERSEDE row.
type SupersedeResult struct{ TagsDeleted, RecordsDeleted, FilesDeleted int64 }

// QuotaResult is a QUOTA_GC row.
type QuotaResult struct{ FilesDropped, RecordsDropped, BytesFreed int64 }

// RebuildWhat selects what a REBUILD rebuilds (a bit set).
type RebuildWhat uint32

const (
	RebuildPartitionIndexes RebuildWhat = 1
	RebuildTypeIndex        RebuildWhat = 2
	RebuildFTS              RebuildWhat = 4
	RebuildVerify           RebuildWhat = 8
)

// RebuildRow is a REBUILD row, one per type.
type RebuildRow struct {
	Type                string
	Entries, Mismatches int64
}

// Caps bound a SQL statement (0 = the class default, or none for results).
type Caps struct{ MaxRowsExamined, MaxBytesRead, MaxResultRows, MaxResultBytes uint64 }

// SQLRequest is one SQL statement.
type SQLRequest struct {
	SQL          string
	Params       []format2.Cell
	Class        Class
	Sandbox, Raw bool
	Caps         Caps
}

// SQLStats are a finished statement's counters and times.
type SQLStats struct {
	Rows, RowsExamined, BytesRead uint64
	Queue, Run                    time.Duration
}

// Relation is one SQL relation of the public surface.
type Relation struct {
	Name, Kind, Source   string
	Columns, Placeholder []string
	Bound                int64
}

// API is the format-4 store.
//
// Every method is goroutine-safe and no Go lock is held across an engine
// call. ctx cancellation sets the request's cancel word, waits up to a second
// for the engine to finish it, then returns ctx.Err(). Reads never return
// ErrBusy: a read with no free slot waits for one, bounded by ctx. A trap or
// hang fences the store: every call then returns ErrStopped.
type API interface {
	// control (exec thread; not per request)
	RegisterType(spec TypeSpec) error
	SetQuota(bytes int64) error
	Activate(ctx context.Context) error // CreateForMigration only: engine step 1 of §2.3 (Go then calls marker.FinishActivation)
	Stats() ([]uint64, error)           // §3.10 order
	// State stamps the counter state: while it returns the same stamp with
	// ok, no write started or ended, so every count, head and summary
	// (Types, Partitions, Lanes, Head without a search) answers the same, and
	// what a caller derives from them may be kept under the stamp. ok is
	// false while a write runs. With a quota set it moves every second (the
	// engine's own quota pass).
	State() (stamp uint64, ok bool)
	// writes (durable when they return; P4_E_BUSY retried with backoff until ctx is done, or ErrBusy after 2 minutes of refusals)
	Put(ctx context.Context, b Batch) ([]Outcome, error) // len == len(b.Records), input order; the client splits by request size
	Supersede(ctx context.Context, typ, provider, source, keepBatch string, apply bool) (SupersedeResult, error)
	Delete(ctx context.Context, typ string, cids []string) (int64, error)
	QuotaGC(ctx context.Context, maxBytes int64) (QuotaResult, error)
	Rebuild(ctx context.Context, typ string, what RebuildWhat) ([]RebuildRow, error) // typ "" = all
	// reads
	Get(ctx context.Context, typ string, cids []string, allCopies, hydrate bool) ([]Rec, error) // a miss is no row, never an error
	Tags(ctx context.Context, typ string, cids []string) ([]TagRow, error)
	Scan(ctx context.Context, q Query) ([]Rec, error)
	Head(ctx context.Context, q Query) (Head, error)
	Window(ctx context.Context, q Query) ([]Rec, error)
	IndexPage(ctx context.Context, q Query) ([]IndexRow, error)
	Epoch(ctx context.Context, q EpochQuery) ([]Rec, error)      // profiles 1-4
	EpochCount(ctx context.Context, q EpochQuery) (int64, error) // profiles 1-4
	Coverage(ctx context.Context, q EpochQuery) ([]CoverageBucket, error)
	Types(ctx context.Context) ([]TypeSummary, error)
	Partitions(ctx context.Context) ([]PartitionSummary, error)
	Lanes(ctx context.Context, typ string) ([]Lane, error) // typ "" = all
	Disk(ctx context.Context) ([]DiskSummary, error)
	FTS(ctx context.Context) ([]FTSState, error)
	SQL(ctx context.Context, req SQLRequest, sink func(chunk []byte) error) (SQLStats, error) // RB1 or raw bytes, streamed
	Surface(ctx context.Context) ([]Relation, error)
	Close(ctx context.Context) error // flatsql_p4_stop within ctx's deadline
}
