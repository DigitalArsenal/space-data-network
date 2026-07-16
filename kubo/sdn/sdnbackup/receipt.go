package sdnbackup

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
)

// RunStatus is a backup run's outcome (spec C.1): complete requires every
// PRIMARY landing to succeed; a SECONDARY failure degrades to partial; a
// PRIMARY failure fails the run.
type RunStatus string

const (
	StatusComplete RunStatus = "complete"
	StatusPartial  RunStatus = "partial"
	StatusFailed   RunStatus = "failed"
)

// ReceiptType is the 3-letter sdnstore type the receipt manifest is stored
// under (spec C.5 / D.2: StoreManifest(source, "BKR", ...)). BackupSource skips
// this type when enumerating sds_record units so receipts do not recursively
// back themselves up.
const ReceiptType = "BKR"

// Landing records one (adapter, unit) outcome — the $PNM per-landing pointer
// (spec C.5), carried in the receipt's JSON summary entry.
type Landing struct {
	AdapterID         string    `json:"adapterId"`
	Tier              string    `json:"tier"`
	ContentHash       string    `json:"contentHash"`
	Kind              Kind      `json:"kind"`
	ProviderKey       string    `json:"providerKey,omitempty"`
	ProviderVersionID string    `json:"providerVersionId,omitempty"`
	Size              int       `json:"size,omitempty"`
	Present           bool      `json:"present,omitempty"` // already had it (incremental skip)
	Stored            bool      `json:"stored,omitempty"`  // newly put this run
	Verified          bool      `json:"verified,omitempty"`
	Encrypted         bool      `json:"encrypted,omitempty"`
	ErrorCode         ErrorCode `json:"errorCode,omitempty"`
	Error             string    `json:"error,omitempty"`
}

// ReceiptUnit enumerates one unit that was in the run.
type ReceiptUnit struct {
	ContentHash string `json:"contentHash"`
	Kind        Kind   `json:"kind"`
	Size        int    `json:"size"`
	Meta        Meta   `json:"meta"`
}

// RunReceipt is the whole record of one backup run (spec C.5).
type RunReceipt struct {
	RunID       string        `json:"runId"`
	Node        string        `json:"node"`
	StartedAt   string        `json:"startedAt"`
	CompletedAt string        `json:"completedAt"`
	Status      RunStatus     `json:"status"`
	UnitCount   int           `json:"unitCount"`
	BytesTotal  int64         `json:"bytesTotal"`
	Units       []ReceiptUnit `json:"units"`
	Landings    []Landing     `json:"landings"`
}

// BuildReceiptMBL renders a run receipt as an $MBL: one ATTESTATION entry per
// unit (entry_id = content hash, so the manifest is enumerable by hash at a
// glance, spec D.2) plus one AUXILIARY JSON entry carrying the full receipt —
// the $PNM/$REC stand-in the spec sanctions (A.3/D.2), since neither type is in
// the vendored go lib.
func BuildReceiptMBL(r RunReceipt) ([]byte, error) {
	summary, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	entries := make([]mblEntry, 0, len(r.Units)+1)
	entries = append(entries, mblEntry{
		EntryID:   r.RunID,
		Role:      MBL.ModuleBundleEntryRoleAUXILIARY,
		Section:   sectionReceiptHeader,
		Encoding:  MBL.ModulePayloadEncodingJSON_UTF8,
		MediaType: mediaJSON,
		Payload:   summary,
		Desc:      string(r.Status),
	})
	for _, u := range r.Units {
		entries = append(entries, mblEntry{
			EntryID:  u.ContentHash,
			Role:     MBL.ModuleBundleEntryRoleATTESTATION,
			Section:  string(u.Kind),
			Encoding: MBL.ModulePayloadEncodingRAW_BYTES,
			Desc:     string(u.Kind),
		})
	}
	return buildMBL(moduleFormatReceipt, entries), nil
}

// ParseReceiptMBL recovers a RunReceipt from receipt $MBL bytes.
func ParseReceiptMBL(buf []byte) (RunReceipt, error) {
	format, entries, err := parseMBL(buf)
	if err != nil {
		return RunReceipt{}, err
	}
	if format != moduleFormatReceipt {
		return RunReceipt{}, fmt.Errorf("sdnbackup: not a backup receipt $MBL (module_format=%q)", format)
	}
	for _, e := range entries {
		if e.Section != sectionReceiptHeader {
			continue
		}
		var r RunReceipt
		if err := json.Unmarshal(e.Payload, &r); err != nil {
			return RunReceipt{}, fmt.Errorf("sdnbackup: decode receipt summary: %w", err)
		}
		return r, nil
	}
	return RunReceipt{}, errors.New("sdnbackup: receipt $MBL carries no summary entry")
}
