// Package pmmreceipt is the sdn-side contract for the W4.1 conformance
// RECEIPT (finding graph/findings/official-harness-shapes.md §5 "Receipt &
// trust"; task harness-w4-conformance-receipt-attestation).
//
// The receipt document is SETTLED on the SDK side (space-data-module-sdk
// src/conformance/receipt.js): envelope key "sdn-conformance-receipt",
// version 1, artifact{sha256,size_bytes} over the PORTABLE payload (REC
// publication trailer stripped), corpus/kit/tool/generated_at/verdict/checks
// and a summary that must equal the checks[] tallies. This package MIRRORS
// that document with the exact json tags and pins the same canonical fixture
// in its tests — drift on either side is a red lane. The node's pmm package
// will consume this when the listing gate wires in (SDN-MODULE-MANIFEST-V2);
// this package itself is a standalone, dependency-free library.
//
// Graduated listing requirement (the W4.1 gate):
//
//	CORE        — a valid receipt (family-matched, verdict != FAIL, binding
//	              the artifact CONTENT_HASH) is REQUIRED for every CORE entry:
//	              that is the class served to ANONYMOUS clients with no human
//	              in the loop. The explicit, recorded `Legacy` grandfather
//	              flag is the day-one escape ("NOT a hard listing requirement
//	              day one" — first-party modules that currently fail their own
//	              vectors must not be delisted). A family with no kit can
//	              never produce a receipt, so without Legacy it can never be
//	              CORE — the SDK refuses to build a receipt without kit.id,
//	              which makes that structural, not rhetorical.
//	RECOMMENDED  — badge-gated: a receipt is profit when present, silence when
//	              absent. A DECLARED receipt must always validate.
//	OPTIONAL     — below the badge line; a valid receipt is still honored,
//	              a missing one is not an error.
//
// There is no silent drop anywhere: an invalid, family-mismatched, FAIL-verdict
// or hash-divergent declared receipt is a refusal with a named code, at every
// tier. Trust metadata that fails to parse must never travel as if it
// certified something.
package pmmreceipt

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DocumentKey is the single envelope key of a receipt document — deliberately
// the future statement-domain name spelled out (W4.2 binds
// SDN-CONFORMANCE-RECEIPT-V1 to the artifact CONTENT_HASH), so the receipt
// document and the statement that authenticates it never disagree about what
// they name.
const DocumentKey = "sdn-conformance-receipt"

// SchemaVersion is the settled receipt schema version. Anything else refuses.
const SchemaVersion = 1

// Verdicts a receipt may record. FAIL can be RECORDED but never certifies.
var Verdicts = []string{"PASS", "PASS-WITH-GAPS", "FAIL"}

// CheckStatuses a check may carry.
var CheckStatuses = []string{"pass", "gap", "fail"}

// TrustTier is the listing trust tier (mirrors the manifest vocabulary:
// UNSPECIFIED is the zero value and means "not classified").
type TrustTier string

const (
	TrustTierUnspecified TrustTier = "UNSPECIFIED"
	TrustTierCORE        TrustTier = "CORE"
	TrustTierRecommended TrustTier = "RECOMMENDED"
	TrustTierOptional    TrustTier = "OPTIONAL"
)

// AccessPolicy is the listing access class (ANONYMOUS is the class served to
// unauthenticated clients with no human in the loop).
type AccessPolicy string

const (
	AccessPolicyAnonymous     AccessPolicy = "ANONYMOUS"
	AccessPolicyAuthenticated AccessPolicy = "AUTHENTICATED"
	AccessPolicyEntitled      AccessPolicy = "ENTITLED"
)

// Grade is the outcome of EvaluateGraduatedRequirement.
type Grade int

const (
	GradeNone          Grade = iota // no receipt, none required
	GradeCertified                  // a valid receipt exists and satisfies the tier
	GradeGrandfathered              // CORE without a receipt, explicitly Legacy
)

func (g Grade) String() string {
	switch g {
	case GradeCertified:
		return "certified"
	case GradeGrandfathered:
		return "grandfathered"
	default:
		return "none"
	}
}

// ErrorCode names each refusal. Structured, loud, never a silent skip.
type ErrorCode string

const (
	ErrMissingReceipt        ErrorCode = "missing_receipt"           // CORE without a receipt and without Legacy
	ErrInvalidReceipt        ErrorCode = "invalid_receipt"           // declared receipt fails shape/version/summary validation
	ErrReceiptFamilyMismatch ErrorCode = "receipt_family_mismatch"   // receipt certifies a different family
	ErrFailedVerdict         ErrorCode = "failed_verdict"            // declared receipt records FAIL
	ErrArtifactMismatch      ErrorCode = "receipt_artifact_mismatch" // receipt artifact.sha256 != CONTENT_HASH
	ErrLegacyNotCORE         ErrorCode = "legacy_not_core"           // Legacy grandfathering asserted below CORE
)

