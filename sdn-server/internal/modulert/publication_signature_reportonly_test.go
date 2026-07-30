package modulert

// Tests for the REPORT-ONLY observe stage of the module publication-signature
// rollout (seal council / owner ruling 2026-07-30, see
// graph/tasks/saw-module-signing-enforcement.md).
//
// The point of these tests is the THIRD STATE. Before this change the gate had
// only two: nil policy (no verification at all — the inert state the council
// found on the sdn-server binary) and non-nil policy (fail closed — a flip that
// would have killed App2 OD, licensing and wallet sign-in on host-01). The
// observe stage must evaluate exactly as strictly as enforcement while refusing
// nothing, so an operator can drain the rejection log to empty by re-signing
// and only then flip. So each test below asserts BOTH halves: what report-only
// admits, and that the very same artifact and the very same trust set refuse it
// once the single ReportOnly field is cleared.

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

// reportOnlyPolicy is the observe-stage policy: real trust set, nothing refused.
func reportOnlyPolicy(trusted ...ed25519.PublicKey) *ModuleSignaturePolicy {
	return &ModuleSignaturePolicy{
		TrustedSigners:             trusted,
		AllowUnsignedByContentHash: map[string]bool{},
		ReportOnly:                 true,
	}
}

// enforcingPolicy is the same policy after the flip — deliberately built from
// the same inputs so the tests prove the flip is exactly one field.
func enforcingPolicy(trusted ...ed25519.PublicKey) *ModuleSignaturePolicy {
	p := reportOnlyPolicy(trusted...)
	p.ReportOnly = false
	return p
}

func TestReportOnlyAdmitsUnsignedArtifactAndStillReportsIt(t *testing.T) {
	pub, _ := mustGenerateEd25519Key(t)
	portable := []byte("\x00asm\x01\x00\x00\x00unsigned-artifact")

	got, status, err := enforceModuleSignaturePolicy(reportOnlyPolicy(pub), portable)
	if err != nil {
		t.Fatalf("report-only must admit an unsigned artifact, got error: %v", err)
	}
	if !bytesEqual(got, portable) {
		t.Fatalf("report-only must return the portable payload unchanged")
	}
	// The observe stage is worthless if it does not still say WHY.
	if status.Signed {
		t.Fatalf("status.Signed = true for an unsigned artifact")
	}
	if status.Reason != "unsigned" {
		t.Fatalf("status.Reason = %q, want %q", status.Reason, "unsigned")
	}
	if status.ContentHash == "" {
		t.Fatalf("status.ContentHash must be populated so the operator can drain the log by content hash")
	}

	// Same artifact, same trust set, flip cleared => refused.
	if _, _, err := enforceModuleSignaturePolicy(enforcingPolicy(pub), portable); err == nil {
		t.Fatalf("enforcement must refuse the unsigned artifact that report-only admitted")
	}
}

func TestReportOnlyAdmitsUntrustedSignerAndObservesItsKeyID(t *testing.T) {
	trustedPub, _ := mustGenerateEd25519Key(t)
	foreignPub, foreignPriv := mustGenerateEd25519Key(t)
	portable := []byte("\x00asm\x01\x00\x00\x00foreign-signed")
	artifact := buildSignedModuleArtifact(t, portable, foreignPriv, "some-other-node-2026")

	got, status, err := enforceModuleSignaturePolicy(reportOnlyPolicy(trustedPub), artifact)
	if err != nil {
		t.Fatalf("report-only must admit an untrusted-signer artifact, got error: %v", err)
	}
	if !bytesEqual(got, portable) {
		t.Fatalf("report-only must strip the trailer and return the portable payload")
	}
	if !status.Signed || status.Verified {
		t.Fatalf("status = {Signed:%t Verified:%t}, want signed but unverified", status.Signed, status.Verified)
	}
	if status.Reason != "untrusted_signer" {
		t.Fatalf("status.Reason = %q, want %q", status.Reason, "untrusted_signer")
	}
	// These two fields are the whole operational value of the observe window:
	// they tell the operator WHICH key is signing prod artifacts today.
	if status.SignerPubKeyHex != hex.EncodeToString(foreignPub) {
		t.Fatalf("status.SignerPubKeyHex = %q, want the observed foreign key %q", status.SignerPubKeyHex, hex.EncodeToString(foreignPub))
	}
	if status.KeyID != "some-other-node-2026" {
		t.Fatalf("status.KeyID = %q, want the observed key id", status.KeyID)
	}

	if _, _, err := enforceModuleSignaturePolicy(enforcingPolicy(trustedPub), artifact); err == nil {
		t.Fatalf("enforcement must refuse the untrusted signer that report-only admitted")
	}
}

