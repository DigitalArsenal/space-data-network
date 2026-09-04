package caps

// HTTP response validators for the egress connector (fbcs program, CelesTrak
// politeness). The host remembers the ETag / Last-Modified a publisher sent
// with a 2xx and presents them on the next GET for the same URL as
// If-None-Match / If-Modified-Since, so an unchanged payload costs the
// publisher a 304 instead of a full body. The module keeps its own headers
// when it sets either validator itself.
//
// Both hooks are pure observers over the node's retrieval ledger
// (internal/sourcemetrics): they never decide WHAT to fetch, only what to
// present. Nil disables them.

import (
	"net/http"
	"strings"
	"sync"
)

// FetchValidatorSource returns the stored validators for a URL ("" when the
// URL was never fetched or the publisher sent none).
type FetchValidatorSource func(url string) (etag, lastModified string)

// FetchValidatorObserver books the validators a 2xx response carried.
type FetchValidatorObserver func(url, etag, lastModified string)

var (
	fetchValidatorMu       sync.RWMutex
	fetchValidatorSource   FetchValidatorSource
	fetchValidatorObserver FetchValidatorObserver
)

// SetFetchValidatorSource installs the process-wide validator lookup. Pass nil
// to disable.
func SetFetchValidatorSource(source FetchValidatorSource) {
	fetchValidatorMu.Lock()
	fetchValidatorSource = source
	fetchValidatorMu.Unlock()
}

// SetFetchValidatorObserver installs the process-wide validator sink. Pass nil
// to disable.
func SetFetchValidatorObserver(observer FetchValidatorObserver) {
	fetchValidatorMu.Lock()
	fetchValidatorObserver = observer
	fetchValidatorMu.Unlock()
}

func fetchValidatorsFor(url string) (etag, lastModified string) {
	fetchValidatorMu.RLock()
	source := fetchValidatorSource
	fetchValidatorMu.RUnlock()
	if source == nil {
		return "", ""
	}
	return source(url)
}

func observeFetchValidators(url, etag, lastModified string) {
	if strings.TrimSpace(etag) == "" && strings.TrimSpace(lastModified) == "" {
		return
	}
	fetchValidatorMu.RLock()
	observer := fetchValidatorObserver
	fetchValidatorMu.RUnlock()
	if observer != nil {
		observer(url, etag, lastModified)
	}
}

// applyFetchValidators adds If-None-Match / If-Modified-Since to a GET that
// carries neither, from the stored validators for its URL. A request that
// already presents either validator is left exactly as the module wrote it.
func applyFetchValidators(req *http.Request) {
	if req == nil || req.Method != http.MethodGet {
		return
	}
	if req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "" {
		return
	}
	etag, lastModified := fetchValidatorsFor(req.URL.String())
	if etag = strings.TrimSpace(etag); etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified = strings.TrimSpace(lastModified); lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
}
