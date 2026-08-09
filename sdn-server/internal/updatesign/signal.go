package updatesign

// SIGNING THE PUSH.
//
// A signal is the small pub/sub pointer that tells the fleet an artifact exists
// (owner ruling 2026-08-09). It authorizes nothing — every byte it names is
// re-fetched and re-verified against the signed manifest — but it is still
// signed by the bonded node key, and therefore it is still audited by the same
// rule that governs every other use of that key: the signature does not leave
// this function until the line is on disk. "What can the bonded node key be
// made to say?" must have exactly one answer, and it is the audit log.
//
// The structural gate here is narrower than the manifest's on purpose. A
// manifest gate has to refuse anything that is not a release document because
// signing the wrong bytes ships them. A signal gate has to refuse anything that
// is not a signal document for a different reason: a caller must not be able to
// get a manifest-shaped thing signed under the signal domain, or vice versa,
// because that is exactly the cross-domain confusion the domain registry
// exists to prevent.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// MaxSignalBytes bounds a submitted signal. A pointer document is ~1 KB.
const MaxSignalBytes = 16 << 10

// CodeNotASignal is raised when the submitted document is not a signal.
const CodeNotASignal = "NOT_AN_UPDATE_SIGNAL"

// SignalRequest is one signal-signing submission.
type SignalRequest struct {
	// Signal is the canonicalizable JSON document to sign, WITHOUT a
	// signature.
	Signal []byte
	// Statement is the domain-separated preimage the caller derived. It is
	// re-derived here and the two must agree: a caller that could supply the
	// preimage could make this a blind signing oracle.
	Statement []byte
	Requester string
	RemoteIP  string
}

type signalFacts struct {
	UpdateID string
	Version  string
	Sequence int64
	Channel  string
	Target   string
}

// SignSignal issues a detached Ed25519 signature over the domain-separated
// statement for a well-formed update signal.
func (s *Signer) SignSignal(req SignalRequest) (*Result, error) {
	at := s.now()

	facts, canonical, refusal := s.validateSignal(req.Signal)
	if refusal != nil {
		_ = s.audit.Append(Entry{
			Timestamp:       at,
			Event:           EventRefused,
			StatementDomain: sigdomain.DomainUpdateSignalV1,
			SignerPubKeyHex: s.PublicKeyHex(),
			SubmittedBytes:  len(req.Signal),
			Requester:       req.Requester,
			RemoteIP:        req.RemoteIP,
			Reason:          refusal.Code,
			Detail:          refusal.Message,
		})
		return nil, refusal
	}

	sum := sha256.Sum256(canonical)
	contentHash := hex.EncodeToString(sum[:])
	statement, err := sigdomain.Statement(sigdomain.DomainUpdateSignalV1, sum[:])
	if err != nil {
		return nil, fmt.Errorf("build signal signing statement: %w", err)
	}
	// The caller's preimage is CHECKED, never trusted. If the caller and this
	// node disagree about the canonical bytes, that disagreement is the whole
	// failure the lane is arranged to catch, and it must stop here rather than
	// produce a signature nobody can verify.
	if len(req.Statement) > 0 && string(req.Statement) != string(statement) {
		refusal := refuse(CodeNotASignal,
			"the caller's signing statement does not match the one this node derives from the submitted document")
		_ = s.audit.Append(Entry{
			Timestamp:       at,
			Event:           EventRefused,
			ContentHash:     contentHash,
			StatementDomain: sigdomain.DomainUpdateSignalV1,
			SignerPubKeyHex: s.PublicKeyHex(),
			SubmittedBytes:  len(req.Signal),
			Requester:       req.Requester,
			RemoteIP:        req.RemoteIP,
			Reason:          refusal.Code,
			Detail:          refusal.Message,
		})
		return nil, refusal
	}

	signature := ed25519.Sign(s.key, statement)

	// Audit BEFORE the signature leaves this function: there is no such thing
	// as an issued-but-unrecorded signature over this key.
	if auditErr := s.audit.Append(Entry{
		Timestamp:       at,
		Event:           EventIssued,
		ContentHash:     contentHash,
		StatementDomain: sigdomain.DomainUpdateSignalV1,
		SignerPubKeyHex: s.PublicKeyHex(),
		SignatureHex:    hex.EncodeToString(signature),
		SubmittedBytes:  len(req.Signal),
		CanonicalBytes:  len(canonical),
		UpdateID:        facts.UpdateID,
		Version:         facts.Version,
		Sequence:        facts.Sequence,
		Channel:         facts.Channel,
		Target:          facts.Target,
		Requester:       req.Requester,
		RemoteIP:        req.RemoteIP,
		Reason:          "ok",
	}); auditErr != nil {
		return nil, fmt.Errorf("update signal signature discarded: audit line could not be appended: %w", auditErr)
	}

	return &Result{
		ContentHash:     contentHash,
		StatementDomain: sigdomain.DomainUpdateSignalV1,
		SignatureB64:    stdBase64(signature),
		SignatureHex:    hex.EncodeToString(signature),
		PublicKeyB64:    s.PublicKeyB64(),
		PublicKeyHex:    s.PublicKeyHex(),
		Algorithm:       SignatureAlgorithm,
		CanonicalBytes:  len(canonical),
		SignedAt:        at,
		UpdateID:        facts.UpdateID,
		Version:         facts.Version,
		Sequence:        facts.Sequence,
		Channel:         facts.Channel,
		Target:          facts.Target,
	}, nil
}

