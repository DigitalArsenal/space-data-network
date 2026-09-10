package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestRecordReadAllowancePreservesWriteTokens(t *testing.T) {
	rl := &rateLimiter{buckets: make(map[string]*ipBucket)}
	h := &CoreAPIHandler{rl: rl}
	handler := h.withReadRL(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatal("request method changed")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	for i := 0; i < 70; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/data/remote/peer", nil)
		handler(w, r)
		if w.Code != http.StatusNoContent || w.Header().Get("X-RateLimit-Limit") != "100" {
			t.Fatalf("read page %d: %d %v", i, w.Code, w.Header())
		}
	}
	for i := 0; i < writeLimitPerMin; i++ {
		if !rl.Allow(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/write", nil)) {
			t.Fatalf("read pages consumed write token %d", i)
		}
	}
	w := httptest.NewRecorder()
	if rl.Allow(w, httptest.NewRequest(http.MethodPost, "/write", nil)) {
		t.Fatal("write limit bypassed")
	}
	retry, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil || retry < 1 || retry > 6 {
		t.Fatalf("invalid retry delay: %q", w.Header().Get("Retry-After"))
	}
}

func TestReadAllowanceStillLimitsAndReportsRetry(t *testing.T) {
	rl := &rateLimiter{buckets: make(map[string]*ipBucket)}
	for i := 0; i < readLimitPerMin; i++ {
		if !rl.AllowRead(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/read", nil)) {
			t.Fatalf("read %d rejected", i)
		}
	}
	w := httptest.NewRecorder()
	if rl.AllowRead(w, httptest.NewRequest(http.MethodPost, "/read", nil)) || w.Code != http.StatusTooManyRequests {
		t.Fatal("read limit bypassed")
	}
	if w.Header().Get("Retry-After") != "1" {
		t.Fatalf("invalid read retry: %v", w.Header())
	}
}
