package api

// GET /api/v1/standards — the embedded Space Data Standards registry: every
// schema this node embeds with its human name and the description lifted from
// the schema's own doc comment. The dashboard's data catalog reads it so the
// DESCRIPTION column is the standard's text, not a client-side guess.

import (
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
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
