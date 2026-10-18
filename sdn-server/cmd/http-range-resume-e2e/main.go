// Command http-range-resume-e2e drives the node's real http capability
// handler (caps.NewHTTPCapFactory, the one flows reach through
// hostcap/http-request) against a loopback publisher that closes ranged
// bodies mid-way, as Zenodo did to host-01's dust-map-3d table range on
// 2026-10-18 ("unexpected EOF" at 5,823,748 of 6,312,960 bytes):
//  1. Zenodo's headers (no ETag, no Last-Modified): an interrupted 206
//     resumes twice and returns the exact bytes;
//  2. with a strong ETag every resume carries If-Range;
//  3. a changed representation fails, never mixing bytes: If-Range answered
//     200, or (no validator) a different complete length;
//  4. a resume without progress fails;
//  5. an unranged 200 cut short fails without a resume.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
)

const size = 6312960 // the dust table range host-01 lost

var source = func() []byte {
	b := make([]byte, size)
	rand.New(rand.NewSource(20261018)).Read(b)
	return b
}()

// publisher answers single ranges of source. cut(n) says how many body bytes
// the n-th answer sends before its connection is closed (-1: all of them);
// it may also change etag or length, as a republished file would.
type publisher struct {
	etag     atomic.Value // "" sends no validator, as Zenodo's downloads
	length   atomic.Int64 // the complete length the answers claim
	cut      func(n int) int
	requests atomic.Int32
	ifRanges atomic.Int32
}

func (p *publisher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := int(p.requests.Add(1))
	etag, length := p.etag.Load().(string), int(p.length.Load())
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	first, last := 0, length-1
	spec, ranged := strings.CutPrefix(r.Header.Get("Range"), "bytes=")
	if ir := r.Header.Get("If-Range"); ir != "" {
		p.ifRanges.Add(1)
		ranged = ranged && ir == etag // RFC 9110 13.1.5: else the whole current representation
	}
	if ranged {
		a, b, _ := strings.Cut(spec, "-")
		first, _ = strconv.Atoi(a)
		last, _ = strconv.Atoi(b)
		last = min(last, length-1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, length))
	}
	body := source[first : last+1]
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if ranged {
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	send := len(body)
	if k := p.cut(n); k >= 0 && k < send {
		send = k
	}
	w.Write(body[:send])
	if send < len(body) {
		w.(http.Flusher).Flush()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
	}
}

type answer struct {
	OK     bool `json:"ok"`
	Result struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	} `json:"result"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// fetch makes the request dust-map-3d's stage 3 makes, through the cap.
func fetch(p *publisher, etag string, headers map[string]string) answer {
	p.etag.Store(etag)
	p.length.Store(size)
	s := httptest.NewServer(p)
	defer s.Close()
	handler := caps.NewHTTPCapFactory()(nil)
	payload, _ := json.Marshal(map[string]any{"method": "GET", "url": s.URL + "/mean_and_std_healpix.fits?download=1&sdn-range=0-8388607",
		"headers": headers, "timeout_ms": 60000, "max_bytes": 8 << 20})
	raw, err := handler("http.request", payload)
	must(err == nil, "handler: %v", err)
	var a answer
	must(json.Unmarshal(raw, &a) == nil, "answer: %s", raw)
	return a
}

func must(ok bool, format string, args ...any) {
	if !ok {
		fmt.Printf("HTTP RANGE RESUME E2E FAIL: "+format+"\n", args...)
		os.Exit(1)
	}
}

func exact(a answer) bool {
	body, err := base64.StdEncoding.DecodeString(a.Result.Body)
	return a.OK && a.Result.Status == http.StatusPartialContent && err == nil && bytes.Equal(body, source)
}

func main() {
	ranged := map[string]string{"Range": "bytes=0-8388607", "If-Modified-Since": "Thu, 01 Jan 1970 00:00:00 GMT"}
	twoCuts := func(n int) int {
		switch n {
		case 1:
			return 1823748
		case 2:
			return 2000000
		}
		return -1
	}

	p := &publisher{cut: twoCuts}
	a := fetch(p, "", ranged)
	must(exact(a) && p.requests.Load() == 3 && p.ifRanges.Load() == 0, "no validator: ok=%v requests=%d if-range=%d error=%q",
		a.OK, p.requests.Load(), p.ifRanges.Load(), a.Error.Message)
	fmt.Printf("ok  1. Zenodo's headers: an interrupted 206 resumed twice, %d exact bytes in %d requests\n", size, p.requests.Load())

	p = &publisher{cut: twoCuts}
	a = fetch(p, `"v1.0.2"`, ranged)
	must(exact(a) && p.requests.Load() == 3 && p.ifRanges.Load() == 2, "etag: ok=%v requests=%d if-range=%d error=%q",
		a.OK, p.requests.Load(), p.ifRanges.Load(), a.Error.Message)
	fmt.Printf("ok  2. a strong ETag: both resumes carried If-Range, exact bytes\n")

	p = &publisher{}
	p.cut = func(n int) int {
		if n == 1 {
			p.etag.Store(`"v1.0.3"`)
			return 1000000
		}
		return -1
	}
	a = fetch(p, `"v1.0.2"`, ranged)
	must(!a.OK && strings.Contains(a.Error.Message, "answered 200"), "changed etag: ok=%v error=%q", a.OK, a.Error.Message)
	p = &publisher{}
	p.cut = func(n int) int {
		if n == 1 {
			p.length.Store(size - 2880)
			return 1000000
		}
		return -1
	}
	a2 := fetch(p, "", ranged)
	must(!a2.OK && strings.Contains(a2.Error.Message, "representation changed"), "changed length: ok=%v error=%q", a2.OK, a2.Error.Message)
	fmt.Printf("ok  3. a changed representation fails: %s | %s\n", a.Error.Message, a2.Error.Message)

	p = &publisher{cut: func(n int) int { return map[bool]int{true: 1000000, false: 0}[n == 1] }}
	a = fetch(p, "", ranged)
	must(!a.OK && strings.Contains(a.Error.Message, "no progress") && p.requests.Load() == 2, "no progress: ok=%v requests=%d error=%q",
		a.OK, p.requests.Load(), a.Error.Message)
	fmt.Printf("ok  4. a resume without progress fails after %d requests: %s\n", p.requests.Load(), a.Error.Message)

	p = &publisher{cut: func(int) int { return 1000000 }}
	a = fetch(p, "", map[string]string{})
	must(!a.OK && p.requests.Load() == 1 && strings.Contains(a.Error.Message, "unexpected EOF"), "unranged: ok=%v requests=%d error=%q",
		a.OK, p.requests.Load(), a.Error.Message)
	fmt.Printf("ok  5. an unranged 200 cut short fails without a resume: %s\n", a.Error.Message)

	fmt.Println("HTTP RANGE RESUME E2E PASS (5 scenarios)")
}