func TestReportOnlyVerifiesTrustedSignerIdenticallyToEnforcement(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	portable := []byte("\x00asm\x01\x00\x00\x00properly-signed")
	artifact := buildSignedModuleArtifact(t, portable, priv, "node-key-2026")

	for _, tc := range []struct {
		name   string
		policy *ModuleSignaturePolicy
	}{
		{"report-only", reportOnlyPolicy(pub)},
		{"enforcing", enforcingPolicy(pub)},
	} {
		got, status, err := enforceModuleSignaturePolicy(tc.policy, artifact)
		if err != nil {
			t.Fatalf("%s: a trusted-signer artifact must be admitted: %v", tc.name, err)
		}
		if !status.Verified {
			t.Fatalf("%s: status.Verified = false for a correctly signed artifact (reason %q)", tc.name, status.Reason)
		}
		if !bytesEqual(got, portable) {
			t.Fatalf("%s: portable payload mismatch", tc.name)
		}
	}
}

// TestEnforcementRejectsTamperedSignedBundle is the unit-level form of
// acceptance #5: a bundle that was correctly signed and then MODIFIED must be
// refused once enforcement is on. A happy-path test does not close this task,
// so this asserts the tamper case explicitly — and asserts that report-only
// still surfaces it, which is what makes the observe window able to detect
// tampering before the flip.
func TestEnforcementRejectsTamperedSignedBundle(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	portable := []byte("\x00asm\x01\x00\x00\x00original-payload")
	artifact := buildSignedModuleArtifact(t, portable, priv, "node-key-2026")

	// Sanity: untampered, trusted, enforcing => admitted.
	if _, status, err := enforceModuleSignaturePolicy(enforcingPolicy(pub), artifact); err != nil || !status.Verified {
		t.Fatalf("precondition failed: pristine artifact must verify (err=%v verified=%t)", err, status.Verified)
	}

	// Tamper with the PORTABLE payload while leaving the valid trailer intact —
	// the realistic attack, since the signature covers the payload hash.
	tampered := append([]byte(nil), artifact...)
	tampered[8] ^= 0xFF

	_, status, err := enforceModuleSignaturePolicy(enforcingPolicy(pub), tampered)
	if err == nil {
		t.Fatalf("enforcement MUST reject a tampered signed bundle; it was admitted (reason=%q)", status.Reason)
	}
	if status.Verified {
		t.Fatalf("a tampered bundle must never report Verified=true")
	}

	// Report-only must still detect and report the tamper (admitting it, by
	// design) so the observe window is a real tamper detector, not a blind spot.
	if _, roStatus, roErr := enforceModuleSignaturePolicy(reportOnlyPolicy(pub), tampered); roErr != nil {
		t.Fatalf("report-only admits by design, got error: %v", roErr)
	} else if roStatus.Verified {
		t.Fatalf("report-only must report a tampered bundle as unverified, got Verified=true")
	}
}

// TestReportOnlyIsNotAContentHashAllowlist guards Hephaestus's Q4 objection:
// the observe stage must never permanently bless an artifact. Running an
// artifact through report-only must leave the policy's allowlist untouched, so
// nothing signed by the retired dev key can be laundered into policy.
func TestReportOnlyIsNotAContentHashAllowlist(t *testing.T) {
	pub, _ := mustGenerateEd25519Key(t)
	policy := reportOnlyPolicy(pub)
	portable := []byte("\x00asm\x01\x00\x00\x00unsigned-artifact")

	if _, _, err := enforceModuleSignaturePolicy(policy, portable); err != nil {
		t.Fatalf("report-only admit failed: %v", err)
	}
	if len(policy.AllowUnsignedByContentHash) != 0 {
		t.Fatalf("report-only must not seed AllowUnsignedByContentHash; got %d entry(s)", len(policy.AllowUnsignedByContentHash))
	}
}
