package ingest

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	OEMFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/OEM"
	flatbuffers "github.com/google/flatbuffers/go"
)

// Supplemental Space-Track ingest lanes (App 2 / A2.2c-ST).
//
// Two lanes, both gated by the SpaceTrackEnabled master switch (a deploy-time
// decision) and running behind a single shared rate limiter (rateLimiter):
//
//  1. publicfiles operator-ephemeris lane: authenticated enumerate
//     (publicfiles/query/class/dirs + loadpublicdata) -> download NEW files
//     only (dedupe by name+date) -> unpack (nested zips) -> normalize CCSDS
//     OEM XML to canonical OEM FlatBuffer records (frame + time system
//     preserved AS DECLARED) -> store through the same storage path the
//     CelesTrak ingest uses, with source metadata and SOURCE_SHA256 of the raw
//     downloaded bytes.
//
//  2. current-gp lane: full-catalog CCSDS OMM JSON snapshots
//     (basicspacedata/query/class/gp/.../format/json) -> schema-exact OMM +
//     MPE records via the shared ingestGPRows core. Feeds A2.7 catalog
//     synthesis.
//
// RESOLVED live 2026-07-13 (A2.2c-ST): `publicfiles/query/class/dirs` returns
// directory slug strings `public-data-files-<ID>-<slug>-prod`;
// `publicfiles/query/class/loadpublicdata` is a FLAT listing of every file the
// account can access (NOT per-directory — a `?id=<dir>` query is ignored),
// with fields {source,type,date,size,link,name}. New provider files (e.g.
// Kuiper 24206, SpaceX 552 — empty for this account today) appear in that same
// flat listing when shared, so no per-directory query or code change is needed
// to onboard them. Downloads use `publicfiles/query/class/download?name=<file>`
// (the `name` is a query PARAMETER, not a path predicate).
const (
	defaultSpaceTrackPublicFilesBaseURL = "https://www.space-track.org/publicfiles/query/class"
	defaultSpaceTrackCurrentGPQueryURL  = "https://www.space-track.org/basicspacedata/query/class/gp/DECAY_DATE/null-val/orderby/NORAD_CAT_ID%20asc/format/json"

	spaceTrackProviderID = "space-track"

	spaceTrackPublicFilesSource = "spacetrack-publicfiles"
	spaceTrackCurrentGPSource   = "spacetrack-gp"

	parserVersionSpaceTrackPublicFilesOEM = "spacetrack-publicfiles-oem/v1"
	parserVersionSpaceTrackCurrentGP      = "spacetrack-gp/v1"

	spaceTrackUserAgent = "spacedatanetwork-ingest/1.0 (App2 supplemental OMM; +https://spacedatanetwork.com)"

	// Rate limits sit safely under Space-Track's documented hard caps
	// (<30/min, <300/hr). Enforced inside the lane, not the caller's discipline.
	spaceTrackPerMinuteCap = 25
	spaceTrackPerHourCap   = 250

	maxSpaceTrackResponseBytes = 256 * 1024 * 1024
	maxUnzipDepth              = 4
	maxUnzipTotalBytes         = 256 * 1024 * 1024

	// FlatBuffers vtable slot indices from the generated (but unexported)
	// OEM.ephemerisDataBlock / OEM.ephemerisDataLine builders. Kept here
	// because the generated Add*/Start helpers for those two lowercase-named
	// tables are not exported, so the block/line sub-tables must be assembled
	// with the raw flatbuffers.Builder primitives.
	oemBlockNumFields   = 19
	oemBlockSlotComment = 0
	oemBlockSlotObject  = 1
	oemBlockSlotCenter  = 2
	oemBlockSlotRefFrm  = 3
	oemBlockSlotRefEpo  = 4
	oemBlockSlotTimeSys = 6
	oemBlockSlotStart   = 7
	oemBlockSlotStop    = 10
	oemBlockSlotStep    = 13
	oemBlockSlotSVSize  = 14
	oemBlockSlotLines   = 16

	oemLineNumFields = 10
	oemLineSlotEpoch = 0
	oemLineSlotX     = 1
	oemLineSlotY     = 2
	oemLineSlotZ     = 3
	oemLineSlotXDot  = 4
	oemLineSlotYDot  = 5
	oemLineSlotZDot  = 6
	oemLineSlotXDdot = 7
	oemLineSlotYDdot = 8
	oemLineSlotZDdot = 9
)

