package api

// The node's photo sticks (owner 2026-10-07: an uploaded photo "did NOT
// replace the image and did not survive the refresh"): set through the
// route, it is the signed record's PHOTO (SDS 1.239.0), so it survives a
// restart of a node that keeps only its record, survives a record edit, and
// reaches every peer that holds the record — on its card and as a thumbnail
// on its QR code that still scans ("image still not showing up in QR").

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// recordStore keeps only the signed record, as a node's record store does:
// the profile is rebuilt from it at startup.
type recordStore map[string][]byte

func (s recordStore) LoadLocalEPM(peerID string) ([]byte, error) {
	if raw, ok := s[peerID]; ok {
		return raw, nil
	}
	return nil, errors.New("no record")
}

func (s recordStore) SaveLocalEPM(peerID string, epmBytes []byte) error {
	s[peerID] = append([]byte(nil), epmBytes...)
	return nil
}

func photoTestService(t *testing.T, store recordStore, key ed25519.PrivateKey) *epm.Service {
	t.Helper()
	peerID, err := peer.Decode("16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4")
	if err != nil {
		t.Fatal(err)
	}
	service := epm.NewService(nil, nil, peerID, "", t.TempDir())
	service.SetProfileStore(store)
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
	return unfold(card)
}

func unfold(card string) string { return strings.ReplaceAll(card, "\r\n ", "") }

func TestNodePhotoSticksOnTheCard(t *testing.T) {
	store := recordStore{}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	service := photoTestService(t, store, key)
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

	// The photo is signed: it is in the preimage, and the record verifies.
	record := service.GetNodeEPM()
	preimage, err := epm.EPMSigningPayload(record)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(preimage, []byte(`"PHOTO":"`+dataURL+`"`)) {
		t.Fatal("the photo is not covered by the record's signature")
	}
	if err := epm.VerifyEPMSignature(record); err != nil {
		t.Fatalf("the record with its photo does not verify: %v", err)
	}

	// A restart rebuilds the profile from the stored record alone.
	if !strings.Contains(cardOf(t, photoTestService(t, store, key)), photoLine) {
		t.Fatal("the photo did not survive a restart")
	}

	// A peer holding only the record gets the photo on the full card, and a
	// thumbnail on the scannable card that stays within the scannable size
	// and reads back from the code at the 320 px a peer's QR is served at.
	if full, err := vcard.EPMToVCard(record); err != nil || !strings.Contains(unfold(full), photoLine) {
		t.Fatalf("a peer's card of the record does not carry the photo (%v)", err)
	}
	qrCard, err := vcard.CompactQRVCard(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(qrCard, "\r\nPHOTO;ENCODING=b;TYPE=PNG:") {
		t.Fatalf("the QR card carries no thumbnail: %s", qrCard)
	}
	if len(qrCard) > vcard.QRPhotoBudgetBytes {
		t.Fatalf("the QR card is %d bytes, past the scannable %d", len(qrCard), vcard.QRPhotoBudgetBytes)
	}
	if self, err := service.GetNodeQRVCard(); err != nil || self != qrCard {
		t.Fatalf("the node's own QR card differs from a peer's (%v)", err)
	}
	code, err := vcard.VCardToQR(qrCard, 320)
	if err != nil {
		t.Fatal(err)
	}
	scanned, err := vcard.QRToVCard(code)
	if err != nil || scanned != qrCard {
		t.Fatalf("the QR code does not read back as its card (%v)", err)
	}
	// Scanned, the card still names the key that signed the record.
	if err := epm.VerifyEPMSignatureBindingKey(record, vcard.SignKeyFromVCard(scanned)); err != nil {
		t.Fatalf("the scanned card does not bind to the record: %v", err)
	}

	// Editing the record without a photo field set keeps the photo.
	proposal := photoTestService(t, recordStore{}, key)
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

	// Empty removes it, from the card and from the stored record.
	if response := putPhoto(t, handler, ""); response.Code != http.StatusOK {
		t.Fatalf("PUT empty = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(cardOf(t, service), "PHOTO") || strings.Contains(cardOf(t, photoTestService(t, store, key)), "PHOTO") {
		t.Fatal("the photo is still on the card after removal")
	}
}
