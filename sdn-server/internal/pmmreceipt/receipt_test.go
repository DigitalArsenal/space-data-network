package pmmreceipt

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The SETTLED W4.1 receipt — canonical byte form (sorted keys, compact),
// pinned byte-exact. The SDK side pins the SAME string
// (space-data-module-sdk/src/conformance/receipt.test.js). A drift on either
// side is a red lane: the manifest recorder and the bundle attestation would
// disagree about what "a receipt" is.
const fixture = `{"sdn-conformance-receipt":{"artifact":{"sha256":"b5c9a1b2db97ddd709a490be797722c07df0302194bdc79589f3503424668afa","size_bytes":14},"checks":[{"detail":"12/12 vector cases pass (authority: keplerian textbook anchors)","id":"vectors-tierB-keplerian-reference","required":true,"status":"pass","tier":2},{"detail":"energy closure within 1e-9 relative","id":"vis-viva-closure","required":true,"status":"pass","tier":3}],"corpus":{"cases":12,"model":"keplerian","path":"/modules/propagator/keplerian-reference/vectors/vectors.json","schema_version":"1"},"family":"propagator","generated_at":"2026-08-21T00:00:00.000Z","kit":{"id":"propagator","version":"1.0.0"},"summary":{"failed":0,"gaps":0,"passed":2},"tool":{"name":"space-data-module-sdk","version":"0.8.15"},"verdict":"PASS","version":1}}`

const fixtureContentHash = "b5c9a1b2db97ddd709a490be797722c07df0302194bdc79589f3503424668afa"

func mustParse(t *testing.T, data string) *Receipt {
	t.Helper()
	receipt, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse(%s) failed: %v", data, err)
	}
	return receipt
}

// decodeFixture gives a mutable copy of the settled document for refusals.
func decodeFixture(t *testing.T) document {
	t.Helper()
	var doc document
	if err := json.Unmarshal([]byte(fixture), &doc); err != nil {
		t.Fatalf("fixture baseline must decode: %v", err)
	}
	return doc
}

func TestParseSettledFixture(t *testing.T) {
	receipt := mustParse(t, fixture)

	if receipt.Version != SchemaVersion {
		t.Errorf("version = %d, want %d", receipt.Version, SchemaVersion)
	}
	if receipt.Family != "propagator" {
		t.Errorf("family = %q, want propagator", receipt.Family)
	}
	if receipt.Artifact.SHA256 != fixtureContentHash {
		t.Errorf("artifact.sha256 = %q, want %q", receipt.Artifact.SHA256, fixtureContentHash)
	}
	if receipt.Artifact.SizeBytes != 14 {
		t.Errorf("artifact.size_bytes = %d, want 14", receipt.Artifact.SizeBytes)
	}
	if receipt.Kit.ID != "propagator" || receipt.Kit.Version != "1.0.0" {
		t.Errorf("kit = %+v, want {propagator 1.0.0}", receipt.Kit)
	}
	if receipt.Verdict != "PASS" {
		t.Errorf("verdict = %q, want PASS", receipt.Verdict)
	}
	if receipt.Summary != (Summary{Passed: 2, Gaps: 0, Failed: 0}) {
		t.Errorf("summary = %+v, want 2 pass / 0 gap / 0 fail", receipt.Summary)
	}
	if len(receipt.Checks) != 2 {
		t.Errorf("checks = %d entries, want 2", len(receipt.Checks))
	}
	if receipt.Corpus == nil || receipt.Corpus.Cases != 12 || receipt.Corpus.Model != "keplerian" {
		t.Errorf("corpus = %+v, want 12 cases keplerian", receipt.Corpus)
	}
	if err := receipt.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	if !receipt.BindsToArtifact(fixtureContentHash) {
		t.Error("BindsToArtifact(fixture hash) = false, want true")
	}
	if receipt.BindsToArtifact("11" + strings.Repeat("1", 62)) {
		t.Error("BindsToArtifact(other hash) = true, want false")
	}
}