type submittedSignal struct {
	Schema   string `json:"schema"`
	UpdateID string `json:"update_id"`
	Version  string `json:"version"`
	Sequence int64  `json:"sequence"`
	Channel  string `json:"channel"`
	Target   struct {
		Platform string `json:"platform"`
		Arch     string `json:"arch"`
		Kind     string `json:"kind"`
	} `json:"target"`
	ManifestURL string `json:"manifest_url"`
	CarrierURL  string `json:"carrier_url"`
	Signing     struct {
		StatementDomain string `json:"statement_domain"`
		Signature       string `json:"signature"`
	} `json:"signing"`
}

func (s *Signer) validateSignal(body []byte) (signalFacts, []byte, *Refusal) {
	if len(body) == 0 {
		return signalFacts{}, nil, refuse(CodeEmptyPayload, "no update signal was submitted")
	}
	if len(body) > MaxSignalBytes {
		return signalFacts{}, nil, refuse(CodePayloadTooLarge, "update signal exceeds %d bytes", MaxSignalBytes)
	}
	var doc submittedSignal
	if err := json.Unmarshal(body, &doc); err != nil {
		return signalFacts{}, nil, refuse(CodeNotASignal, "submitted document is not JSON: %v", err)
	}
	if doc.Schema != update.SignalSchema {
		return signalFacts{}, nil, refuse(CodeNotASignal, "submitted document declares schema %q, not %s", doc.Schema, update.SignalSchema)
	}
	if strings.TrimSpace(doc.Signing.StatementDomain) != sigdomain.DomainUpdateSignalV1 {
		return signalFacts{}, nil, refuse(CodeBadStatementScope,
			"an update signal must declare signing.statement_domain %s, not %q",
			sigdomain.DomainUpdateSignalV1, doc.Signing.StatementDomain)
	}
	if strings.TrimSpace(doc.Signing.Signature) != "" {
		return signalFacts{}, nil, refuse(CodeNotASignal,
			"the submitted signal already carries a signature; submit the unsigned document")
	}
	for name, value := range map[string]string{
		"update_id":    doc.UpdateID,
		"version":      doc.Version,
		"channel":      doc.Channel,
		"manifest_url": doc.ManifestURL,
		"carrier_url":  doc.CarrierURL,
	} {
		if strings.TrimSpace(value) == "" {
			return signalFacts{}, nil, refuse(CodeNotASignal, "update signal is missing %s", name)
		}
	}
	if doc.Sequence <= 0 {
		return signalFacts{}, nil, refuse(CodeNotASignal, "update signal is missing sequence")
	}
	if !strings.HasPrefix(doc.ManifestURL, "https://") || !strings.HasPrefix(doc.CarrierURL, "https://") {
		return signalFacts{}, nil, refuse(CodeNotASignal, "update signal URLs must be HTTPS")
	}

	canonical, err := update.CanonicalSignedDocument(body)
	if err != nil {
		return signalFacts{}, nil, refuse(CodeNotASignal, "update signal could not be canonicalized: %v", err)
	}
	return signalFacts{
		UpdateID: doc.UpdateID,
		Version:  doc.Version,
		Sequence: doc.Sequence,
		Channel:  doc.Channel,
		Target:   fmt.Sprintf("%s/%s/%s", doc.Target.Kind, doc.Target.Platform, doc.Target.Arch),
	}, canonical, nil
}
