package api

// Durable-table helpers the export lane (export.go) pages with: the source
// lane name, the stored count, and the opaque page cursor. The cursor is
// binary rendered as hex: [u8 len][schema] then per table, in key order,
// [u8 len][table][i64le row id].

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

var tableCursorTablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// maxTableCursorEntries bounds one decoded cursor.
const maxTableCursorEntries = 1024

func durableTableSourceName(schema, source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", nil
	}
	prefix := schema + "@"
	if !strings.HasPrefix(source, prefix) || strings.TrimPrefix(source, prefix) == "" {
		return "", fmt.Errorf("source must name a %s lane", schema)
	}
	return strings.TrimPrefix(source, prefix), nil
}

func encodeFullTableCursor(schema string, cursor storage.FullTablePageCursor) string {
	if len(cursor) == 0 || len(schema) > 255 {
		return ""
	}
	tables := make([]string, 0, len(cursor))
	for table := range cursor {
		if len(table) > 255 {
			return ""
		}
		tables = append(tables, table)
	}
	sort.Strings(tables)
	out := append([]byte{byte(len(schema))}, schema...)
	for _, table := range tables {
		out = append(out, byte(len(table)))
		out = append(out, table...)
		out = binary.LittleEndian.AppendUint64(out, uint64(cursor[table]))
	}
	return hex.EncodeToString(out)
}

func decodeFullTableCursor(schema, encoded string) (storage.FullTablePageCursor, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > 64*1024 {
		return nil, fmt.Errorf("table cursor is too large")
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("table cursor is invalid")
	}
	n := int(raw[0])
	if len(raw) < 1+n || string(raw[1:1+n]) != schema {
		return nil, fmt.Errorf("table cursor does not match %s", schema)
	}
	rest := raw[1+n:]
	cursor := storage.FullTablePageCursor{}
	for len(rest) > 0 {
		m := int(rest[0])
		if len(rest) < 1+m+8 {
			return nil, fmt.Errorf("table cursor is invalid")
		}
		table := string(rest[1 : 1+m])
		rowID := int64(binary.LittleEndian.Uint64(rest[1+m : 1+m+8]))
		if !tableCursorTablePattern.MatchString(table) || rowID <= 0 || len(cursor) >= maxTableCursorEntries {
			return nil, fmt.Errorf("table cursor is invalid")
		}
		cursor[table] = rowID
		rest = rest[1+m+8:]
	}
	if len(cursor) == 0 {
		return nil, fmt.Errorf("table cursor does not match %s", schema)
	}
	return cursor, nil
}

func fullTableStoredCount(summary *storage.DataSummary, schemaName, sourceName string) int64 {
	if summary == nil {
		return 0
	}
	if sourceName == "" {
		for _, schema := range summary.Schemas {
			if schema.SchemaName == schemaName {
				return schema.Count
			}
		}
		return 0
	}
	var total int64
	for _, source := range summary.Sources {
		if source.SchemaName == schemaName && source.SourceName == sourceName {
			total += source.Count
		}
	}
	return total
}
