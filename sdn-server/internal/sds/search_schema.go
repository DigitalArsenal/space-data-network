package sds

import (
	"embed"
	"strings"
)

// Binary schemas are generated from the existing SDS IDLs. The Go connector
// passes them opaquely to FlatSQL; FlatSQL owns field traversal and indexing.
//
//go:embed search-schemas/*.bfbs
var searchSchemas embed.FS

func SearchSchema(name string) ([]byte, bool) {
	code := strings.ToUpper(strings.TrimSpace(name))
	code = strings.TrimSuffix(code, ".FBS")
	if code == "" || strings.ContainsAny(code, "/\\.") {
		return nil, false
	}
	data, err := searchSchemas.ReadFile("search-schemas/" + code + ".bfbs")
	return data, err == nil
}