// syncSpaceTrackSupplemental logs in once and drives both supplemental lanes.
func (r *Runner) syncSpaceTrackSupplemental(ctx context.Context) error {
	if !r.cfg.SpaceTrackEnabled {
		return nil
	}
	if !r.cfg.SpaceTrackPublicFilesEnabled && !r.cfg.SpaceTrackCurrentGPEnabled {
		return nil
	}
	if r.cfg.SpaceTrackIdentity == "" || r.cfg.SpaceTrackPassword == "" {
		log.Warn("Space-Track credentials missing; skipping supplemental sync")
		return nil
	}
	if err := r.requireFreeDisk("Space-Track supplemental sync"); err != nil {
		return err
	}

	// Single login for both lanes; the cookie jar is shared on the runner's
	// HTTP client.
	if err := r.stLimiter.Wait(ctx); err != nil {
		return err
	}
	if err := r.spaceTrackLogin(ctx); err != nil {
		return err
	}

	var errs []string
	if r.cfg.SpaceTrackPublicFilesEnabled {
		if err := r.syncSpaceTrackPublicFiles(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("publicfiles: %v", err))
		}
	}
	if err := ctx.Err(); err != nil {
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "; "))
		}
		return err
	}
	if r.cfg.SpaceTrackCurrentGPEnabled {
		if err := r.syncSpaceTrackCurrentGP(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("current-gp: %v", err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// spaceTrackGet performs one rate-limited authenticated GET. It HALTS on any
// non-200 (Space-Track M2M rule) by returning an error.
func (r *Runner) spaceTrackGet(ctx context.Context, sourceURL string) ([]byte, fetchMetadata, error) {
	if err := r.stLimiter.Wait(ctx); err != nil {
		return nil, fetchMetadata{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, fetchMetadata{}, err
	}
	req.Header.Set("User-Agent", spaceTrackUserAgent)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fetchMetadata{}, err
	}
	defer resp.Body.Close()

	metadata := fetchMetadata{
		SourceURL:    sourceURL,
		HTTPStatus:   resp.StatusCode,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		ContentType:  resp.Header.Get("Content-Type"),
		RetrievedAt:  time.Now().UTC(),
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, metadata, fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSpaceTrackResponseBytes))
	return data, metadata, err
}

// -------------------------------------------------------------------------
// Lane 1: publicfiles operator ephemeris (CCSDS OEM)
// -------------------------------------------------------------------------

// publicFile is one entry from publicfiles/query/class/loadpublicdata.
type publicFile struct {
	Source string `json:"source"`
	Type   string `json:"type"`
	Date   string `json:"date"`
	Size   string `json:"size"`
	Link   string `json:"link"`
	Name   string `json:"name"`
}

func (r *Runner) syncSpaceTrackPublicFiles(ctx context.Context) error {
	base := strings.TrimRight(r.cfg.SpaceTrackPublicFilesBaseURL, "/")

	// 1. Enumerate directories (observability + future-source discovery). New
	//    sources appear here and in loadpublicdata without any code change.
	dirsData, _, err := r.spaceTrackGet(ctx, base+"/dirs")
	if err != nil {
		return fmt.Errorf("fetch dirs: %w", err)
	}
	dirs, err := parsePublicFilesDirs(dirsData)
	if err != nil {
		return fmt.Errorf("parse dirs: %w", err)
	}
	log.Infof("Space-Track publicfiles dirs: %d [%s]", len(dirs), strings.Join(dirs, ", "))

	// 2. Flat listing of all accessible files.
	listData, listMeta, err := r.spaceTrackGet(ctx, base+"/loadpublicdata")
	if err != nil {
		return fmt.Errorf("fetch loadpublicdata: %w", err)
	}
	files, err := parsePublicFilesListing(listData)
	if err != nil {
		return fmt.Errorf("parse loadpublicdata: %w", err)
	}

	var totalOEM, processed, skipped int
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !isOEMEphemerisFile(f) {
			skipped++
			continue
		}
		if r.publicFileAlreadyIngested(f) {
			skipped++
			continue
		}
		n, err := r.ingestPublicFile(ctx, base, f, listMeta)
		if err != nil {
			r.recordIngestFailureForReview(spaceTrackPublicFilesSource, err)
			return fmt.Errorf("ingest %s: %w", f.Name, err)
		}
		totalOEM += n
		processed++
	}

	log.Infof("Space-Track publicfiles sync complete: dirs=%d files_processed=%d skipped=%d OEM=%d",
		len(dirs), processed, skipped, totalOEM)
	r.checkpoints.setString("spacetrack_publicfiles_last_success", time.Now().UTC().Format(time.RFC3339))
	if err := r.checkpoints.save(); err != nil {
		log.Warnf("Failed to persist checkpoints: %v", err)
	}
	return nil
}

func (r *Runner) ingestPublicFile(ctx context.Context, base string, f publicFile, listMeta fetchMetadata) (int, error) {
	dlURL := base + "/download?name=" + url.QueryEscape(f.Name)
	zipData, meta, err := r.spaceTrackGet(ctx, dlURL)
	if err != nil {
		return 0, err
	}

	if err := r.archiveRaw("spacetrack-publicfiles", safePublicFileName(f.Name), zipData); err != nil {
		log.Warnf("Failed to archive Space-Track public file %s: %v", f.Name, err)
	}

	xmls, warnings, err := extractOEMXMLs(zipData)
	if err != nil {
		return 0, fmt.Errorf("unpack: %w", err)
	}
	if len(xmls) == 0 {
		return 0, fmt.Errorf("no OEM XML found in archive")
	}

	tags := sourceTags(spaceTrackProviderID, spaceTrackPublicFilesSource, meta.SourceURL, zipData)
	normalized := sha256.New()
	count := 0
	for _, xmlBytes := range xmls {
		docs, err := parseOEMXML(xmlBytes)
		if err != nil {
			return count, fmt.Errorf("parse OEM XML: %w", err)
		}
		for _, doc := range docs {
			oemBytes, w := buildOEMRecord(doc)
			warnings = append(warnings, w...)
			if _, err := r.storeIngestRecord("OEM.fbs", oemBytes, "source:spacetrack", tags); err != nil {
				return count, err
			}
			writeNormalizedHashRecord(normalized, "OEM.fbs", oemBytes)
			count++
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("no OEM segments parsed")
	}

	if err := r.recordIngestBatchProvenanceDetailed(spaceTrackPublicFilesSource, zipData, meta,
		parserVersionSpaceTrackPublicFilesOEM, hex.EncodeToString(normalized.Sum(nil)),
		map[string]int{"OEM.fbs": count}, warnings, nil); err != nil {
		log.Warnf("Failed to record Space-Track publicfiles provenance: %v", err)
	}

	// Dedup marker: name + upstream date. Reprocessed only if the file's date
	// changes (a new ephemeris also gets a new timestamped name).
	r.checkpoints.setString(publicFileSeenKey(f.Name), f.Date)
	if err := r.checkpoints.save(); err != nil {
		log.Warnf("Failed to persist checkpoints: %v", err)
	}

	log.Infof("Space-Track publicfiles ingested %s (source=%s type=%s): OEM=%d", f.Name, f.Source, f.Type, count)
	return count, nil
}

func parsePublicFilesDirs(data []byte) ([]string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var dirs []string
	if err := json.Unmarshal(trimmed, &dirs); err != nil {
		return nil, fmt.Errorf("decode dirs JSON: %w", err)
	}
	return dirs, nil
}

func parsePublicFilesListing(data []byte) ([]publicFile, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var files []publicFile
	if err := json.Unmarshal(trimmed, &files); err != nil {
		return nil, fmt.Errorf("decode loadpublicdata JSON: %w", err)
	}
	return files, nil
}

// isOEMEphemerisFile keeps any non-ReadMe .zip so new providers/types onboard
// without code changes.
func isOEMEphemerisFile(f publicFile) bool {
	name := strings.ToLower(strings.TrimSpace(f.Name))
	if name == "" || !strings.HasSuffix(name, ".zip") {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(f.Type), "ReadMe") {
		return false
	}
	return true
}

func publicFileSeenKey(name string) string {
	return "spacetrack_publicfiles_seen_" + checkpointSafeKey(name)
}

func (r *Runner) publicFileAlreadyIngested(f publicFile) bool {
	if strings.TrimSpace(f.Date) == "" {
		return false
	}
	return r.checkpoints.getString(publicFileSeenKey(f.Name)) == f.Date
}

func safePublicFileName(name string) string {
	repl := strings.NewReplacer(":", "_", "/", "_", "\\", "_", " ", "_")
	cleaned := repl.Replace(strings.TrimSpace(name))
	if cleaned == "" {
		return "publicfile.zip"
	}
	return cleaned
}

// extractOEMXMLs recursively unpacks a (possibly nested) zip and returns every
// OEM XML payload found. Non-OEM entries (e.g. TrajectorySummary.txt) are
// ignored with a warning. Depth and total-size are capped against zip bombs.
func extractOEMXMLs(data []byte) ([][]byte, []string, error) {
	var out [][]byte
	var warnings []string
	var total int64

	var walk func(b []byte, depth int) error
	walk = func(b []byte, depth int) error {
		if depth > maxUnzipDepth {
			return fmt.Errorf("zip nesting exceeds %d levels", maxUnzipDepth)
		}
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return fmt.Errorf("open zip (depth %d): %w", depth, err)
		}
		for _, zf := range zr.File {
			if zf.FileInfo().IsDir() {
				continue
			}
			rc, err := zf.Open()
			if err != nil {
				return fmt.Errorf("open zip entry %s: %w", zf.Name, err)
			}
			content, err := io.ReadAll(io.LimitReader(rc, maxUnzipTotalBytes+1))
			rc.Close()
			if err != nil {
				return fmt.Errorf("read zip entry %s: %w", zf.Name, err)
			}
			total += int64(len(content))
			if total > maxUnzipTotalBytes {
				return fmt.Errorf("unpacked payload exceeds %d bytes", maxUnzipTotalBytes)
			}
			lower := strings.ToLower(zf.Name)
			switch {
			case strings.HasSuffix(lower, ".zip"):
				if err := walk(content, depth+1); err != nil {
					return err
				}
			case strings.HasSuffix(lower, ".xml"), bytes.Contains(content, []byte("<oem")):
				out = append(out, content)
			default:
				warnings = append(warnings, "ignored non-OEM entry "+zf.Name)
			}
		}
		return nil
	}

	if err := walk(data, 0); err != nil {
		return nil, warnings, err
	}
	return out, warnings, nil
}

// -------------------------------------------------------------------------
// CCSDS OEM XML parsing (NDM v1.0/2.0 layout)
// -------------------------------------------------------------------------

type oemNDM struct {
	XMLName xml.Name `xml:"ndm"`
	OEMs    []oemDoc `xml:"oem"`
}

type oemDoc struct {
	XMLName xml.Name  `xml:"oem"`
	ID      string    `xml:"id,attr"`
	Version string    `xml:"version,attr"`
	Header  oemHeader `xml:"header"`
	Body    oemBody   `xml:"body"`
}

type oemHeader struct {
	Classification string `xml:"CLASSIFICATION"`
	CreationDate   string `xml:"CREATION_DATE"`
	Originator     string `xml:"ORIGINATOR"`
}

type oemBody struct {
	Segments []oemSegment `xml:"segment"`
}

type oemSegment struct {
	Metadata oemMetadata `xml:"metadata"`
	Data     oemData     `xml:"data"`
}

type oemMetadata struct {
	ObjectName    string `xml:"OBJECT_NAME"`
	ObjectID      string `xml:"OBJECT_ID"`
	CenterName    string `xml:"CENTER_NAME"`
	RefFrame      string `xml:"REF_FRAME"`
	RefFrameEpoch string `xml:"REF_FRAME_EPOCH"`
	TimeSystem    string `xml:"TIME_SYSTEM"`
	StartTime     string `xml:"START_TIME"`
	StopTime      string `xml:"STOP_TIME"`
}

type oemData struct {
	Comments     []string         `xml:"COMMENT"`
	StateVectors []oemStateVector `xml:"stateVector"`
}

type oemStateVector struct {
	Epoch string  `xml:"EPOCH"`
	X     float64 `xml:"X"`
	Y     float64 `xml:"Y"`
	Z     float64 `xml:"Z"`
	XDot  float64 `xml:"X_DOT"`
	YDot  float64 `xml:"Y_DOT"`
	ZDot  float64 `xml:"Z_DOT"`
	XDdot float64 `xml:"X_DDOT"`
	YDdot float64 `xml:"Y_DDOT"`
	ZDdot float64 `xml:"Z_DDOT"`
}

// parseOEMXML returns one oemDoc per <oem> element. It accepts either an <ndm>
// wrapper (NASA-JSC layout) or a bare <oem> root.
func parseOEMXML(data []byte) ([]oemDoc, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty OEM XML")
	}
	var ndm oemNDM
	if err := xml.Unmarshal(trimmed, &ndm); err == nil && len(ndm.OEMs) > 0 {
		return ndm.OEMs, nil
	}
	var doc oemDoc
	if err := xml.Unmarshal(trimmed, &doc); err != nil {
		return nil, fmt.Errorf("decode OEM XML: %w", err)
	}
	if len(doc.Body.Segments) == 0 {
		return nil, fmt.Errorf("OEM XML has no segments")
	}
	return []oemDoc{doc}, nil
}

