package storage

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type rawRecordSyncFilter struct {
	where string
	args  []interface{}
}

func (filter rawRecordSyncFilter) active() bool {
	return strings.TrimSpace(filter.where) != ""
}

var (
	syncFilterBetweenPattern = regexp.MustCompile(`(?is)^\s*([A-Za-z_][A-Za-z0-9_\.]*)\s+BETWEEN\s+(.+)\s+AND\s+(.+)\s*$`)
	syncFilterLikePattern    = regexp.MustCompile(`(?is)^\s*([A-Za-z_][A-Za-z0-9_\.]*)\s+LIKE\s+(.+)\s*$`)
	syncFilterComparePattern = regexp.MustCompile(`(?is)^\s*([A-Za-z_][A-Za-z0-9_\.]*)\s*(=|!=|<>|>=|<=|>|<)\s*(.+?)\s*$`)
)

func compileRawRecordSyncFilter(query RawRecordQuery) (rawRecordSyncFilter, error) {
	text := strings.TrimSpace(query.SyncFilter)
	if text == "" {
		return rawRecordSyncFilter{}, nil
	}
	clauses, err := splitSyncFilterClauses(text)
	if err != nil {
		return rawRecordSyncFilter{}, err
	}
	where := make([]string, 0, len(clauses))
	args := make([]interface{}, 0, len(clauses))
	for _, clause := range clauses {
		sqlText, sqlArgs, err := compileSyncFilterClause(clause)
		if err != nil {
			return rawRecordSyncFilter{}, err
		}
		where = append(where, "("+sqlText+")")
		args = append(args, sqlArgs...)
	}
	return rawRecordSyncFilter{
		where: strings.Join(where, " AND "),
		args:  args,
	}, nil
}

func appendRawRecordSyncFilterWhere(query string, args []interface{}, filter rawRecordSyncFilter) (string, []interface{}) {
	if !filter.active() {
		return query, args
	}
	query += " AND " + filter.where
	args = append(args, filter.args...)
	return query, args
}

func splitSyncFilterClauses(text string) ([]string, error) {
	clauses := make([]string, 0, 4)
	for start := 0; start < len(text); {
		next := findSyncFilterAnd(text, start)
		if next < 0 {
			clause := strings.TrimSpace(text[start:])
			if clause != "" {
				clauses = append(clauses, clause)
			}
			break
		}
		candidate := text[start:next]
		if containsSyncFilterKeyword(candidate, "BETWEEN") {
			second := findSyncFilterAnd(text, next+3)
			if second < 0 {
				clause := strings.TrimSpace(text[start:])
				if clause != "" {
					clauses = append(clauses, clause)
				}
				break
			}
			clause := strings.TrimSpace(text[start:second])
			if clause != "" {
				clauses = append(clauses, clause)
			}
			start = second + 3
			continue
		}
		clause := strings.TrimSpace(candidate)
		if clause != "" {
			clauses = append(clauses, clause)
		}
		start = next + 3
	}
	if len(clauses) == 0 {
		return nil, fmt.Errorf("sync_filter is empty")
	}
	return clauses, nil
}

func findSyncFilterAnd(text string, start int) int {
	var quote rune
	for index, value := range text[start:] {
		absolute := start + index
		switch value {
		case '\'', '"':
			if quote == value {
				quote = 0
			} else if quote == 0 {
				quote = value
			}
			continue
		}
		if quote != 0 {
			continue
		}
		if keywordAt(text, absolute, "AND") {
			return absolute
		}
	}
	return -1
}

func containsSyncFilterKeyword(text, keyword string) bool {
	for index := range text {
		if keywordAt(text, index, keyword) {
			return true
		}
	}
	return false
}

func keywordAt(text string, index int, keyword string) bool {
	end := index + len(keyword)
	if index < 0 || end > len(text) || !strings.EqualFold(text[index:end], keyword) {
		return false
	}
	if index > 0 && isSyncFilterIdentifierByte(text[index-1]) {
		return false
	}
	if end < len(text) && isSyncFilterIdentifierByte(text[end]) {
		return false
	}
	return true
}