// ReceiptError is a structured gate refusal with a stable code.
type ReceiptError struct {
	Code ErrorCode
	Msg  string
}

func (e *ReceiptError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Msg) }

func errf(code ErrorCode, format string, args ...any) error {
	return &ReceiptError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// ---------------------------------------------------------------------------
// Document mirror (json tags EXACTLY match the SDK settled form; the SDK
// canonical form sorts keys, so field order here is irrelevant — names are).
// ---------------------------------------------------------------------------

// Artifact names the bytes the receipt certifies: the PORTABLE payload
// (REC trailer stripped), sha256 hex + size. The node compares this sha256
// to the artifact CONTENT_HASH (pmm.HashArtifact uses the same strip-then-
// hash semantic).
type Artifact struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// Corpus names the vectors suite the checks ran against (nullable).
type Corpus struct {
	Path          string `json:"path"`
	SchemaVersion string `json:"schema_version"`
	Cases         int    `json:"cases"`
	Model         string `json:"model"`
}

// Kit names the conformance kit that certified the artifact. A receipt only
// exists for a family with a kit.
type Kit struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Tool names the runner that executed the kit.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Check is one adjudicated conformance lane.
type Check struct {
	ID       string `json:"id"`
	Tier     int    `json:"tier"`
	Required bool   `json:"required"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
}

// Summary tallies checks[] by status. A document whose summary disagrees
// with its own checks[] is refused.
type Summary struct {
	Passed int `json:"passed"`
	Gaps   int `json:"gaps"`
	Failed int `json:"failed"`
}

// Receipt is the settled W4.1 document (inner value of the envelope).
type Receipt struct {
	Version     int      `json:"version"`
	Family      string   `json:"family"`
	Artifact    Artifact `json:"artifact"`
	Corpus      *Corpus  `json:"corpus"`
	Kit         Kit      `json:"kit"`
	Tool        Tool     `json:"tool"`
	GeneratedAt string   `json:"generated_at"`
	Verdict     string   `json:"verdict"`
	Checks      []Check  `json:"checks"`
	Summary     Summary  `json:"summary"`
}

// document is the wire envelope: exactly one key.
type document struct {
	Receipt Receipt `json:"sdn-conformance-receipt"`
}

var sha256HexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func contains(list []string, value string) bool {
	for _, candidate := range list {
		if candidate == value {
			return true
		}
	}
	return false
}

// Parse parses AND shape-validates a receipt document from canonical JSON
// bytes (the bundle ATTESTATION payload). Unknown fields and envelope shape
// violations are refusals (ErrInvalidReceipt); version, sha256 form, verdict,
// check statuses and summary-vs-checks consistency are all enforced here —
// the same strictness the SDK's parseConformanceReceipt applies, so the two
// sides can never disagree about what "a receipt" is.
func Parse(data []byte) (*Receipt, error) {
	if len(data) == 0 {
		return nil, errf(ErrInvalidReceipt, "empty payload")
	}
	var env document
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&env); err != nil {
		return nil, errf(ErrInvalidReceipt, "malformed document: %v", err)
	}
	receipt := &env.Receipt
	if err := receipt.Validate(); err != nil {
		return nil, err
	}
	return receipt, nil
}

// Validate checks the settled rules. A document that fails any rule is
// refused — an invalid or inscrutable receipt must never travel as if it
// certified anything.
func (r *Receipt) Validate() error {
	if r.Version != SchemaVersion {
		return errf(ErrInvalidReceipt, "unsupported receipt version %d (need %d)", r.Version, SchemaVersion)
	}
	if strings.TrimSpace(r.Family) == "" {
		return errf(ErrInvalidReceipt, "receipt family must not be empty")
	}
	if !sha256HexRe.MatchString(r.Artifact.SHA256) {
		return errf(ErrInvalidReceipt, "receipt artifact.sha256 must be 64 lowercase hex characters")
	}
	if r.Artifact.SizeBytes < 0 {
		return errf(ErrInvalidReceipt, "receipt artifact.size_bytes must be non-negative")
	}
	if strings.TrimSpace(r.Kit.ID) == "" {
		return errf(ErrInvalidReceipt, "receipt kit.id must not be empty (a receipt exists only for a family with a kit)")
	}
	if strings.TrimSpace(r.Tool.Name) == "" {
		return errf(ErrInvalidReceipt, "receipt tool.name must not be empty")
	}
	if !contains(Verdicts, r.Verdict) {
		return errf(ErrInvalidReceipt, "receipt verdict %q is not one of %v", r.Verdict, Verdicts)
	}
	if len(r.Checks) == 0 {
		return errf(ErrInvalidReceipt, "receipt checks must not be empty")
	}
	expected := Summary{}
	for index, check := range r.Checks {
		if !contains(CheckStatuses, check.Status) {
			return errf(ErrInvalidReceipt, "receipt checks[%d] has invalid status %q", index, check.Status)
		}
		switch check.Status {
		case "pass":
			expected.Passed++
		case "gap":
			expected.Gaps++
		default:
			expected.Failed++
		}
	}
	if r.Summary != expected {
		return errf(ErrInvalidReceipt,
			"receipt summary (%d/%d/%d) disagrees with checks[] (%d/%d/%d)",
			r.Summary.Passed, r.Summary.Gaps, r.Summary.Failed,
			expected.Passed, expected.Gaps, expected.Failed)
	}
	if r.Corpus != nil && r.Corpus.Cases < 0 {
		return errf(ErrInvalidReceipt, "receipt corpus.cases must be non-negative")
	}
	return nil
}

// BindsToArtifact reports whether the receipt certifies the given content
// hash — the CONTENT_HASH carried by the module manifest for the artifact's
// PORTABLE bytes (strip-then-hash, same semantic as pmm.HashArtifact). The
// comparison is case-insensitive; the settled form is lowercase hex.
func (r *Receipt) BindsToArtifact(contentHashHex string) bool {
	return strings.EqualFold(r.Artifact.SHA256, strings.TrimSpace(contentHashHex))
}

// Listing is the data the gate evaluates: what the node claims the module is.
// Every policy input is DATA — no tier table lives in Go.
type Listing struct {
	Family       string
	TrustTier    TrustTier
	AccessPolicy AccessPolicy
	// Legacy is the EXPLICIT, recorded day-one grandfather: CORE without a
	// receipt. It is never implied and never applied below CORE.
	Legacy bool
}

// EvaluateGraduatedRequirement applies the graduated listing gate to one
// listing.
//
//	receipt == nil  CORE         -> ErrMissingReceipt (unless Legacy -> GradeGrandfathered)
//	                RECOMMENDED  -> GradeNone (badge-gated: silence is not a lie)
//	                OPTIONAL/UNSPECIFIED -> GradeNone
//
//	receipt != nil  every tier   -> the declared receipt must parse, be
//	                family-matched and (when contentHashHex != "") bind the
//	                CONTENT_HASH; FAIL verdict refuses. Valid: GradeCertified.
//
// Legacy is a CORE-only escape; asserting it below CORE is ErrLegacyNotCORE.
func EvaluateGraduatedRequirement(receipt *Receipt, listing Listing, contentHashHex string) (Grade, error) {
	if receipt == nil {
		switch listing.TrustTier {
		case TrustTierCORE:
			if listing.Legacy {
				return GradeGrandfathered, nil
			}
			return GradeNone, errf(ErrMissingReceipt,
				"entry %q tier CORE requires a conformance receipt (or an explicit Legacy grandfather); "+
					"a family with no kit can never be CORE", listing.Family)
		case TrustTierRecommended:
			if listing.Legacy {
				return GradeNone, errf(ErrLegacyNotCORE,
					"Legacy grandfathering exists only for CORE entries; tier RECOMMENDED is badge-gated")
			}
			return GradeNone, nil
		default:
			if listing.Legacy {
				return GradeNone, errf(ErrLegacyNotCORE,
					"Legacy grandfathering exists only for CORE entries; tier %s is below the line", listing.TrustTier)
			}
			return GradeNone, nil
		}
	}

	if err := receipt.Validate(); err != nil {
		return GradeNone, err
	}
	if receipt.Verdict == "FAIL" {
		return GradeNone, errf(ErrFailedVerdict,
			"declared receipt records FAIL — it certifies nothing (family %s)", listing.Family)
	}
	if listing.Family != "" && !strings.EqualFold(receipt.Family, listing.Family) {
		return GradeNone, errf(ErrReceiptFamilyMismatch,
			"declared receipt certifies family %q, listing asserts %q", receipt.Family, listing.Family)
	}
	if strings.TrimSpace(contentHashHex) != "" && !receipt.BindsToArtifact(contentHashHex) {
		return GradeNone, errf(ErrArtifactMismatch,
			"declared receipt artifact.sha256 %s does not bind CONTENT_HASH %s",
			receipt.Artifact.SHA256, contentHashHex)
	}
	if listing.Legacy {
		return GradeNone, errf(ErrLegacyNotCORE,
			"Legacy grandfathering exists only for CORE entries without a receipt; this entry declares a receipt")
	}
	return GradeCertified, nil
}

// IsReceiptError reports whether err is a structured gate refusal from this
// package.
func IsReceiptError(err error) bool {
	var receiptErr *ReceiptError
	return errors.As(err, &receiptErr)
}