// -------------------------------------------------------------------------
// Canonical OEM FlatBuffer construction
// -------------------------------------------------------------------------

// buildOEMRecord serializes one <oem> element to a size-prefixed OEM
// FlatBuffer with the "$OEM" file identifier (matching the OMM/MPE/SPW ingest
// builders). Reference frame and time system are preserved AS DECLARED: the
// declared REF_FRAME string is stored verbatim in RFM.NAME (union type NONE,
// i.e. no frame transform is asserted), and TIME_SYSTEM maps to the CCSDS
// timingStandard enum.
func buildOEMRecord(doc oemDoc) ([]byte, []string) {
	b := flatbuffers.NewBuilder(8192)
	var warnings []string

	blockOffsets := make([]flatbuffers.UOffsetT, 0, len(doc.Body.Segments))
	for _, seg := range doc.Body.Segments {
		off, w := buildOEMBlock(b, seg)
		warnings = append(warnings, w...)
		blockOffsets = append(blockOffsets, off)
	}

	blocksVec := prependOffsetVector(b, blockOffsets)

	classOff := b.CreateString(strings.TrimSpace(doc.Header.Classification))
	creationOff := b.CreateString(normalizeOEMTime(doc.Header.CreationDate))
	origOff := b.CreateString(strings.TrimSpace(doc.Header.Originator))

	OEMFB.OEMStart(b)
	OEMFB.OEMAddCLASSIFICATION(b, classOff)
	if v, ok := parseFloat(doc.Version); ok {
		OEMFB.OEMAddCCSDS_OEM_VERS(b, v)
	}
	OEMFB.OEMAddCREATION_DATE(b, creationOff)
	OEMFB.OEMAddORIGINATOR(b, origOff)
	OEMFB.OEMAddEPHEMERIS_DATA_BLOCK(b, blocksVec)
	oem := OEMFB.OEMEnd(b)
	OEMFB.FinishSizePrefixedOEMBuffer(b, oem)

	out := make([]byte, len(b.FinishedBytes()))
	copy(out, b.FinishedBytes())
	return out, warnings
}