func isSyncFilterIdentifierByte(value byte) bool {
	return value == '_' || value == '.' ||
		(value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9')
}

func compileSyncFilterClause(clause string) (string, []interface{}, error) {
	if matches := syncFilterBetweenPattern.FindStringSubmatch(clause); len(matches) == 4 {
		field, err := rawSyncFilterField(matches[1])
		if err != nil {
			return "", nil, err
		}
		left, err := rawSyncFilterValue(field, matches[2])
		if err != nil {
			return "", nil, err
		}
		right, err := rawSyncFilterValue(field, matches[3])
		if err != nil {
			return "", nil, err
		}
		if field.kind == "text" || field.kind == "enum" {
			return "", nil, fmt.Errorf("sync_filter BETWEEN is not supported for %s", matches[1])
		}
		return field.column + " BETWEEN ? AND ?", []interface{}{left, right}, nil
	}

	if matches := syncFilterLikePattern.FindStringSubmatch(clause); len(matches) == 3 {
		field, err := rawSyncFilterField(matches[1])
		if err != nil {
			return "", nil, err
		}
		if field.kind != "text" && field.kind != "enum" && field.kind != "day" {
			return "", nil, fmt.Errorf("sync_filter LIKE is not supported for %s", matches[1])
		}
		value := unquoteSyncFilterValue(matches[2])
		if field.kind == "enum" {
			value = normalizeIndexEnum(value)
		}
		return field.column + " LIKE ?", []interface{}{value}, nil
	}

	if matches := syncFilterComparePattern.FindStringSubmatch(clause); len(matches) == 4 {
		field, err := rawSyncFilterField(matches[1])
		if err != nil {
			return "", nil, err
		}
		op := normalizeSyncFilterOperator(matches[2])
		if (field.kind == "text" || field.kind == "enum") && op != "=" && op != "!=" {
			return "", nil, fmt.Errorf("sync_filter operator %s is not supported for %s", op, matches[1])
		}
		value, err := rawSyncFilterValue(field, matches[3])
		if err != nil {
			return "", nil, err
		}
		return field.column + " " + op + " ?", []interface{}{value}, nil
	}

	return "", nil, fmt.Errorf("unsupported sync_filter clause %q", strings.TrimSpace(clause))
}

type rawSyncFilterFieldSpec struct {
	column string
	kind   string
}

func rawSyncFilterField(raw string) (rawSyncFilterFieldSpec, error) {
	name := strings.TrimSpace(raw)
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	name = strings.ToUpper(name)
	switch name {
	case "EPOCH", "EPOCH_UNIX":
		return rawSyncFilterFieldSpec{column: "idx.epoch_unix", kind: "time"}, nil
	case "SOURCE_TIMESTAMP":
		return rawSyncFilterFieldSpec{column: "idx.source_timestamp", kind: "time"}, nil
	case "EPOCH_DAY":
		return rawSyncFilterFieldSpec{column: "idx.epoch_day", kind: "day"}, nil
	case "NORAD_CAT_ID":
		return rawSyncFilterFieldSpec{column: "idx.norad_cat_id", kind: "int"}, nil
	case "OBJECT_ID", "ENTITY_ID", "FILE_ID":
		return rawSyncFilterFieldSpec{column: "idx.entity_id", kind: "text"}, nil
	case "OBJECT_TYPE":
		return rawSyncFilterFieldSpec{column: "idx.object_type", kind: "enum"}, nil
	case "OPS_STATUS_CODE":
		return rawSyncFilterFieldSpec{column: "idx.ops_status_code", kind: "enum"}, nil
	default:
		return rawSyncFilterFieldSpec{}, fmt.Errorf("unsupported sync_filter field %q", raw)
	}
}

func normalizeSyncFilterOperator(op string) string {
	if op == "<>" {
		return "!="
	}
	return op
}

func rawSyncFilterValue(field rawSyncFilterFieldSpec, raw string) (interface{}, error) {
	value := unquoteSyncFilterValue(raw)
	switch field.kind {
	case "time":
		return parseSyncFilterTime(value)
	case "int":
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("sync_filter value %q must be an integer", value)
		}
		return parsed, nil
	case "enum":
		normalized := normalizeIndexEnum(value)
		if normalized == "" {
			return nil, fmt.Errorf("sync_filter enum value %q is not supported", value)
		}
		return normalized, nil
	case "day":
		day, err := parseSyncFilterDay(value)
		if err != nil {
			return nil, err
		}
		return day, nil
	default:
		return value, nil
	}
}

func unquoteSyncFilterValue(raw string) string {
	value := strings.TrimSpace(raw)
	if len(value) >= 2 {
		first := value[0]
		last := value[len(value)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			value = value[1 : len(value)-1]
		}
	}
	value = strings.ReplaceAll(value, "''", "'")
	value = strings.ReplaceAll(value, `\"`, `"`)
	return strings.TrimSpace(value)
}

func parseSyncFilterTime(value string) (int64, error) {
	if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
		return parsed, nil
	}
	if day, err := time.Parse("2006-01-02", value); err == nil {
		return day.UTC().Unix(), nil
	}
	parsed, err := parseEpochString(value)
	if err != nil {
		return 0, fmt.Errorf("sync_filter epoch value %q is not a Unix timestamp, YYYY-MM-DD, or RFC3339 timestamp", value)
	}
	return parsed, nil
}

func parseSyncFilterDay(value string) (string, error) {
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return "", fmt.Errorf("sync_filter EPOCH_DAY value %q must be YYYY-MM-DD", value)
	}
	return value, nil
}
