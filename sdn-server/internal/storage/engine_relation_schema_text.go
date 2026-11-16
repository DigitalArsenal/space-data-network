package storage

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

var (
	relationSchemaComments    = regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`)
	relationSchemaDeclaration = regexp.MustCompile(`(?m)^[ \t]*(table|struct|enum|union)\s+(\w+)[^{]*\{[^}]*\}`)
	relationSchemaFieldType   = regexp.MustCompile(`:\s*\[?([A-Za-z_]\w*)`)
	relationSchemaWord        = regexp.MustCompile(`[A-Za-z_]\w*`)
)

// EngineRelationSchemaText serves the node's projected table verbatim, with
// the declarations referenced by that table, its root, and its file identifier.
// Dependency tables use the engine projection when present in its graph.
func EngineRelationSchemaText(schemaName string) (text, table, fileID string, ok bool) {
	code := strings.ToUpper(strings.TrimSpace(schemaName))
	code = strings.TrimSpace(strings.TrimSuffix(code, ".FBS"))
	binding, routed := engineRoutedSchemaFor(code)
	if !routed {
		return "", "", "", false
	}
	source, found := sds.StandaloneSchemaText(code)
	if !found {
		return "", "", "", false
	}
	// The SDS closure supplies named enums and auxiliary types absent from the
	// engine graph. Engine tables take precedence, including renamed roots.
	declarations := make(map[string]string)
	for _, match := range relationSchemaDeclaration.FindAllStringSubmatch(relationSchemaComments.ReplaceAllString(source, ""), -1) {
		declarations[match[2]] = match[0]
	}
	for _, match := range relationSchemaDeclaration.FindAllStringSubmatch(engineDatabaseSchema, -1) {
		declarations[match[2]] = match[0]
	}
	root, found := engineTableBlockText(engineDatabaseSchema, binding.Table)
	if !found {
		return "", "", "", false
	}
	seen := make(map[string]bool)
	var out strings.Builder
	var visit func(string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		block, exists := declarations[name]
		if !exists {
			return
		} // Scalars have no declaration.
		if name == binding.Table {
			block = root
		}
		for _, match := range relationSchemaFieldType.FindAllStringSubmatch(block, -1) {
			visit(match[1])
		}
		if strings.HasPrefix(strings.TrimSpace(block), "union ") {
			body := block[strings.IndexByte(block, '{')+1 : strings.LastIndexByte(block, '}')]
			for _, member := range relationSchemaWord.FindAllString(body, -1) {
				visit(member)
			}
		}
		out.WriteString(strings.TrimSpace(block))
		out.WriteByte('\n')
	}
	visit(binding.Table)
	fmt.Fprintf(&out, "root_type %s;\nfile_identifier %q;\n", binding.Table, binding.FileID)
	return out.String(), binding.Table, binding.FileID, true
}