// buildOEMBlock assembles one ephemerisDataBlock. The ephemerisDataBlock and
// ephemerisDataLine tables are constructed from raw builder primitives because
// their generated Add*/Start helpers are unexported (lowercase schema table
// names); slot indices come from the generated OEM code (see const block).
func buildOEMBlock(b *flatbuffers.Builder, seg oemSegment) (flatbuffers.UOffsetT, []string) {
	var warnings []string

	// Ephemeris data lines (explicit-epoch form; STEP_SIZE stays 0).
	lineOffsets := make([]flatbuffers.UOffsetT, 0, len(seg.Data.StateVectors))
	for _, sv := range seg.Data.StateVectors {
		iso, err := normalizeOEMEpochStrict(sv.Epoch)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("state-vector epoch %q preserved raw: %v", sv.Epoch, err))
			iso = strings.TrimSpace(sv.Epoch)
		}
		epochOff := b.CreateString(iso)
		b.StartObject(oemLineNumFields)
		b.PrependUOffsetTSlot(oemLineSlotEpoch, epochOff, 0)
		b.PrependFloat64Slot(oemLineSlotX, sv.X, 0)
		b.PrependFloat64Slot(oemLineSlotY, sv.Y, 0)
		b.PrependFloat64Slot(oemLineSlotZ, sv.Z, 0)
		b.PrependFloat64Slot(oemLineSlotXDot, sv.XDot, 0)
		b.PrependFloat64Slot(oemLineSlotYDot, sv.YDot, 0)
		b.PrependFloat64Slot(oemLineSlotZDot, sv.ZDot, 0)
		b.PrependFloat64Slot(oemLineSlotXDdot, sv.XDdot, 0)
		b.PrependFloat64Slot(oemLineSlotYDdot, sv.YDdot, 0)
		b.PrependFloat64Slot(oemLineSlotZDdot, sv.ZDdot, 0)
		lineOffsets = append(lineOffsets, b.EndObject())
	}
	linesVec := prependOffsetVector(b, lineOffsets)

	// OBJECT (embedded CAT) and REFERENCE_FRAME (RFM), both via exported
	// builders.
	objNameOff := b.CreateString(strings.TrimSpace(seg.Metadata.ObjectName))
	objIDOff := b.CreateString(strings.TrimSpace(seg.Metadata.ObjectID))
	OEMFB.CATStart(b)
	OEMFB.CATAddOBJECT_NAME(b, objNameOff)
	OEMFB.CATAddOBJECT_ID(b, objIDOff)
	objOff := OEMFB.CATEnd(b)

	frameNameOff := b.CreateString(strings.TrimSpace(seg.Metadata.RefFrame))
	OEMFB.RFMStart(b)
	OEMFB.RFMAddNAME(b, frameNameOff) // union type left NONE: frame preserved as-declared, no transform asserted
	frameOff := OEMFB.RFMEnd(b)

	commentOff := b.CreateString(strings.Join(seg.Data.Comments, "\n"))
	centerOff := b.CreateString(strings.TrimSpace(seg.Metadata.CenterName))
	startOff := b.CreateString(normalizeOEMTime(seg.Metadata.StartTime))
	stopOff := b.CreateString(normalizeOEMTime(seg.Metadata.StopTime))

	timeSys, tsOK := oemTimeSystemEnum(seg.Metadata.TimeSystem)
	if !tsOK && strings.TrimSpace(seg.Metadata.TimeSystem) != "" {
		warnings = append(warnings, fmt.Sprintf("unmapped TIME_SYSTEM %q left at schema default", seg.Metadata.TimeSystem))
	}

	b.StartObject(oemBlockNumFields)
	b.PrependUOffsetTSlot(oemBlockSlotComment, commentOff, 0)
	b.PrependUOffsetTSlot(oemBlockSlotObject, objOff, 0)
	b.PrependUOffsetTSlot(oemBlockSlotCenter, centerOff, 0)
	b.PrependUOffsetTSlot(oemBlockSlotRefFrm, frameOff, 0)
	if refEpoch := strings.TrimSpace(seg.Metadata.RefFrameEpoch); refEpoch != "" {
		refEpochOff := b.CreateString(normalizeOEMTime(refEpoch))
		b.PrependUOffsetTSlot(oemBlockSlotRefEpo, refEpochOff, 0)
	}
	if tsOK {
		b.PrependInt8Slot(oemBlockSlotTimeSys, timeSys, 0)
	}
	b.PrependUOffsetTSlot(oemBlockSlotStart, startOff, 0)
	b.PrependUOffsetTSlot(oemBlockSlotStop, stopOff, 0)
	// STEP_SIZE (slot 13) stays 0 to signal explicit-epoch lines;
	// STATE_VECTOR_SIZE (slot 14) stays the schema default (6).
	b.PrependUOffsetTSlot(oemBlockSlotLines, linesVec, 0)
	return b.EndObject(), warnings
}

