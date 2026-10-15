package sealed

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A sealed command relayed by a proxy must still look remote once unwrapped.
// The inner request copied six forwarding headers while the node-wide rule
// (internal/netorigin) counts fifteen, so an outer request marked only by,
// say, Via or X-Forwarded-Port unwrapped as one from this machine
// (security audit 2026-10-15, round 2). Uses only APIs on origin/main.
func TestSealedInnerRequestKeepsEveryForwardingHeader(t *testing.T) {
	headers := []string{
		"X-Forwarded-For", "X-Real-Ip", "Forwarded", "X-Forwarded-Host",
		"X-Forwarded-Proto", "X-Forwarded-Port", "X-Forwarded-Server",
		"X-Forwarded-Prefix", "X-Original-Forwarded-For", "Via", "X-Client-Ip",
		"True-Client-Ip", "Cf-Connecting-Ip", "Fastly-Client-Ip", "X-Cluster-Client-Ip",
	}
	for _, name := range headers {
		f := newFixture(t)
		f.h.Next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "remote="+r.RemoteAddr+" "+name+"="+r.Header.Get(name))
		})
		raw := f.request(t, f.admin, http.MethodGet, "/api/echo", "", f.now)
		outer := httptest.NewRequest(http.MethodPost, Route, bytes.NewReader(raw))
		outer.RemoteAddr = "127.0.0.1:40000"
		outer.Header.Set(name, "edge-proxy")
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, outer)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d %s", name, rec.Code, rec.Body.String())
		}
		env, err := Open(rec.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		body, err := env.Decrypt(f.replyPriv)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(body.Body), name+"=edge-proxy") {
			t.Fatalf("%s was dropped when the command was unwrapped: the inner request looks local (%q)", name, body.Body)
		}
	}
}
