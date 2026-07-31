package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Locks the /identity/<peerId>.qr.png surface: the density-locked qr.vcf card
// rendered server-side as a scannable PNG (inline, not attachment) for the
// dashboard contact card — no client QR library, no external bytes.
func TestIdentityQRPNG(t *testing.T) {
	handler := makeIdentityHandler(identitySource{
		SelfID:      "16UiuSelf",
		SelfQRVCard: func() (string, error) { return fullChainTestCard("Self"), nil },
		PeerQRVCard: func(id string) (string, bool) {
			if id == "16UiuPeerA" {
				return fullChainTestCard("Peer A"), true
			}
			return "", false
		},
	})

	pngMagic := []byte{0x89, 'P', 'N', 'G'}
	for _, path := range []string{"/identity/16UiuSelf.qr.png", "/identity/16UiuPeerA.qr.png"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200: %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "image/png" {
			t.Errorf("GET %s content-type = %q, want image/png", path, got)
		}
		if disp := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(disp, "inline;") {
			t.Errorf("GET %s disposition = %q, want inline (the modal renders it in an <img>)", path, disp)
		}
		if !bytes.HasPrefix(rec.Body.Bytes(), pngMagic) {
			t.Errorf("GET %s body is not a PNG", path)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/identity/16UiuUnknown.qr.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown id qr.png status = %d, want 404", rec.Code)
	}
}