// prependOffsetVector builds a FlatBuffers vector of table offsets.
func prependOffsetVector(b *flatbuffers.Builder, offsets []flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	b.StartVector(4, len(offsets), 4)
	for i := len(offsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	return b.EndVector(len(offsets))
}

// oemTimeSystemEnum maps a declared TIME_SYSTEM string to the CCSDS
// timingStandard enum int8 value. Returns ok=false when unrecognized.
func oemTimeSystemEnum(raw string) (int8, bool) {
	key := strings.ToUpper(strings.TrimSpace(raw))
	if v, ok := OEMFB.EnumValuestimingStandard[key]; ok {
		return int8(v), true
	}
	return 0, false
}

// -------------------------------------------------------------------------
// CCSDS time handling (day-of-year and calendar forms)
// -------------------------------------------------------------------------

// normalizeOEMTime converts a CCSDS timestamp to RFC3339 UTC, preserving the
// raw string when it cannot be parsed (metadata fields, best effort).
func normalizeOEMTime(raw string) string {
	if s, err := normalizeOEMEpochStrict(raw); err == nil {
		return s
	}
	return strings.TrimSpace(raw)
}

func normalizeOEMEpochStrict(raw string) (string, error) {
	t, err := parseCCSDSTime(raw)
	if err != nil {
		return "", err
	}
	return t.UTC().Format(time.RFC3339Nano), nil
}

// parseCCSDSTime parses both CCSDS day-of-year epochs (YYYY-DDDThh:mm:ss[.f]Z)
// and ISO calendar epochs, always returning UTC. DOY form is what NASA-JSC OEM
// files use and is not handled by the calendar-only parseEpoch.
func parseCCSDSTime(raw string) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	ti := strings.IndexAny(s, "Tt")
	if ti < 0 {
		return parseEpoch(s)
	}
	datePart := s[:ti]
	timePart := s[ti+1:]
	// CCSDS epochs are UTC; drop a trailing Z. A non-Z offset falls through to
	// the calendar parser below.
	if strings.HasSuffix(timePart, "Z") || strings.HasSuffix(timePart, "z") {
		timePart = timePart[:len(timePart)-1]
	}

	if strings.Count(datePart, "-") == 1 {
		fields := strings.SplitN(datePart, "-", 2)
		year, err1 := strconv.Atoi(strings.TrimSpace(fields[0]))
		doy, err2 := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err1 != nil || err2 != nil {
			return time.Time{}, fmt.Errorf("invalid day-of-year date %q", datePart)
		}
		if doy < 1 || doy > 366 {
			return time.Time{}, fmt.Errorf("day-of-year %d out of range", doy)
		}
		hh, mm, ss, ns, err := parseClock(timePart)
		if err != nil {
			return time.Time{}, err
		}
		return time.Date(year, 1, 1, hh, mm, ss, ns, time.UTC).AddDate(0, 0, doy-1), nil
	}

	return parseEpoch(s)
}

