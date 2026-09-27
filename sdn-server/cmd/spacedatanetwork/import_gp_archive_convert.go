package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// The GP archive's records are built by the gp-archive-records WASM module
// (space-data-network-modules data-source/gp-archive-records) in ARCHIVE
// ENCODING V1: the byte layout the archive's content IDs depend on. A CID is
// sha256 over the record bytes, so the layout is a compatibility contract; the
// module reproduces it with the SDS schema-generated C++ builders, and its
// README states it. The importer only parses, keys identities and keeps
// cursors; it never lays out a record.

const (
	gpArchiveRecordBuilderPluginID = "com.digitalarsenal.data-source.gp-archive-records"
	gpArchiveRecordBuilderFile     = "gp-archive-records.wasm"
	// Records per module call: ~330 kB in, ~560 kB out.
	gpArchiveRecordBuildBatchDefault = 4096
)

// gpArchiveRecordBuildBatch is a variable so tests can force batch boundaries.
var gpArchiveRecordBuildBatch = gpArchiveRecordBuildBatchDefault

// Record keys, in one table. The Space-Track GP keys are CCSDS OMM keywords
// and the SATCAT keys are Space-Track's; the $MPE and $CAT keys are the SDS
// field names the builder module takes. TestGPArchiveKeysMatchSDSSchemas
// checks every key against the pinned SDS schemas (OMM, MPE, CAT) and the
// JSON struct tags against this table.
const (
	gpKeyObjectID        = "OBJECT_ID"
	gpKeyObjectName      = "OBJECT_NAME"
	gpKeyNORADCatID      = "NORAD_CAT_ID"
	gpKeyCreationDate    = "CREATION_DATE"
	gpKeyEpoch           = "EPOCH"
	gpKeyMeanMotion      = "MEAN_MOTION"
	gpKeyEccentricity    = "ECCENTRICITY"
	gpKeyInclination     = "INCLINATION"
	gpKeyRAOfAscNode     = "RA_OF_ASC_NODE"
	gpKeyArgOfPericenter = "ARG_OF_PERICENTER"
	gpKeyMeanAnomaly     = "MEAN_ANOMALY"
	gpKeyBSTAR           = "BSTAR"
	gpKeyEntityID        = "ENTITY_ID"
	// Space-Track keys with no SDS field: the GP record serial, and the
	// SATCAT international designator and legacy name columns.
	gpKeyGPID    = "GP_ID"
	gpKeyINTLDES = "INTLDES"
	gpKeySATNAME = "SATNAME"
)

// gpArchiveElementKeys are the mean elements in archive order: the order the
// GP sources are validated in and the order the builder module adds them.
var gpArchiveElementKeys = [...]string{gpKeyMeanMotion, gpKeyEccentricity, gpKeyInclination, gpKeyRAOfAscNode, gpKeyArgOfPericenter, gpKeyMeanAnomaly, gpKeyBSTAR}

func (r *gpArchiveRecord) elements() [len(gpArchiveElementKeys)]float64 {
	return [...]float64{r.MeanMotion, r.Eccentricity, r.Inclination, r.RAOfAscNode, r.ArgOfPericenter, r.MeanAnomaly, r.BSTAR}
}

// gpArchiveCATInput is one catalog row for build_cat.
type gpArchiveCATInput struct {
	EntityID string
	NORAD    uint32
	Name     string
}

type gpArchiveRecordBuilder interface {
	// BuildMPE returns one archive-v1 $MPE per record (keyed by EntityID).
	BuildMPE(ctx context.Context, records []gpArchiveRecord) ([][]byte, error)
	// BuildCAT returns one archive-v1 $CAT per row.
	BuildCAT(ctx context.Context, rows []gpArchiveCATInput) ([][]byte, error)
	Close() error
}

type gpWASMRecordBuilder struct {
	module *modulert.Module
	buf    []byte
}

// defaultGPArchiveRecordBuilderPath is gp-archive-records.wasm beside the
// running executable, where install.sh puts it.
func defaultGPArchiveRecordBuilderPath() string {
	exe, err := os.Executable()
	if err != nil {
		return gpArchiveRecordBuilderFile
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), gpArchiveRecordBuilderFile)
}

// loadGPArchiveRecordBuilder loads the builder module and returns it with the
// sha256 of the artifact bytes.
func loadGPArchiveRecordBuilder(path string) (*gpWASMRecordBuilder, string, error) {
	wasm, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read record builder module: %w (build data-source/gp-archive-records and pass --record-builder-module)", err)
	}
	sum := sha256.Sum256(wasm)
	m, err := modulert.NewModule(wasm, modulert.NewCapabilityRegistry(), &modulert.NodeContext{})
	if err != nil {
		return nil, "", fmt.Errorf("load record builder module %s: %w", path, err)
	}
	if id := m.ID(); id != gpArchiveRecordBuilderPluginID {
		m.Close()
		return nil, "", fmt.Errorf("record builder module %s is %q, want %q", path, id, gpArchiveRecordBuilderPluginID)
	}
	return &gpWASMRecordBuilder{module: m}, hex.EncodeToString(sum[:]), nil
}

