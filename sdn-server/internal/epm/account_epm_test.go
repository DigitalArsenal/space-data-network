package epm

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

// accountEPMFixture is a Service holding only a runtime signing key: the
// minimum a node needs to ISSUE a custodial account record.
func accountEPMFixture(t *testing.T) (*Service, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}
	svc := &Service{runtimeSigningKey: priv, runtimeSigningPath: "sdn/runtime-signing"}
	return svc, hex.EncodeToString(priv.Public().(ed25519.PublicKey))
}

func accountSubjectKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate account key: %v", err)
	}
	return hex.EncodeToString(pub)
}

// The record the node issues must verify under the WIRE verifier, carry the
// account as its subject, and project back into the profile shape the dashboard
// sent — otherwise the round trip the UI performs is a lie.
func TestBuildAccountEPMSignsVerifiesAndRoundTrips(t *testing.T) {
	t.Parallel()

	svc, issuerKey := accountEPMFixture(t)
	subjectKey := accountSubjectKey(t)

	profile := &Profile{
		DN:         "Ada Lovelace",
		LegalName:  "Augusta Ada King",
		GivenName:  "Ada",
		FamilyName: "Lovelace",
		JobTitle:   "Analyst",
		Occupation: "Mathematician",
		Email:      "ada@example.test",
		Telephone:  "+15550100",
		Address: &Address{
			Country:    "GB",
			Region:     "London",
			Locality:   "Marylebone",
			PostalCode: "W1",
			Street:     "12 St James's Square",
		},
		AlternateNames: []string{"A. Lovelace"},
	}

	record, err := svc.BuildAccountEPM(profile, AccountSubject{SigningPubKeyHex: subjectKey})
	if err != nil {
		t.Fatalf("BuildAccountEPM: %v", err)
	}
	if err := VerifyEPMSignature(record); err != nil {
		t.Fatalf("VerifyEPMSignature on the node's own output: %v", err)
	}

	if got := AccountEPMSubjectKeyHex(record); got != subjectKey {
		t.Fatalf("subject key = %q, want %q", got, subjectKey)
	}

	parsed := EPM.GetSizePrefixedRootAsEPM(record, 0)
	if parsed.ENTITY_TYPE() != EPM.EntityTypeUser {
		t.Fatalf("ENTITY_TYPE = %v, want User", parsed.ENTITY_TYPE())
	}
	if parsed.KEYSLength() != 2 {
		t.Fatalf("KEYS length = %d, want 2 (subject + issuer)", parsed.KEYSLength())
	}
	key := new(EPM.CryptoKey)
	parsed.KEYS(key, 1)
	if got := string(key.PUBLIC_KEY()); got != issuerKey {
		t.Fatalf("issuer key = %q, want %q", got, issuerKey)
	}

	projection := AccountEPMJSON(record, "data:image/png;base64,AAAA")
	for field, want := range map[string]string{
		"dn":             "Ada Lovelace",
		"legal_name":     "Augusta Ada King",
		"given_name":     "Ada",
		"family_name":    "Lovelace",
		"job_title":      "Analyst",
		"occupation":     "Mathematician",
		"email":          "ada@example.test",
		"telephone":      "+15550100",
		"photo_data_url": "data:image/png;base64,AAAA",
		"entity_type":    "user",
	} {
		if got, _ := projection[field].(string); got != want {
			t.Fatalf("projection[%q] = %q, want %q", field, got, want)
		}
	}
	address, ok := projection["address"].(map[string]string)
	if !ok || address["country"] != "GB" || address["postal_code"] != "W1" {
		t.Fatalf("projection address = %#v", projection["address"])
	}
	names, ok := projection["alternate_names"].([]string)
	if !ok || len(names) != 1 || names[0] != "A. Lovelace" {
		t.Fatalf("projection alternate_names = %#v", projection["alternate_names"])
	}
}

func TestBuildAccountEPMRefusals(t *testing.T) {
	t.Parallel()

	svc, _ := accountEPMFixture(t)
	subjectKey := accountSubjectKey(t)

	cases := []struct {
		name    string
		service *Service
		subject string
		profile *Profile
		wantErr error
	}{
		{"no subject key", svc, "", &Profile{}, ErrNoAccountSigningKey},
		{"subject key is not ed25519", svc, "abcd", &Profile{}, ErrNoAccountSigningKey},
		{"node holds no signing key", &Service{}, subjectKey, &Profile{}, ErrNoIssuerSigningKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.service.BuildAccountEPM(tc.profile, AccountSubject{SigningPubKeyHex: tc.subject})
			if err == nil {
				t.Fatalf("BuildAccountEPM accepted %s", tc.name)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// A rejected derivation path must stop the build before anything is emitted —
// the node lane's §18 rule, applied identically on the account lane.
func TestBuildAccountEPMRejectsInvalidKeyPath(t *testing.T) {
	t.Parallel()

	svc, _ := accountEPMFixture(t)
	_, err := svc.BuildAccountEPM(
		&Profile{SigningKeyPath: "m/44'/0'/0'/0'/0'"},
		AccountSubject{SigningPubKeyHex: accountSubjectKey(t)},
	)
	if err == nil {
		t.Fatal("BuildAccountEPM accepted a hardened signing path")
	}
	if !IsKeyPathValidationError(err) {
		t.Fatalf("error = %v, want a key-path validation error", err)
	}
}
