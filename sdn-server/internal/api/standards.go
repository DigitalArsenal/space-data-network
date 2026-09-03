package api

// GET /api/v1/standards — the embedded Space Data Standards registry: every
// schema this node embeds with its human name and the description lifted from
// the schema's own doc comment. The dashboard's data catalog reads it so the
// DESCRIPTION column is the standard's text, not a client-side guess.
//
// GET /api/v1/standards/{CODE}.fbs — the node's OWN engine table declaration
// for one routed standard (storage.EngineRelationSchemaText), text/plain. A
// browser-hosted engine is created from this exact text and projects the
// node's raw record frames identically; the node never projects for it.

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type standardRow struct {
	Code        string `json:"code"`
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

var (
	standardsOnce sync.Once
	standardsRows []standardRow
)

// splitStandardDoc turns "Name - description" into its two halves; a doc with
// no separator is all name.
func splitStandardDoc(doc string) (string, string) {
	doc = strings.TrimSpace(doc)
	if i := strings.Index(doc, " - "); i > 0 {
		return strings.TrimSpace(doc[:i]), strings.TrimSpace(doc[i+3:])
	}
	return doc, ""
}

func standardsList() []standardRow {
	standardsOnce.Do(func() {
		reg, err := sds.NewSchemaRegistry()
		if err != nil {
			return
		}
		for _, info := range reg.Info() {
			name, desc := splitStandardDoc(info.Description)
			code := strings.TrimSuffix(info.Name, ".fbs")
			standardsRows = append(standardsRows, standardRow{Code: code, Schema: info.Name, Name: name, Description: desc})
		}
		sort.Slice(standardsRows, func(i, j int) bool { return standardsRows[i].Code < standardsRows[j].Code })
	})
	return standardsRows
}

func (h *CoreAPIHandler) handleStandards(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	rows := standardsList()
	if rows == nil {
		rows = []standardRow{}
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]interface{}{"standards": rows})
}

// standardSchemaCodePattern bounds what may follow /api/v1/standards/: a
// standard code (two to eight alphanumerics) with an optional .fbs suffix,
// nothing that could name a path.
var standardSchemaCodePattern = regexp.MustCompile(`^[A-Za-z0-9]{2,8}$`)

const (
	standardSchemaTextPathPrefix  = "/api/v1/standards/"
	standardSchemaEngineTableHdr  = "X-SDN-Engine-Table"
	standardSchemaEngineFileIDHdr = "X-SDN-File-Id"
)

// handleStandardSchemaText serves GET/HEAD /api/v1/standards/{CODE}.fbs: the
// engine DDL block for that standard as text/plain, with the engine table
// name and FlatBuffer file identifier in headers so the caller can register
// the file id against the table exactly like the node does at boot.
// 400 for a malformed name, 404 for a standard the engine does not route.
func (h *CoreAPIHandler) handleStandardSchemaText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, standardSchemaTextPathPrefix)
	code := strings.TrimSuffix(name, ".fbs")
	if !standardSchemaCodePattern.MatchString(code) {
		writeError(w, http.StatusBadRequest, "standard code must match ^[A-Za-z0-9]{2,8}$ (optionally suffixed .fbs)")
		return
	}
	text, table, fileID, ok := storage.EngineRelationSchemaText(code)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown standard: "+code)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set(standardSchemaEngineTableHdr, table)
	w.Header().Set(standardSchemaEngineFileIDHdr, fileID)
	w.Header().Set("Content-Length", strconv.Itoa(len(text)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(text))
	}
}
