package format4

import (
	"errors"
	"fmt"
)

// Engine status codes (flatsql_p4.h P4_*; contract §3.2).
const (
	StatusOK          int32 = 0
	StatusArg         int32 = -1 // malformed request or config
	StatusNoMem       int32 = -2
	StatusIO          int32 = -3
	StatusCorrupt     int32 = -4 // the file is quarantined; reads elsewhere continue
	StatusFormat      int32 = -5 // store/file format above this engine; marker/state mismatch; C-5 rule change
	StatusBusy        int32 = -6 // a queue or partition credit is full: nothing was done
	StatusCancelled   int32 = -7
	StatusBudget      int32 = -8
	StatusUnsupported int32 = -9
	StatusNoType      int32 = -10
	StatusStopped     int32 = -11
	StatusNoSpace     int32 = -12 // quota or device full: writes refuse, reads continue
	StatusSQL         int32 = -13
	StatusInternal    int32 = -14
)

var (
	ErrNotMigrated = errors.New("format4: this store is format 1 and has not been migrated (store-migrate --to 4)")
	ErrWrongFormat = errors.New("format4: this store is format 2 or 3; format 4 does not open it")
	ErrClosed      = errors.New("format4: engine closed")
)

// The status errors errors.Is matches a *StatusError against, by status.
var (
	ErrBusy      error = statusSentinel(StatusBusy)
	ErrNoType    error = statusSentinel(StatusNoType)
	ErrCorrupt   error = statusSentinel(StatusCorrupt)
	ErrNoSpace   error = statusSentinel(StatusNoSpace)
	ErrBudget    error = statusSentinel(StatusBudget)
	ErrCancelled error = statusSentinel(StatusCancelled)
	ErrStopped   error = statusSentinel(StatusStopped)
	ErrFormat    error = statusSentinel(StatusFormat)
	ErrSQL       error = statusSentinel(StatusSQL)
)

func statusSentinel(status int32) *StatusError { return &StatusError{Status: status} }

// StatusError is every negative engine status. errors.Is(err, ErrBusy|
// ErrNoType|ErrCorrupt|ErrNoSpace|ErrBudget|ErrCancelled|ErrStopped|
// ErrFormat|ErrSQL) matches by status.
type StatusError struct {
	Op     string
	Status int32
	Msg    string
}

func (e *StatusError) Error() string {
	s := "format4: "
	if e.Op != "" {
		s += e.Op + ": "
	}
	s += statusName(e.Status)
	if e.Msg != "" {
		s += ": " + e.Msg
	}
	return s
}

// Is matches another *StatusError with the same status.
func (e *StatusError) Is(target error) bool {
	var t *StatusError
	return errors.As(target, &t) && t.Status == e.Status
}

func statusName(status int32) string {
	switch status {
	case StatusOK:
		return "ok"
	case StatusArg:
		return "malformed request (P4_E_ARG)"
	case StatusNoMem:
		return "out of memory (P4_E_NOMEM)"
	case StatusIO:
		return "I/O error (P4_E_IO)"
	case StatusCorrupt:
		return "corrupt file, quarantined (P4_E_CORRUPT)"
	case StatusFormat:
		return "format or state mismatch (P4_E_FORMAT)"
	case StatusBusy:
		return "busy, nothing done (P4_E_BUSY)"
	case StatusCancelled:
		return "cancelled (P4_E_CANCELLED)"
	case StatusBudget:
		return "over budget (P4_E_BUDGET)"
	case StatusUnsupported:
		return "unsupported (P4_E_UNSUPPORTED)"
	case StatusNoType:
		return "type not registered (P4_E_NOTYPE)"
	case StatusStopped:
		return "engine stopped (P4_E_STOPPED)"
	case StatusNoSpace:
		return "no space: writes refused (P4_E_NOSPACE)"
	case StatusSQL:
		return "SQL error (P4_E_SQL)"
	case StatusInternal:
		return "internal error (P4_E_INTERNAL)"
	}
	return fmt.Sprintf("status %d", status)
}

// Per-record reject codes (PUT rows; contract §3.2): ps::RejectCode, kept,
// and the format-4 additions.
const (
	RejectBadEntry    int32 = -100
	RejectFrameSize   int32 = -101
	RejectFID         int32 = -102
	RejectVerify      int32 = -103
	RejectCIDMismatch int32 = -104
	RejectSealed      int32 = -105
	RejectAttr        int32 = -106
	RejectQuarantined int32 = -107
	RejectNoType      int32 = -108
	RejectTxnTooLarge int32 = -109
	RejectCIDForm     int32 = -110
	RejectSeq         int32 = -111
	RejectTooLarge    int32 = -112
	RejectTag         int32 = -113
)

// RejectReason names a per-record reject code (-100..-113).
func RejectReason(code int32) string {
	switch code {
	case RejectBadEntry:
		return "bad entry"
	case RejectFrameSize:
		return "frame size"
	case RejectFID:
		return "file identifier does not match the type"
	case RejectVerify:
		return "record does not verify against the type's schema"
	case RejectCIDMismatch:
		return "CID does not match the record"
	case RejectSealed:
		return "invalid sealed envelope"
	case RejectAttr:
		return "attribute extraction failed"
	case RejectQuarantined:
		return "target file is quarantined"
	case RejectNoType:
		return "type not registered"
	case RejectTxnTooLarge:
		return "transaction too large"
	case RejectCIDForm:
		return "CID is not a CIDv1 raw sha2-256 (bafkrei…)"
	case RejectSeq:
		return "migrate seq missing, not positive, or held by another CID"
	case RejectTooLarge:
		return "record too large"
	case RejectTag:
		return "bad tag (no provider or source, index out of range, or a peer of another producer)"
	}
	return fmt.Sprintf("reject %d", code)
}
