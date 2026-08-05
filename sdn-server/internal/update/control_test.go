package update

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlHandlerAcceptsLoopbackOneTimeToken(t *testing.T) {
	root := t.TempDir()
	paths := PathsFor(root)
	if err := WriteControlToken(paths, "token-123"); err != nil {
		t.Fatalf("WriteControlToken failed: %v", err)
	}

	shutdown := make(chan struct{}, 1)
	handler := NewControlHandler(ControlHandlerOptions{
		BundleRoot: root,
		Shutdown: func() {
			shutdown <- struct{}{}
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
	req.RemoteAddr = "127.0.0.1:43123"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(controlTokenPath(paths)); !os.IsNotExist(err) {
		t.Fatalf("control token should be consumed, stat err=%v", err)
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not called")
	}
}

// TestControlHandlerReportsSystemdSupervision is the regression test for
// sdn-update-helper-supervisor-mode: the shutdown response must tell the
// helper whether THIS daemon was started by systemd (INVOCATION_ID), since
// the helper cannot infer that from its own environment.
func TestControlHandlerReportsSystemdSupervision(t *testing.T) {
	for _, tc := range []struct {
		name           string
		invocationID   string
		wantSupervised bool
	}{
		{name: "unsupervised (no INVOCATION_ID)", invocationID: "", wantSupervised: false},
		{name: "supervised (systemd sets INVOCATION_ID)", invocationID: "abc123deadbeef", wantSupervised: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INVOCATION_ID", tc.invocationID)

			root := t.TempDir()
			paths := PathsFor(root)
			if err := WriteControlToken(paths, "token-123"); err != nil {
				t.Fatalf("WriteControlToken failed: %v", err)
			}
			handler := NewControlHandler(ControlHandlerOptions{
				BundleRoot: root,
				Shutdown:   func() {},
			})

			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
			req.RemoteAddr = "127.0.0.1:43123"
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"supervised":`+boolJSON(tc.wantSupervised)) {
				t.Fatalf("body = %s, want supervised=%v", rec.Body.String(), tc.wantSupervised)
			}
		})
	}
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestControlHandlerRejectsNonLoopback(t *testing.T) {
	root := t.TempDir()
	if err := WriteControlToken(PathsFor(root), "token-123"); err != nil {
		t.Fatal(err)
	}
	handler := NewControlHandler(ControlHandlerOptions{BundleRoot: root, Shutdown: func() {}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
	req.RemoteAddr = "203.0.113.9:43123"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestControlHandlerRejectsWrongTokenAndBundleRoot(t *testing.T) {
	root := t.TempDir()
	if err := WriteControlToken(PathsFor(root), "token-123"); err != nil {
		t.Fatal(err)
	}
	handler := NewControlHandler(ControlHandlerOptions{BundleRoot: root, Shutdown: func() { t.Fatal("shutdown should not be called") }})

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{
			name: "wrong token",
			body: `{"token":"wrong","bundleRoot":"` + filepath.ToSlash(root) + `"}`,
			want: http.StatusForbidden,
		},
		{
			name: "wrong bundle root",
			body: `{"token":"token-123","bundleRoot":"` + filepath.ToSlash(filepath.Join(root, "other")) + `"}`,
			want: http.StatusConflict,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", strings.NewReader(tc.body))
			req.RemoteAddr = "127.0.0.1:43123"
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
