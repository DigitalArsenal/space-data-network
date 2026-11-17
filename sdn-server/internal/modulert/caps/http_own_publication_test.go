package caps

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Only a POST to the dataset publish route on this node's own admin
// listener waits for the publication; the same route on another port or
// host keeps the capability's cap.
func TestHTTPCapOwnPublicationIsTheAdminListenerOnly(t *testing.T) {
	defer SetOwnAdminListenAddr("")
	const path = "/api/v1/admin/dataset-updates/publish"
	cases := []struct {
		listen, method, url string
		own                 bool
	}{
		{"127.0.0.1:5001", "POST", "http://127.0.0.1:5001" + path, true},
		{"127.0.0.1:5001", "POST", "http://localhost:5001" + path, true},
		{"127.0.0.1:5001", "POST", "http://[::1]:5001" + path, true},
		{"0.0.0.0:5001", "POST", "http://127.0.0.1:5001" + path, true},
		{":5001", "POST", "http://127.0.0.1:5001" + path, true},
		{"10.0.0.5:5001", "POST", "http://10.0.0.5:5001" + path, true},
		{"127.0.0.1:5001", "POST", "http://127.0.0.1:5002" + path, false},
		{"127.0.0.1:5001", "POST", "http://127.0.0.1" + path, false},
		{"127.0.0.1:5001", "POST", "http://10.0.0.5:5001" + path, false},
		{"10.0.0.5:5001", "POST", "http://127.0.0.1:5001" + path, false},
		{"127.0.0.1:5001", "GET", "http://127.0.0.1:5001" + path, false},
		{"127.0.0.1:5001", "POST", "http://127.0.0.1:5001/api/v1/other", false},
		{"", "POST", "http://127.0.0.1:5001" + path, false},
	}
	for _, c := range cases {
		SetOwnAdminListenAddr(c.listen)
		if got := isOwnDatasetPublicationRequest(c.method, c.url); got != c.own {
			t.Errorf("listen %q, %s %s: own publication %v, want %v", c.listen, c.method, c.url, got, c.own)
		}
	}
}

// A request to this node's own publish route does not follow a redirect,
// whatever the flow asks.
func TestHTTPCapOwnPublicationDoesNotFollowRedirects(t *testing.T) {
	defer SetOwnAdminListenAddr("")
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.Write([]byte("arrived"))
	}))
	defer target.Close()
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer own.Close()
	SetOwnAdminListenAddr(strings.TrimPrefix(own.URL, "http://"))
	meta := httpCapCall(t, map[string]interface{}{"url": own.URL + "/api/v1/admin/dataset-updates/publish", "method": "POST", "body": "{}", "follow_redirects": true})
	if meta["ok"] != true {
		t.Fatalf("HTTP request failed: %v", meta)
	}
	result := meta["result"].(map[string]interface{})
	if reached.Load() != 0 || result["status"] != float64(http.StatusTemporaryRedirect) {
		t.Fatalf("the own publication request followed its redirect: reached=%d result=%v", reached.Load(), result)
	}
}
