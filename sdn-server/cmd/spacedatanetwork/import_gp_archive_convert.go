package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	CATFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	MPEFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// The GP archive's records are built by the gp-archive-records WASM module
// (space-data-network-modules data-source/gp-archive-records) in ARCHIVE
// ENCODING V1: the byte layout the archive's content IDs depend on. A CID is
// sha256 over the record bytes, so the layout is a compatibility contract; the
// module reproduces it with the SDS schema-generated C++ builders, and its
// README states it. The importer parses, keys identities and keeps cursors;
// it hands the module each record's values as a $MPE / $CAT record in any
// layout, and the archive's bytes are the module's.

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
// fields the builder module reads. TestGPArchiveKeysMatchSDSSchemas
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
	fb     *flatbuffers.Builder
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
	return &gpWASMRecordBuilder{module: m, fb: flatbuffers.NewBuilder(256)}, hex.EncodeToString(sum[:]), nil
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
		b.buf = appendGPArchiveMPEStream(b.buf[:0], b.fb, records[start:end])
		built, err := b.invoke(ctx, "build_mpe", gpArchiveMPEType, end-start)
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
		b.buf = appendGPArchiveCATStream(b.buf[:0], b.fb, rows[start:end])
		built, err := b.invoke(ctx, "build_cat", gpArchiveCATType, end-start)
		if err != nil {
			return nil, err
		}
		out = append(out, built...)
	}
	return out, nil
}

// The "records" port's type for each method: a size-prefixed record stream.
var (
	gpArchiveMPEType = modulert.InvokeInputFrame{PortID: "records", SchemaName: "MPE.fbs", FileIdentifier: MPEFB.MPEIdentifier, RootTypeName: "MPE"}
	gpArchiveCATType = modulert.InvokeInputFrame{PortID: "records", SchemaName: "CAT.fbs", FileIdentifier: CATFB.CATIdentifier, RootTypeName: "CAT"}
)

func (b *gpWASMRecordBuilder) invoke(ctx context.Context, method string, frame modulert.InvokeInputFrame, want int) ([][]byte, error) {
	frame.Payload = b.buf
	// A started batch finishes: a half-built batch would only be rebuilt.
	outputs, err := b.module.InvokeMethodOutputs(context.WithoutCancel(ctx), method, []modulert.InvokeInputFrame{frame})
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

// The builder module's input is a stream of size-prefixed records,
// [u32le length][buffer] each (format in the module's README). Each record
// carries the values in place, as FlatBuffers stores them: strings as their
// bytes and doubles as their IEEE-754 bits, so every value reaches the builder
// exactly (negative zero, subnormals, and names that are not UTF-8 included).
// The module re-builds each one in archive encoding v1.

// gpArchiveMPESlots are the $MPE vtable slots of EPOCH and the mean elements,
// in gpArchiveElementKeys order after EPOCH.
var gpArchiveMPESlots = [...]flatbuffers.VOffsetT{1, 2, 3, 4, 5, 6, 7, 8}

// appendGPArchiveMPEStream appends one $MPE per record. Every element is
// written, defaults included (the generated adders leave a 0.0 out, and an
// omitted 0.0 cannot be told from -0.0); the module refuses a record that
// lacks one.
func appendGPArchiveMPEStream(dst []byte, b *flatbuffers.Builder, records []gpArchiveRecord) []byte {
	for i := range records {
		r := &records[i]
		b.Reset()
		entity := b.CreateString(r.EntityID)
		MPEFB.MPEStart(b)
		MPEFB.MPEAddENTITY_ID(b, entity)
		values, elements := [len(gpArchiveMPESlots)]float64{r.Epoch}, r.elements()
		copy(values[1:], elements[:])
		for j, slot := range gpArchiveMPESlots {
			b.PrependFloat64(values[j])
			b.Slot(int(slot))
		}
		b.FinishSizePrefixedWithFileIdentifier(MPEFB.MPEEnd(b), []byte(MPEFB.MPEIdentifier))
		dst = append(dst, b.FinishedBytes()...)
	}
	return dst
}

// appendGPArchiveCATStream appends one $CAT per row: OBJECT_ID, NORAD_CAT_ID
// and OBJECT_NAME.
func appendGPArchiveCATStream(dst []byte, b *flatbuffers.Builder, rows []gpArchiveCATInput) []byte {
	for _, row := range rows {
		b.Reset()
		id, name := b.CreateString(row.EntityID), b.CreateString(row.Name)
		CATFB.CATStart(b)
		CATFB.CATAddOBJECT_ID(b, id)
		CATFB.CATAddNORAD_CAT_ID(b, row.NORAD)
		CATFB.CATAddOBJECT_NAME(b, name)
		b.FinishSizePrefixedWithFileIdentifier(CATFB.CATEnd(b), []byte(CATFB.CATIdentifier))
		dst = append(dst, b.FinishedBytes()...)
	}
	return dst
}
