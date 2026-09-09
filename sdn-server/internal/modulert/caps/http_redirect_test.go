package caps

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestHTTPCapRedirectControlPreventsPostReplay(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, follow := range []string{"unset", "true", "false"} {
			t.Run(fmt.Sprintf("%d/%s", status, follow), func(t *testing.T) {
				var reached atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					reached.Add(1)
					body, _ := io.ReadAll(r.Body)
					if r.Method != http.MethodPost || string(body) != "fixture-body" {
						t.Errorf("redirect changed request: method=%s body=%q", r.Method, body)
					}
					w.Header().Set("Content-Type", "text/plain")
					_, _ = w.Write([]byte("arrived"))
				}))
				defer target.Close()
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", target.URL)
					w.Header().Set("Content-Type", "text/plain")
					w.WriteHeader(status)
					_, _ = w.Write([]byte("redirect response"))
				}))
				defer origin.Close()
				payload := map[string]interface{}{"url": origin.URL, "method": "POST", "body": "fixture-body"}
				if follow != "unset" {
					payload["follow_redirects"] = follow == "true"
				}
				meta := httpCapCall(t, payload)
				if meta["ok"] != true {
					t.Fatalf("HTTP request failed: %v", meta)
				}
				result := meta["result"].(map[string]interface{})
				if follow == "false" {
					if reached.Load() != 0 || result["status"] != float64(status) || result["body"] != "redirect response" {
						t.Fatalf("redirect was followed or original response lost: reached=%d result=%v", reached.Load(), result)
					}
				} else if reached.Load() != 1 || result["status"] != float64(http.StatusOK) {
					t.Fatalf("default redirect behavior changed: reached=%d result=%v", reached.Load(), result)
				}
			})
		}
	}
}

func TestHTTPCapPreservesRepeatedCookieHeaders(t *testing.T) {
	cookies := []string{"first=fixture; Path=/; HttpOnly", "second=fixture; Expires=Wed, 09 Sep 2026 12:00:00 GMT; Path=/"}
	for _, status := range []int{http.StatusFound, http.StatusNotModified} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, cookie := range cookies {
					w.Header().Add("Set-Cookie", cookie)
				}
				w.Header().Set("Location", "/unused")
				w.WriteHeader(status)
			}))
			defer server.Close()
			meta := httpCapCall(t, map[string]interface{}{"url": server.URL, "follow_redirects": false})
			if meta["ok"] != true {
				t.Fatalf("HTTP request failed: %v", meta)
			}
			result := meta["result"].(map[string]interface{})
			legacy := result["headers"].(map[string]interface{})
			values := result["header_values"].(map[string]interface{})["Set-Cookie"]
			if result["status"] != float64(status) || legacy["Set-Cookie"] != cookies[0] || !reflect.DeepEqual(values, []interface{}{cookies[0], cookies[1]}) {
				t.Fatalf("cookie headers changed: %v", result)
			}
		})
	}
}

func TestHTTPCapBinaryResponsePreservesExactBytes(t *testing.T) {
	raw := []byte{0, 255, '<', '&', '>', '\n'}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Add("Set-Cookie", "first=fixture")
		w.Header().Add("Set-Cookie", "second=fixture")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(raw)
	}))
	defer server.Close()
	payload, _ := json.Marshal(map[string]interface{}{"url": server.URL, "response_encoding": "binary", "max_bytes": len(raw)})
	reply, err := httpCapHandle("http.request", payload)
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte{0, 'S', 'D', 'N', 'E', 'N', 'V', '1'}
	if !bytes.HasPrefix(reply, marker) {
		t.Fatal("missing binary hostcall envelope")
	}
	wire := reply[len(marker):]
	n := int(binary.LittleEndian.Uint32(wire))
	var meta struct {
		OK     bool `json:"ok"`
		Result struct {
			Status   int                 `json:"status"`
			Encoding string              `json:"body_encoding"`
			Body     map[string]int      `json:"body"`
			Headers  map[string][]string `json:"header_values"`
		} `json:"result"`
	}
	if err := json.Unmarshal(wire[4:4+n], &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.OK || meta.Result.Status != 206 || meta.Result.Encoding != "binary" || meta.Result.Body["$bin"] != 0 || len(meta.Result.Headers["Set-Cookie"]) != 2 {
		t.Fatalf("metadata: %+v", meta)
	}
	tail := wire[4+n:]
	if binary.LittleEndian.Uint32(tail) != 1 || int(binary.LittleEndian.Uint32(tail[4:])) != len(raw) || !bytes.Equal(tail[8:], raw) {
		t.Fatal("response bytes changed")
	}
	payload, _ = json.Marshal(map[string]interface{}{"url": server.URL, "response_encoding": "binary", "max_bytes": len(raw) - 1})
	reply, err = httpCapHandle("http.request", payload)
	if err != nil || bytes.HasPrefix(reply, marker) || !bytes.Contains(reply, []byte(`"ok":false`)) {
		t.Fatal("oversized binary response was accepted")
	}
}