func (b *gpWASMRecordBuilder) Close() error {
	if b == nil || b.module == nil {
		return nil
	}
	return b.module.Close()
}

func (b *gpWASMRecordBuilder) BuildMPE(ctx context.Context, records []gpArchiveRecord) ([][]byte, error) {
	out := make([][]byte, 0, len(records))
	for start := 0; start < len(records); start += gpArchiveRecordBuildBatch {
		end := min(start+gpArchiveRecordBuildBatch, len(records))
		b.buf = appendGPArchiveMPEInput(b.buf[:0], records[start:end])
		built, err := b.invoke(ctx, "build_mpe", end-start)
		if err != nil {
			return nil, err
		}
		out = append(out, built...)
	}
	return out, nil
}

func (b *gpWASMRecordBuilder) BuildCAT(ctx context.Context, rows []gpArchiveCATInput) ([][]byte, error) {
	out := make([][]byte, 0, len(rows))
	for start := 0; start < len(rows); start += gpArchiveRecordBuildBatch {
		end := min(start+gpArchiveRecordBuildBatch, len(rows))
		b.buf = appendGPArchiveCATInput(b.buf[:0], rows[start:end])
		built, err := b.invoke(ctx, "build_cat", end-start)
		if err != nil {
			return nil, err
		}
		out = append(out, built...)
	}
	return out, nil
}

func (b *gpWASMRecordBuilder) invoke(ctx context.Context, method string, want int) ([][]byte, error) {
	// A started batch finishes: a half-built batch would only be rebuilt.
	outputs, err := b.module.InvokeMethodOutputs(context.WithoutCancel(ctx), method, []modulert.InvokeInputFrame{{PortID: "records", WireFormat: 1, RequiredAlignment: 1, Alignment: 1, ByteLength: uint32(len(b.buf)), Payload: b.buf}})
	if err != nil {
		return nil, fmt.Errorf("record builder %s: %w", method, err)
	}
	var stream []byte
	for _, o := range outputs {
		if o.PortID == "records" {
			stream = o.Payload
		}
	}
	records, err := splitSizePrefixedAligned(stream)
	if err != nil {
		return nil, fmt.Errorf("record builder %s output: %w", method, err)
	}
	if len(records) != want {
		return nil, fmt.Errorf("record builder %s returned %d records for %d inputs", method, len(records), want)
	}
	return records, nil
}

// The builder module's input is a "field batch" (format in the module's
// README): a header naming the SDS fields of the record, then each record's
// values in header order, strings as length-prefixed bytes and doubles as
// their IEEE-754 bits. Nothing is formatted or re-parsed on the way, so every
// value reaches the builder exactly (negative zero, subnormals, and names that
// are not UTF-8 included).
var (
	gpArchiveMPEFields = append([]string{gpKeyEntityID, gpKeyEpoch}, gpArchiveElementKeys[:]...)
	gpArchiveCATFields = []string{gpKeyObjectID, gpKeyNORADCatID, gpKeyObjectName}
)

func appendGPArchiveFieldBatchHeader(dst []byte, fields []string, count int) []byte {
	dst = append(dst, "GPAF"...)
	dst = binary.LittleEndian.AppendUint16(dst, 1)
	dst = binary.LittleEndian.AppendUint16(dst, uint16(len(fields)))
	for _, f := range fields {
		dst = append(dst, byte(len(f)))
		dst = append(dst, f...)
	}
	return binary.LittleEndian.AppendUint32(dst, uint32(count))
}

func appendGPArchiveString(dst []byte, s string) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(s)))
	return append(dst, s...)
}

func appendGPArchiveDouble(dst []byte, v float64) []byte {
	return binary.LittleEndian.AppendUint64(dst, math.Float64bits(v))
}

// appendGPArchiveMPEInput writes the build_mpe field batch (fields in
// gpArchiveMPEFields order).
func appendGPArchiveMPEInput(dst []byte, records []gpArchiveRecord) []byte {
	dst = appendGPArchiveFieldBatchHeader(dst, gpArchiveMPEFields, len(records))
	for i := range records {
		r := &records[i]
		dst = appendGPArchiveString(dst, r.EntityID)
		dst = appendGPArchiveDouble(dst, r.Epoch)
		for _, v := range r.elements() {
			dst = appendGPArchiveDouble(dst, v)
		}
	}
	return dst
}

// appendGPArchiveCATInput writes the build_cat field batch (fields in
// gpArchiveCATFields order).
func appendGPArchiveCATInput(dst []byte, rows []gpArchiveCATInput) []byte {
	dst = appendGPArchiveFieldBatchHeader(dst, gpArchiveCATFields, len(rows))
	for _, row := range rows {
		dst = appendGPArchiveString(dst, row.EntityID)
		dst = binary.LittleEndian.AppendUint32(dst, row.NORAD)
		dst = appendGPArchiveString(dst, row.Name)
	}
	return dst
}
