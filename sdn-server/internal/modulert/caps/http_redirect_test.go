package caps

import (
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
