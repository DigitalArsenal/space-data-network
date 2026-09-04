package flatsqlrt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// Sandboxed public query (gateway loop G.5). The engine export
// flatsql_query_sandboxed executes ONE read-only SELECT for untrusted
// callers with enforcement IN-WASM: a SQLite authorizer restricted to the
// record tables / source shadow tables / unified views (control tables are
// invisible), single-statement validation, sqlite3_stmt_readonly, a
// progress-handler statement timeout, and row/byte caps that REJECT
// oversized results. It bypasses the engine's statement/query/raw-stream
// caches and the host-side raw-stream mirror entirely.

// SandboxCaps are the host-configured resource caps for one sandboxed
// execution. Zero values mean "unlimited" — production hosts should always
// set all three.
type SandboxCaps struct {
	MaxRows  uint64
	MaxBytes uint64
	Timeout  time.Duration
}

// Sandbox rejection codes (the "<code>" of the engine's latched
// "sandbox: <code>: ..." error text).
const (
	SandboxCodeNotAuthorized   = "not-authorized"      // PRAGMA/ATTACH/DDL/DML/denied table
	SandboxCodeMultiStatement  = "multi-statement"     // more than one statement
	SandboxCodeEmptyStatement  = "empty-statement"     // no statement / comment only
	SandboxCodeReadOnly        = "read-only"           // stmt_readonly false
	SandboxCodeNotSelect       = "not-select"          // no result columns
	SandboxCodeTimeout         = "timeout"             // progress-handler deadline
	SandboxCodeRowCap          = "row-cap"             // result exceeds MaxRows
	SandboxCodeByteCap         = "byte-cap"            // result exceeds MaxBytes
	SandboxCodeNotRecordStream = "not-a-record-stream" // stream mode, non-BLOB cell
	SandboxCodeParams          = "params"              // bind-parameter count mismatch
)

// SandboxError is a typed sandbox rejection. Plain SQL errors (bad syntax,
// unknown column) are NOT SandboxErrors — they surface as ordinary engine
// errors.
type SandboxError struct {
	Code    string // one of the SandboxCode* constants
	Message string // full engine error text
}

func (e *SandboxError) Error() string { return e.Message }

// AsSandboxError unwraps err to a *SandboxError when the failure was a
// sandbox rejection.
func AsSandboxError(err error) (*SandboxError, bool) {
	var se *SandboxError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}

// parseSandboxError classifies an engine error-latch text. Returns the
// typed error when it carries the sandbox prefix, else nil.
func parseSandboxError(text string) *SandboxError {
	const prefix = "sandbox: "
	if !strings.HasPrefix(text, prefix) {
		return nil
	}
	rest := text[len(prefix):]
	code := rest
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		code = rest[:i]
	}
	return &SandboxError{Code: strings.TrimSpace(code), Message: text}
}

// sandboxModes for flatsql_query_sandboxed.
const (
	sandboxModeStream = 0
	sandboxModeJSON   = 1
)

// querySandboxed runs the export and reads the response artifact. Lock
// handling and readout mirror QueryRawFlatBufferStream, minus every cache
// layer (sandboxed results must never be replayed from or stored into the
// host mirror).
func (d *Database) querySandboxed(sql string, mode int, caps SandboxCaps, params []interface{}) ([]byte, int, int, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	blob, err := EncodeParams(params)
	if err != nil {
		return nil, 0, 0, err
	}

	var out []byte
	var rows, cols int
	err = d.rt.mod.RunOnExecThread(context.Background(), func(inv wasmrt.GuestCaller) error {
		sqlPtr, err := d.rt.allocCStringVia(inv, sql)
		if err != nil {
			return err
		}
		defer d.rt.freeVia(inv, sqlPtr)

		blobPtr, err := d.rt.allocBytesVia(inv, blob)
		if err != nil {
			return err
		}
		if blobPtr != 0 {
			defer d.rt.freeVia(inv, blobPtr)
		}

		res, err := inv.Execute("flatsql_query_sandboxed",
			int32(d.handle), int32(sqlPtr), int32(blobPtr), int32(len(blob)), int32(len(params)),
			int32(mode),
			float64(caps.MaxRows), float64(caps.MaxBytes),
			float64(caps.Timeout/time.Millisecond))
		if err != nil {
			return d.rt.execErr("flatsql_query_sandboxed", err)
		}
		if wasmrt.ToInt32(res[0]) == 0 {
			text := d.rt.lastErrorVia(inv)
			if se := parseSandboxError(text); se != nil {
				return fmt.Errorf("flatsqlrt: query_sandboxed: %w", se)
			}
			return fmt.Errorf("flatsqlrt: query_sandboxed: %s", text)
		}

		ptrRes, err := inv.Execute("flatsql_response_artifact_data")
		if err != nil {
			return err
		}
		sizeRes, err := inv.Execute("flatsql_response_artifact_size")
		if err != nil {
			return err
		}
		rowsRes, err := inv.Execute("flatsql_response_artifact_row_count")
		if err != nil {
			return err
		}
		colsRes, err := inv.Execute("flatsql_response_artifact_column_count")
		if err != nil {
			return err
		}

		size := uint32(wasmrt.ToInt32(sizeRes[0]))
		if size > 0 {
			out, err = inv.ReadMemory(toUint32(ptrRes[0]), size)
			if err != nil {
				return err
			}
		} else {
			out = []byte{}
		}
		rows = int(toFloat64(rowsRes[0]))
		cols = int(toFloat64(colsRes[0]))
		return nil
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return out, rows, cols, nil
}

// QuerySandboxedStream executes one untrusted read-only SELECT whose every
// result cell is a BLOB and returns the aligned size-prefixed frame stream
// (byte-parity with QueryRawFlatBufferStream for the same rows). Sandbox
// rejections return a wrapped *SandboxError.
func (d *Database) QuerySandboxedStream(sql string, caps SandboxCaps, params ...interface{}) (*RawStream, error) {
	payload, rows, cols, err := d.querySandboxed(sql, sandboxModeStream, caps, params)
	if err != nil {
		return nil, err
	}
	return &RawStream{
		Bytes:      payload,
		Rows:       rows,
		Columns:    cols,
		FNV1a64:    FNV1a64WordFolded(payload),
		FrameCount: countStreamFrames(payload),
	}, nil
}

// QuerySandboxedJSON executes one untrusted read-only SELECT and returns the
// engine-assembled UTF-8 bare-array JSON bytes ([{"<column>":value},...]) —
// column names verbatim from SQLite, so schema-exact key capitalization is
// structural. Sandbox rejections return a wrapped *SandboxError.
func (d *Database) QuerySandboxedJSON(sql string, caps SandboxCaps, params ...interface{}) (payload []byte, rows, cols int, err error) {
	return d.querySandboxed(sql, sandboxModeJSON, caps, params)
}