func TestParseRefusals(t *testing.T) {
	refuse := func(t *testing.T, data string) ErrorCode {
		t.Helper()
		_, err := Parse([]byte(data))
		if err == nil {
			t.Fatalf("Parse(%q) succeeded, want refusal", data)
		}
		var receiptErr *ReceiptError
		if !errors.As(err, &receiptErr) {
			t.Fatalf("err = %T, want *ReceiptError", err)
		}
		return receiptErr.Code
	}

	cases := []struct {
		name string
		mut  func(doc *document)
	}{
		{"unsupported version", func(doc *document) { doc.Receipt.Version = 2 }},
		{"bad sha256 form", func(doc *document) { doc.Receipt.Artifact.SHA256 = "ab" }},
		{"bad verdict", func(doc *document) { doc.Receipt.Verdict = "MAYBE" }},
		{"summary disagrees with checks", func(doc *document) { doc.Receipt.Summary.Passed = 17 }},
		{"empty kit id", func(doc *document) { doc.Receipt.Kit.ID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := decodeFixture(t)
			tc.mut(&doc)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if code := refuse(t, string(raw)); code != ErrInvalidReceipt {
				t.Errorf("code = %q, want %q", code, ErrInvalidReceipt)
			}
		})
	}

	t.Run("envelope key missing", func(t *testing.T) {
		if code := refuse(t, `{"something-else":{"version":1}}`); code != ErrInvalidReceipt {
			t.Errorf("code = %q, want %q", code, ErrInvalidReceipt)
		}
	})
	t.Run("unknown field refused", func(t *testing.T) {
		unknown := strings.Replace(fixture, `"version":1}`, `"version":1,"future":true}}`, 1)
		if code := refuse(t, unknown); code != ErrInvalidReceipt {
			t.Errorf("code = %q, want %q", code, ErrInvalidReceipt)
		}
	})
	t.Run("empty payload", func(t *testing.T) {
		if code := refuse(t, ""); code != ErrInvalidReceipt {
			t.Errorf("code = %q, want %q", code, ErrInvalidReceipt)
		}
	})
}

