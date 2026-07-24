package flowrt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	linkedStoreSectionName = "sdn.flatsql.descriptor.v1"
	maxLinkedStoreSchema   = 128 << 10
	maxLinkedStoreMappings = 256
	maxLinkedStoreViews    = 256
	maxLinkedStoreFilters  = 32
	maxLinkedStoreSection  = 256 << 10
)

var safeSQLIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// LinkedStoreDescriptor is application-owned metadata embedded in a compiled
// WASM artifact. The host uses it mechanically to create an opaque FlatSQL
// arena and route four-byte FlatBuffer identifiers to declared tables.
type LinkedStoreDescriptor struct {
	Version         int                         `json:"version"`
	Engine          string                      `json:"engine"`
	Database        string                      `json:"database"`
	Schema          string                      `json:"schema"`
	FileIdentifiers []LinkedStoreFileIdentifier `json:"fileIdentifiers"`
	RecordViews     []LinkedStoreRecordView     `json:"recordViews,omitempty"`
}

type LinkedStoreFileIdentifier struct {
	ID    string `json:"id"`
	Table string `json:"table"`
}

// LinkedStoreRecordView is retained as opaque, validated route metadata for a
// future generic read surface. Store creation does not interpret these fields.
type LinkedStoreRecordView struct {
	ID             string                        `json:"id"`
	FileIdentifier string                        `json:"fileIdentifier"`
	Table          string                        `json:"table"`
	RecordColumn   string                        `json:"recordColumn"`
	Filters        []LinkedStoreRecordViewFilter `json:"filters"`
	LatestOrderBy  string                        `json:"latestOrderBy"`
}

type LinkedStoreRecordViewFilter struct {
	Parameter string `json:"parameter"`
	Column    string `json:"column"`
	Type      string `json:"type"`
}

func (d *LinkedStoreDescriptor) validate() error {
	if d == nil {
		return fmt.Errorf("linked-store descriptor is nil")
	}
	if d.Version != 1 {
		return fmt.Errorf("linked-store descriptor version = %d, want 1", d.Version)
	}
	if d.Engine != "flatsql" {
		return fmt.Errorf("linked-store descriptor engine = %q, want flatsql", d.Engine)
	}
	if !safeSQLIdentifier.MatchString(d.Database) {
		return fmt.Errorf("linked-store descriptor database %q is not a safe SQL identifier", d.Database)
	}
	if strings.TrimSpace(d.Schema) == "" || !utf8.ValidString(d.Schema) || len([]byte(d.Schema)) > maxLinkedStoreSchema || strings.ContainsRune(d.Schema, '\x00') {
		return fmt.Errorf("linked-store descriptor schema must contain 1..%d non-NUL UTF-8 bytes", maxLinkedStoreSchema)
	}
	if len(d.FileIdentifiers) == 0 || len(d.FileIdentifiers) > maxLinkedStoreMappings {
		return fmt.Errorf("linked-store descriptor must contain 1..%d file identifier mappings", maxLinkedStoreMappings)
	}
	ids := make(map[string]bool, len(d.FileIdentifiers))
	tables := make(map[string]bool, len(d.FileIdentifiers))
	for i, mapping := range d.FileIdentifiers {
		if len(mapping.ID) != 4 || !printableASCII(mapping.ID) {
			return fmt.Errorf("linked-store descriptor mapping %d id must be four printable ASCII bytes", i)
		}
		if !safeSQLIdentifier.MatchString(mapping.Table) {
			return fmt.Errorf("linked-store descriptor mapping %d table %q is not a safe SQL identifier", i, mapping.Table)
		}
		if ids[mapping.ID] || tables[mapping.Table] {
			return fmt.Errorf("linked-store descriptor contains a duplicate file identifier or table")
		}
		ids[mapping.ID] = true
		tables[mapping.Table] = true
	}
	if len(d.RecordViews) > maxLinkedStoreViews {
		return fmt.Errorf("linked-store descriptor contains more than %d record views", maxLinkedStoreViews)
	}
	viewIDs := make(map[string]bool, len(d.RecordViews))
	mappingTables := make(map[string]string, len(d.FileIdentifiers))
	for _, mapping := range d.FileIdentifiers {
		mappingTables[mapping.ID] = mapping.Table
	}
	for i, view := range d.RecordViews {
		if view.ID == "" || !safeRouteIdentifier(view.ID) {
			return fmt.Errorf("linked-store descriptor record view %d has an unsafe id", i)
		}
		if viewIDs[view.ID] {
			return fmt.Errorf("linked-store descriptor contains duplicate record view id %q", view.ID)
		}
		viewIDs[view.ID] = true
		if !ids[view.FileIdentifier] || mappingTables[view.FileIdentifier] != view.Table {
			return fmt.Errorf("linked-store descriptor record view %q references an undeclared mapping", view.ID)
		}
		if !safeSQLIdentifier.MatchString(view.RecordColumn) || !safeSQLIdentifier.MatchString(view.LatestOrderBy) {
			return fmt.Errorf("linked-store descriptor record view %q has an unsafe column", view.ID)
		}
		if view.Filters == nil || len(view.Filters) > maxLinkedStoreFilters {
			return fmt.Errorf("linked-store descriptor record view %q filters must be an array with at most %d entries", view.ID, maxLinkedStoreFilters)
		}
		parameters := make(map[string]bool, len(view.Filters))
		for _, filter := range view.Filters {
			if !safeSQLIdentifier.MatchString(filter.Parameter) || !safeSQLIdentifier.MatchString(filter.Column) || filter.Type != "text" {
				return fmt.Errorf("linked-store descriptor record view %q has an invalid filter", view.ID)
			}
			if parameters[filter.Parameter] {
				return fmt.Errorf("linked-store descriptor record view %q contains duplicate filter parameter %q", view.ID, filter.Parameter)
			}
			parameters[filter.Parameter] = true
		}
	}
	return nil
}

func printableASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func safeRouteIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	first := value[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || (first >= '0' && first <= '9')) {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// ReadLinkedStoreDescriptor returns the single embedded linked-store
// descriptor, or nil when the artifact declares none.
func ReadLinkedStoreDescriptor(wasm []byte) (*LinkedStoreDescriptor, error) {
	sections, err := wasmCustomSections(wasm, linkedStoreSectionName)
	if err != nil {
		return nil, err
	}
	if len(sections) == 0 {
		return nil, nil
	}
	if len(sections) != 1 {
		return nil, fmt.Errorf("WASM must contain exactly one %s custom section", linkedStoreSectionName)
	}
	if len(sections[0]) > maxLinkedStoreSection {
		return nil, fmt.Errorf("linked-store descriptor exceeds %d bytes", maxLinkedStoreSection)
	}
	if !utf8.Valid(sections[0]) {
		return nil, fmt.Errorf("linked-store descriptor is not valid UTF-8")
	}
	var descriptor LinkedStoreDescriptor
	decoder := json.NewDecoder(bytes.NewReader(sections[0]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return nil, fmt.Errorf("decode linked-store descriptor: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("linked-store descriptor contains multiple JSON values")
		}
		return nil, fmt.Errorf("decode linked-store descriptor trailing data: %w", err)
	}
	if err := descriptor.validate(); err != nil {
		return nil, err
	}
	canonical, err := canonicalLinkedStoreDescriptor(&descriptor)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(sections[0], canonical) {
		return nil, fmt.Errorf("linked-store descriptor JSON is not canonical")
	}
	return &descriptor, nil
}

func canonicalLinkedStoreDescriptor(descriptor *LinkedStoreDescriptor) ([]byte, error) {
	type canonicalMapping struct {
		ID    string `json:"id"`
		Table string `json:"table"`
	}
	type canonicalFilter struct {
		Column    string `json:"column"`
		Parameter string `json:"parameter"`
		Type      string `json:"type"`
	}
	type canonicalView struct {
		FileIdentifier string            `json:"fileIdentifier"`
		Filters        []canonicalFilter `json:"filters"`
		ID             string            `json:"id"`
		LatestOrderBy  string            `json:"latestOrderBy"`
		RecordColumn   string            `json:"recordColumn"`
		Table          string            `json:"table"`
	}
	type canonicalDescriptor struct {
		Database        string             `json:"database"`
		Engine          string             `json:"engine"`
		FileIdentifiers []canonicalMapping `json:"fileIdentifiers"`
		RecordViews     []canonicalView    `json:"recordViews,omitempty"`
		Schema          string             `json:"schema"`
		Version         int                `json:"version"`
	}
	canonical := canonicalDescriptor{
		Database: descriptor.Database,
		Engine:   descriptor.Engine,
		Schema:   descriptor.Schema,
		Version:  descriptor.Version,
	}
	for _, mapping := range descriptor.FileIdentifiers {
		canonical.FileIdentifiers = append(canonical.FileIdentifiers, canonicalMapping{ID: mapping.ID, Table: mapping.Table})
	}
	for _, view := range descriptor.RecordViews {
		out := canonicalView{
			FileIdentifier: view.FileIdentifier,
			ID:             view.ID,
			LatestOrderBy:  view.LatestOrderBy,
			RecordColumn:   view.RecordColumn,
			Table:          view.Table,
		}
		for _, filter := range view.Filters {
			out.Filters = append(out.Filters, canonicalFilter{Column: filter.Column, Parameter: filter.Parameter, Type: filter.Type})
		}
		canonical.RecordViews = append(canonical.RecordViews, out)
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(canonical); err != nil {
		return nil, fmt.Errorf("encode canonical linked-store descriptor: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func wasmCustomSections(wasm []byte, wanted string) ([][]byte, error) {
	if len(wasm) < 8 || string(wasm[:4]) != "\x00asm" || !bytes.Equal(wasm[4:8], []byte{1, 0, 0, 0}) {
		return nil, fmt.Errorf("invalid WASM header")
	}
	var matches [][]byte
	for offset := 8; offset < len(wasm); {
		sectionID := wasm[offset]
		offset++
		size, next, ok := readWasmU32(wasm, offset)
		if !ok {
			return nil, fmt.Errorf("invalid WASM section size")
		}
		offset = next
		end := offset + int(size)
		if end < offset || end > len(wasm) {
			return nil, fmt.Errorf("WASM section exceeds artifact bounds")
		}
		if sectionID == 0 {
			nameLen, payloadStart, valid := readWasmU32(wasm, offset)
			if !valid || payloadStart+int(nameLen) > end {
				return nil, fmt.Errorf("invalid WASM custom-section name")
			}
			nameEnd := payloadStart + int(nameLen)
			if string(wasm[payloadStart:nameEnd]) == wanted {
				matches = append(matches, append([]byte(nil), wasm[nameEnd:end]...))
			}
		}
		offset = end
	}
	return matches, nil
}

func readWasmU32(buf []byte, offset int) (uint32, int, bool) {
	var value uint32
	for shift := uint(0); shift <= 28 && offset < len(buf); shift += 7 {
		b := buf[offset]
		offset++
		value |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, offset, true
		}
	}
	return 0, offset, false
}
