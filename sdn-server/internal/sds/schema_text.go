package sds

import (
	"io/fs"
	"regexp"
	"strings"
)

var schemaInclude = regexp.MustCompile(`^include\s+"\.\./([A-Za-z0-9_]+)/main\.fbs"\s*;`)

// StandaloneSchemaText returns the embedded schema with its dependency
// declarations inlined and only the requested root and file identifier.
func StandaloneSchemaText(name string) (string, bool) {
	code := strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(name)), ".FBS")
	if code == "" || strings.ContainsAny(code, "/\\.") {
		return "", false
	}
	return standaloneSchemaText(sdsSchemasFS, code)
}

func standaloneSchemaText(files fs.FS, code string) (string, bool) {
	seen := make(map[string]bool)
	attributes := make(map[string]bool)
	var out strings.Builder
	var walk func(string, bool) bool
	walk = func(current string, root bool) bool {
		if seen[current] {
			return true
		}
		seen[current] = true
		data, err := fs.ReadFile(files, "schemas/"+current+".fbs")
		if err != nil {
			return false
		}
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "include ") {
				match := schemaInclude.FindStringSubmatch(trimmed)
				if match == nil || !walk(match[1], false) {
					return false
				}
			}
		}
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "include ") {
				continue
			}
			if !root && (strings.HasPrefix(trimmed, "root_type ") || strings.HasPrefix(trimmed, "file_identifier ") || strings.HasPrefix(trimmed, "file_extension ")) {
				continue
			}
			if strings.HasPrefix(trimmed, "attribute ") {
				if attributes[trimmed] {
					continue
				}
				attributes[trimmed] = true
			}
			out.WriteString(line)
			out.WriteByte('\n')
		}
		return true
	}
	if !walk(code, true) {
		return "", false
	}
	return out.String(), true
}