func TestEvaluateGraduatedRequirement(t *testing.T) {
	valid := mustParse(t, fixture)
	failVerdict := mustParse(t, strings.Replace(fixture, `"verdict":"PASS"`, `"verdict":"FAIL"`, 1))
	otherFamily := mustParse(t, strings.Replace(fixture, `"family":"propagator"`, `"family":"maneuver"`, 1))

	cases := []struct {
		name     string
		receipt  *Receipt
		listing  Listing
		content  string
		want     Grade
		wantCode ErrorCode
	}{
		{
			name:    "CORE ANONYMOUS without receipt",
			listing: Listing{Family: "propagator", TrustTier: TrustTierCORE, AccessPolicy: AccessPolicyAnonymous},
			want:    GradeNone, wantCode: ErrMissingReceipt,
		},
		{
			name:    "CORE AUTHENTICATED without receipt",
			listing: Listing{Family: "propagator", TrustTier: TrustTierCORE, AccessPolicy: AccessPolicyAuthenticated},
			want:    GradeNone, wantCode: ErrMissingReceipt,
		},
		{
			name:    "CORE without receipt but recorded Legacy (day-one escape)",
			listing: Listing{Family: "propagator", TrustTier: TrustTierCORE, Legacy: true},
			want:    GradeGrandfathered,
		},
		{
			name:    "CORE with valid receipt",
			receipt: valid,
			listing: Listing{Family: "propagator", TrustTier: TrustTierCORE},
			content: fixtureContentHash,
			want:    GradeCertified,
		},
		{
			name:    "CORE with receipt family mismatch",
			receipt: otherFamily,
			listing: Listing{Family: "propagator", TrustTier: TrustTierCORE},
			content: fixtureContentHash,
			want:    GradeNone, wantCode: ErrReceiptFamilyMismatch,
		},
		{
			name:    "CORE with FAIL verdict",
			receipt: failVerdict,
			listing: Listing{Family: "propagator", TrustTier: TrustTierCORE},
			content: fixtureContentHash,
			want:    GradeNone, wantCode: ErrFailedVerdict,
		},
		{
			name:    "CORE with hash mismatch",
			receipt: valid,
			listing: Listing{Family: "propagator", TrustTier: TrustTierCORE},
			content: "11" + strings.Repeat("1", 62),
			want:    GradeNone, wantCode: ErrArtifactMismatch,
		},
		{
			name:    "RECOMMENDED without receipt (badge-gated: silence is not a lie)",
			listing: Listing{Family: "propagator", TrustTier: TrustTierRecommended},
			want:    GradeNone,
		},
		{
			name:    "RECOMMENDED with valid receipt",
			receipt: valid,
			listing: Listing{Family: "propagator", TrustTier: TrustTierRecommended},
			content: fixtureContentHash,
			want:    GradeCertified,
		},
		{
			name:    "OPTIONAL without receipt",
			listing: Listing{Family: "propagator", TrustTier: TrustTierOptional},
			want:    GradeNone,
		},
		{
			name:    "OPTIONAL with valid receipt",
			receipt: valid,
			listing: Listing{Family: "propagator", TrustTier: TrustTierOptional},
			want:    GradeCertified,
		},
		{
			name:    "UNSPECIFIED with valid receipt honored",
			receipt: valid,
			listing: Listing{Family: "propagator", TrustTier: TrustTierUnspecified},
			want:    GradeCertified,
		},
		{
			name:    "Legacy below CORE refused",
			listing: Listing{Family: "propagator", TrustTier: TrustTierRecommended, Legacy: true},
			want:    GradeNone, wantCode: ErrLegacyNotCORE,
		},
		{
			name:    "Legacy with a declared receipt refused",
			receipt: valid,
			listing: Listing{Family: "propagator", TrustTier: TrustTierCORE, Legacy: true},
			content: fixtureContentHash,
			want:    GradeNone, wantCode: ErrLegacyNotCORE,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grade, err := EvaluateGraduatedRequirement(tc.receipt, tc.listing, tc.content)
			if tc.wantCode != "" {
				if err == nil {
					t.Fatalf("grade = %s, want code %s", grade, tc.wantCode)
				}
				var receiptErr *ReceiptError
				if !errors.As(err, &receiptErr) {
					t.Fatalf("err = %T, want *ReceiptError", err)
				}
				if receiptErr.Code != tc.wantCode {
					t.Errorf("code = %q, want %q (%v)", receiptErr.Code, tc.wantCode, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("EvaluateGraduatedRequirement() = %v, want nil", err)
			}
			if grade != tc.want {
				t.Errorf("grade = %s, want %s", grade, tc.want)
			}
		})
	}
}

// TestInvalidDeclaredReceiptRefusesAtEveryTier: Parse already refuses an
// invalid document, so the recorder can never hand one to the gate — but
// Evaluate re-validates defensively (a caller that builds the struct
// directly gets the same refusal at every tier). No tier drops trust
// metadata silently.
func TestInvalidDeclaredReceiptRefusesAtEveryTier(t *testing.T) {
	invalid := &Receipt{Version: 999}
	for _, tier := range []TrustTier{TrustTierCORE, TrustTierRecommended, TrustTierOptional} {
		grade, err := EvaluateGraduatedRequirement(invalid, Listing{Family: "propagator", TrustTier: tier}, "")
		if err == nil {
			t.Errorf("tier %s: grade = %s, want refusal", tier, grade)
			continue
		}
		var receiptErr *ReceiptError
		if !errors.As(err, &receiptErr) {
			t.Errorf("tier %s: err = %T, want *ReceiptError", tier, err)
			continue
		}
		if receiptErr.Code != ErrInvalidReceipt {
			t.Errorf("tier %s: code = %q, want %q", tier, receiptErr.Code, ErrInvalidReceipt)
		}
	}
}
