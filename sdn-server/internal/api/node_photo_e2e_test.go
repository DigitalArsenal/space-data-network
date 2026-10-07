package api

// The node's photo sticks (owner 2026-10-07: an uploaded photo "did NOT
// replace the image and did not survive the refresh"): set through the
// route, it is on the node's card, after a restart, and after a record edit.
// Its QR code carries a thumbnail and still scans ("image still not showing
// up in QR").

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/vcard"
)

func photoTestService(t *testing.T, dir string, key ed25519.PrivateKey) *epm.Service {
	t.Helper()
	peerID, err := peer.Decode("16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4")
	if err != nil {
		t.Fatal(err)
	}
	service := epm.NewService(nil, nil, peerID, "", dir)
	if err := service.SetRuntimeSigningKey(key, "sdn/runtime-signing"); err != nil {
		t.Fatal(err)
	}
	if err := service.Init(); err != nil {
		t.Fatal(err)
	}
	return service
}

func photoDataURL(t *testing.T) (string, string) {
	t.Helper()
	// A photo the size the dashboard sends: 256 px, a gradient behind a disc.
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for x := 0; x < 256; x++ {
		for y := 0; y < 256; y++ {
			c := color.RGBA{uint8(x), uint8(y), 160, 255}
			if (x-128)*(x-128)+(y-110)*(y-110) < 70*70 {
				c = color.RGBA{230, 120, 190, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString(buf.Bytes())
	return "data:image/jpeg;base64," + payload, payload
}

func putPhoto(t *testing.T, handler http.Handler, dataURL string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"photo_data_url": dataURL})
	request := httptest.NewRequest(http.MethodPut, "/api/node/epm/photo", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func cardOf(t *testing.T, service *epm.Service) string {
	t.Helper()
	card, err := service.GetNodeVCard()
	if err != nil {
		t.Fatalf("GetNodeVCard: %v", err)
	}
	return strings.ReplaceAll(card, "\r\n ", "")
}

// withoutPhoto unfolds a card and drops its PHOTO line.
func withoutPhoto(card string) string {
	var kept []string
	for _, line := range strings.Split(strings.ReplaceAll(card, "\r\n ", ""), "\r\n") {
		if !strings.HasPrefix(line, "PHOTO") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\r\n")
}

func TestNodePhotoSticksOnTheCard(t *testing.T) {
	dir := t.TempDir()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	service := photoTestService(t, dir, key)
	commits := 0
	handler := NewNodePhotoHandler(service, func(context.Context, []byte) error { commits++; return nil })
	dataURL, payload := photoDataURL(t)
	photoLine := "PHOTO;ENCODING=b;TYPE=JPEG:" + payload

	if response := putPhoto(t, handler, dataURL); response.Code != http.StatusOK {
		t.Fatalf("PUT photo = %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(cardOf(t, service), photoLine) {
		t.Fatal("the node's card does not carry the photo just set")
	}
	if commits != 1 {
		t.Fatalf("the re-signed record was committed %d times, want 1", commits)
	}

	// The scannable card carries a thumbnail, stays within the scannable
	// size, and the code drawn from it reads back as the same card.
	qrCard, err := service.GetNodeQRVCard()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(qrCard, "\r\nPHOTO;ENCODING=b;TYPE=PNG:") {
		t.Fatalf("the QR card carries no thumbnail: %s", qrCard)
	}
	plain, err := vcard.CompactQRVCard(service.GetNodeEPM())
	if err != nil {
		t.Fatal(err)
	}
	if len(qrCard) > vcard.QRPhotoBudgetBytes || withoutPhoto(qrCard) != strings.ReplaceAll(plain, "\r\n ", "") {
		t.Fatalf("the QR card is %d bytes (budget %d) or is not the compact card plus its photo: %s", len(qrCard), vcard.QRPhotoBudgetBytes, qrCard)
	}
	code, err := vcard.VCardToQR(qrCard, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if scanned, err := vcard.QRToVCard(code); err != nil || scanned != qrCard {
		t.Fatalf("the QR code does not read back as its card (%v)", err)
	}

	// A restart reads it back from what the node stored.
	if !strings.Contains(cardOf(t, photoTestService(t, dir, key)), photoLine) {
		t.Fatal("the photo did not survive a restart")
	}

	// Editing the record (which has no photo field) keeps the photo.
	proposal := photoTestService(t, t.TempDir(), key)
	if err := proposal.UpdateProfile(&epm.Profile{DN: "Flight Operations", LegalName: "Digital Arsenal"}); err != nil {
		t.Fatal(err)
	}
	edit := httptest.NewRequest(http.MethodPut, "/api/node/epm", bytes.NewReader(proposal.GetNodeEPM()))
	edit.Header.Set("Content-Type", epm.EPMContentType)
	editResponse := httptest.NewRecorder()
	NewNodeEPMHandler(service, func(context.Context, []byte) error { return nil }).ServeHTTP(editResponse, edit)
	if editResponse.Code != http.StatusOK {
		t.Fatalf("PUT record = %d %s", editResponse.Code, editResponse.Body.String())
	}
	if card := cardOf(t, service); !strings.Contains(card, photoLine) || !strings.Contains(card, "Digital Arsenal") {
		t.Fatal("a record edit dropped the photo")
	}

	// Not an image: refused, and the photo stays.
	if response := putPhoto(t, handler, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString([]byte("not a jpeg"))); response.Code != http.StatusBadRequest {
		t.Fatalf("PUT non-image = %d, want 400", response.Code)
	}
	if !strings.Contains(cardOf(t, service), photoLine) {
		t.Fatal("a refused photo changed the card")
	}

	// Empty removes it.
	if response := putPhoto(t, handler, ""); response.Code != http.StatusOK {
		t.Fatalf("PUT empty = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(cardOf(t, service), "PHOTO") {
		t.Fatal("the photo is still on the card after removal")
	}
}