func parseClock(tp string) (hh, mm, ss, ns int, err error) {
	tp = strings.TrimSpace(tp)
	parts := strings.Split(tp, ":")
	if len(parts) != 3 {
		return 0, 0, 0, 0, fmt.Errorf("invalid clock %q", tp)
	}
	if hh, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid hour in %q: %w", tp, err)
	}
	if mm, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid minute in %q: %w", tp, err)
	}
	secParts := strings.SplitN(parts[2], ".", 2)
	if ss, err = strconv.Atoi(secParts[0]); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid second in %q: %w", tp, err)
	}
	if len(secParts) == 2 && secParts[1] != "" {
		frac := secParts[1]
		if len(frac) > 9 {
			frac = frac[:9]
		}
		for len(frac) < 9 {
			frac += "0"
		}
		if ns, err = strconv.Atoi(frac); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("invalid fractional second in %q: %w", tp, err)
		}
	}
	return hh, mm, ss, ns, nil
}

// -------------------------------------------------------------------------
// Lane 2: current full-catalog gp (OMM)
// -------------------------------------------------------------------------

func (r *Runner) syncSpaceTrackCurrentGP(ctx context.Context) error {
	data, meta, err := r.spaceTrackGet(ctx, r.cfg.SpaceTrackCurrentGPQueryURL)
	if err != nil {
		return fmt.Errorf("fetch gp: %w", err)
	}
	rows, err := parseSpaceTrackJSONRecords(data)
	if err != nil {
		return fmt.Errorf("parse gp JSON: %w", err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("gp query returned no records")
	}

	if err := r.archiveRaw("spacetrack", "gp_current.json", data); err != nil {
		log.Warnf("Failed to archive Space-Track current-gp payload: %v", err)
	}

	tags := sourceTags(spaceTrackProviderID, spaceTrackCurrentGPSource, meta.SourceURL, data)
	countOMM, countMPE, normalizedHash, err := r.ingestGPRows(rows, "source:spacetrack", tags)
	if err != nil {
		r.recordIngestFailureForReview(spaceTrackCurrentGPSource, err)
		return fmt.Errorf("ingest gp: %w", err)
	}
	if err := r.recordIngestBatchProvenance(spaceTrackCurrentGPSource, data, meta,
		parserVersionSpaceTrackCurrentGP, normalizedHash, map[string]int{
			"OMM.fbs": countOMM,
			"MPE.fbs": countMPE,
		}, warningsForFetch(meta)); err != nil {
		log.Warnf("Failed to record Space-Track current-gp provenance: %v", err)
	}

	r.checkpoints.setString("spacetrack_current_gp_last_success", time.Now().UTC().Format(time.RFC3339))
	if err := r.checkpoints.save(); err != nil {
		log.Warnf("Failed to persist checkpoints: %v", err)
	}
	log.Infof("Space-Track current-gp sync complete: OMM=%d MPE=%d", countOMM, countMPE)
	return nil
}

// parseSpaceTrackJSONRecords decodes a Space-Track JSON array (CCSDS OMM gp
// records, schema-exact upper-snake keys) into normalized rows for ingestGPRows.
func parseSpaceTrackJSONRecords(data []byte) ([]map[string]string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var raw []map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode JSON array: %w", err)
	}
	rows := make([]map[string]string, 0, len(raw))
	for _, rec := range raw {
		row := make(map[string]string, len(rec))
		for k, v := range rec {
			row[normalizeKey(k)] = strings.TrimSpace(udlValueToString(v))
		}
		rows = append(rows, row)
	}
	return rows, nil
}
