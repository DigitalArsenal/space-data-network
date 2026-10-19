package api

// POST /api/v1/query: the admin sandboxed read-only SELECT. Request: one
// $QRP{KIND=Request} frame with SQL, PARAMETERS (typed QRPValue), MAX_ROWS,
// MAX_BYTES and MAX_ELAPSED_MS. Answer: one $QRP{KIND=Page} with COLUMNS and
// typed ROWS; a BLOB is a Bytes cell. Every error is one $QRP Error frame.

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// RegisterSandboxQueryRoute mounts POST /api/v1/query behind the admin trust
// gate.
func (h *CoreAPIHandler) RegisterSandboxQueryRoute(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/query", h.requireAuth(peers.Admin, h.handleSandboxQuery))
}

func (h *CoreAPIHandler) handleSandboxQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "query takes POST with one $QRP request frame", 0)
		return
	}
	if h.store == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "storage_unavailable", "no store", 0)
		return
	}
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != StreamContentType {
		WriteErrorFrame(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"query takes one size-prefixed $QRP request frame as "+StreamContentType, 0)
		return
	}
	sql, params, caps, err := readSandboxRequest(r)
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", err.Error(), 0)
		return
	}
	result, err := h.store.QuerySandboxedRows(sql, caps, params...)
	if err != nil {
		var sandboxErr *flatsqlrt.SandboxError
		if errors.As(err, &sandboxErr) {
			WriteErrorFrame(w, http.StatusBadRequest, sandboxErr.Code, err.Error(), 0)
			return
		}
		writeDataReadError(w, err)
		return
	}
	writeTablePage(w, sandboxPage(result))
}

func readSandboxRequest(r *http.Request) (string, []interface{}, flatsqlrt.SandboxCaps, error) {
	frames, err := ReadFrames(r.Body, scanRequestMaxBytes)
	if err != nil {
		return "", nil, flatsqlrt.SandboxCaps{}, err
	}
	if len(frames) != 1 {
		return "", nil, flatsqlrt.SandboxCaps{}, errors.New("query takes exactly one $QRP request frame")
	}
	q, err := ParseQRP(frames[0])
	if err != nil {
		return "", nil, flatsqlrt.SandboxCaps{}, err
	}
	if int8(q.KIND()) != QRPKindRequest {
		return "", nil, flatsqlrt.SandboxCaps{}, errors.New("query takes a $QRP frame of KIND Request")
	}
	sql := string(q.SQL())
	if sql == "" {
		return "", nil, flatsqlrt.SandboxCaps{}, errors.New("SQL is required")
	}
	params := make([]interface{}, q.PARAMETERSLength())
	var v QRP.QRPValue
	for i := range params {
		if !q.PARAMETERS(&v, i) {
			return "", nil, flatsqlrt.SandboxCaps{}, fmt.Errorf("parameter %d is unreadable", i+1)
		}
		params[i] = qrpParamValue(&v)
	}
	caps := flatsqlrt.SandboxCaps{MaxRows: q.MAX_ROWS(), MaxBytes: q.MAX_BYTES()}
	if ms := q.MAX_ELAPSED_MS(); ms > 0 {
		caps.Timeout = time.Duration(ms) * time.Millisecond
	}
	return sql, params, caps, nil
}

// sandboxPage types each column by its first non-null cell.
func sandboxPage(res *storage.SandboxRows) qrpTable {
	cols := make([]qrpCol, len(res.Columns))
	for i, name := range res.Columns {
		cols[i] = qrpCol{Name: name, Type: colString}
		for _, row := range res.Rows {
			if i < len(row) && row[i].Kind != storage.SandboxNull {
				cols[i].Type = map[storage.SandboxCellKind]string{storage.SandboxInt: colInt64, storage.SandboxReal: colDouble,
					storage.SandboxText: colString, storage.SandboxBlob: colBytes}[row[i].Kind]
				break
			}
		}
	}
	rows := make([][]qrpVal, len(res.Rows))
	for r, row := range res.Rows {
		cells := make([]qrpVal, len(row))
		for c, cell := range row {
			switch cell.Kind {
			case storage.SandboxInt:
				cells[c] = intVal(cell.Int)
			case storage.SandboxReal:
				cells[c] = floatVal(cell.Real)
			case storage.SandboxText:
				cells[c] = textVal(string(cell.Bytes))
			case storage.SandboxBlob:
				cells[c] = bytesVal(cell.Bytes)
			default:
				cells[c] = nullVal()
			}
		}
		rows[r] = cells
	}
	status := QRPStatusOk
	if res.Truncated {
		status = QRPStatusPartial
	}
	return qrpTable{Status: status, Columns: cols, Rows: rows, TotalCount: uint64(len(rows)), Truncated: res.Truncated, AsOfMs: time.Now().UnixMilli()}
}
