package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fullChainTestCard is a card carrying the minimum crypto identity a QR
// surface may serve (owner law 2026-07-31): xpub, sign + encrypt HD paths,
// and the epmsig chain.
func fullChainTestCard(name string) string {
	return strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:" + name,
		"EMAIL;type=INTERNET;type=sign:bS80NCcvMCcvMCcvMC8w@sign.spacedatanetwork.org",
		"EMAIL;type=INTERNET;type=xpub:xpub6DKCyLbCHZLFR4XpFg26royZdkx@xpub.spacedatanetwork.org",
		"EMAIL;type=INTERNET;type=encrypt:bS80NCcvMCcvMCcvMS8w@encrypt.spacedatanetwork.org",
		"EMAIL;type=INTERNET;type=epmsig:pPiwij9fiUMf80Jhi8vKRdiGBfdY@epmsig.spacedatanetwork.org",
		"END:VCARD",
	}, "\r\n") + "\r\n"
}

// minimalTestCard is the name+peer-id-only shape the owner scanned on
// 2026-07-31 and outlawed.
func minimalTestCard() string {
	return strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:SDN Node bcPpYr2U",
		"EMAIL;type=INTERNET;type=peer:16Uiu2HAmGjaPx@peer.spacedatanetwork.org",
		"END:VCARD",
	}, "\r\n") + "\r\n"
}

// OWNER LAW 2026-07-31: the identity handler must refuse to serve ANY
// scannable card that lacks the full crypto identity — for peers we hold no
// signed EPM for (source returns nothing) AND for any source that slips a
// crypto-less card through.
func TestIdentityQRRefusesCryptolessCards(t *testing.T) {
	handler := makeIdentityHandler(identitySource{
		SelfID: "16UiuSelf",
		// A self source gone wrong: returns a card with no crypto identity.
		SelfQRVCard: func() (string, error) { return minimalTestCard(), nil },
		PeerQRVCard: func(id string) (string, bool) {
			switch id {
			case "16UiuMinimal":
				return minimalTestCard(), true
			case "16UiuFull":
				return fullChainTestCard("Full Peer"), true
			}
			// No signed EPM held => no card at all.
			return "", false
		},
	})

	for _, path := range []string{
		"/identity/16UiuSelf.qr.png",
		"/identity/16UiuSelf.qr.vcf",
		"/identity/16UiuMinimal.qr.png",
		"/identity/16UiuMinimal.qr.vcf",
		"/identity/16UiuNoEPM.qr.png",
		"/identity/16UiuNoEPM.qr.vcf",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404 (crypto-less QR cards must never be served)", path, rec.Code)
		}
	}

	for _, path := range []string{
		"/identity/16UiuFull.qr.png",
		"/identity/16UiuFull.qr.vcf",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200 (full-chain card must serve): %s", path, rec.Code, rec.Body.String())
		}
	}
}

// The full .vcf (non-QR contact download) stays servable without an EPM —
// the law governs scannable cards; the browse surface may still show the
// registry contact.
func TestIdentityPlainVCFStillServesWithoutEPM(t *testing.T) {
	handler := makeIdentityHandler(identitySource{
		SelfID: "16UiuSelf",
		PeerVCard: func(id string) (string, bool) {
			if id == "16UiuNoEPM" {
				return minimalTestCard(), true
			}
			return "", false
		},
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/identity/16UiuNoEPM.vcf", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET .vcf status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}
