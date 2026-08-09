package update

// THE UPDATE SIGNAL — push, not poll.
//
// OWNER RULING 2026-08-09, verbatim: "We should be building locally and then
// pushing an update signal to all installs to upgrade in place, and only save
// the last five binaries for rollback purposes. That's the point of the update
// server."
//
// WHAT WAS MISSING. The fleet already had every half of this except the nudge.
// A publisher builds locally, verifies the binary, wraps it, gets the manifest
// signed by the bonded node key, and puts it on the feed the daemon itself
// serves. A host already knows how to fetch that feed, verify the signature and
// the hashes, stage the payload, swap it while staying up, health-check itself
// and reverse on failure. What did not exist was anything that told a host the
// artifact was there. In practice a human ssh'd in and typed `update install` —
// which is polling with extra steps, performed by a person.
//
// WHY A SIGNAL AND NOT THE PAYLOAD. internal/node/update_subscription.go (the
// G1 spine) put the whole per-peer-encrypted bundle ON the pub/sub topic. That
// design is why it was never wired to anything: a bundle is ~20 MB and
// gossipsub's default validated message size is 1 MiB, so the announcement
// could not have been delivered even once. It also duplicated authority — the
// feed and the topic would each be a way to obtain bytes, and two sources of
// the same artifact is two things to keep honest.
//
// So the signal carries no payload and grants no authority. It is a POINTER:
// "sequence N exists, here is its manifest URL". Every byte still arrives over
// HTTPS from the feed and is still verified against the signed manifest and the
// bundle trust roots before anything is swapped. The worst a perfectly forged
// signal can do is make a host fetch a manifest and refuse it.
//
// IT IS STILL SIGNED, for a different reason: amplification. An unsigned nudge
// on an open topic lets any peer make the entire fleet fetch 20 MB on demand.
// The signature is checked against the SAME trust roots that gate manifests, so
// no new key, no new trust decision, and no new place for one to be wrong —
// under a DIFFERENT statement domain (sigdomain.DomainUpdateSignalV1) so a
// signal signature can never be replayed as authorization for bytes.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
)

const (
	// SignalSchema identifies the wire document.
	SignalSchema = "org.spacedatanetwork.update.signal.v1"

	// SignalSignatureAlgorithm matches the manifest lane's spelling exactly.
	// Both verifiers refuse anything else, and two spellings of one algorithm
	// is how a fleet ends up with a signal it can verify and a manifest it
	// cannot (or the reverse).
	SignalSignatureAlgorithm = "Ed25519"

	// SignalMaxBytes bounds a signal on the wire. A pointer document is ~1 KB;
	// this leaves generous headroom while keeping the topic cheap to police.
	SignalMaxBytes = 16 << 10

	// SignalDefaultTTL is how long a signal stays actionable if it declares no
	// expiry. Bounded on purpose: gossipsub messages can be replayed, and a
	// signal that never expires is a permanent instruction to install a
	// specific version. Freshness is defence in depth — the sequence gate and
	// the failed-update quarantine are the load-bearing checks.
	SignalDefaultTTL = 24 * time.Hour

	// SignalClockSkew tolerates modest clock disagreement across the fleet
	// before a signal is judged to come from the future.
	SignalClockSkew = 5 * time.Minute
)

// SignalSigning mirrors ManifestSigning's shape deliberately: the two documents
// share one canonicalizer (CanonicalSignedDocument), so they must share the
// place the detached signature lives. One canonical form is one thing to get
// right; two would eventually disagree, and a canonicalization disagreement
// between producer and consumer is the single failure this lane is arranged to
// make impossible.
type SignalSigning struct {
	KeyID           string `json:"key_id"`
	PublicKey       string `json:"public_key,omitempty"`
	Algorithm       string `json:"algorithm,omitempty"`
	StatementDomain string `json:"statement_domain"`
	Signature       string `json:"signature,omitempty"`
}

// Signal is the pub/sub pointer to a published artifact.
type Signal struct {
	Schema      string         `json:"schema"`
	UpdateID    string         `json:"update_id"`
	Version     string         `json:"version"`
	Sequence    int64          `json:"sequence"`
	Channel     string         `json:"channel"`
	Target      ManifestTarget `json:"target"`
	FeedBaseURL string         `json:"feed_base_url"`
	ManifestURL string         `json:"manifest_url"`
	CarrierURL  string         `json:"carrier_url"`
	BundleHash  string         `json:"bundle_hash,omitempty"`
	BundleSize  int64          `json:"bundle_size,omitempty"`
	WasmHash    string         `json:"wasm_hash,omitempty"`
	WasmSize    int64          `json:"wasm_size,omitempty"`
	// Rollback mirrors the manifest's declared source-lineage reversal. A host
	// will not auto-install one: reversing lineage is an operator statement
	// (`--allow-rollback`), and a broadcast is not an operator.
	Rollback    bool          `json:"rollback,omitempty"`
	PublishedAt string        `json:"published_at"`
	ExpiresAt   string        `json:"expires_at,omitempty"`
	Signing     SignalSigning `json:"signing"`

	raw []byte
}

// SignalTopic is the fleet's update topic for a channel. It matches the
// pubsubTopic every shipped bundle manifest already declares
// (manifest.json update.pubsubTopic, e.g. "/sdn/updates/v1/beta"), so the
// bundle stays the source of truth for where its own channel talks and a host
// never has to be told out of band.
func SignalTopic(channel string) string {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = "stable"
	}
	return "/sdn/updates/v1/" + channel
}

// CanonicalSignedDocument is the one canonical form for every JSON document
// this lane signs: object keys sorted lexically, signing.signature removed,
// compact, no HTML escaping. It IS CanonicalManifestBytes — named separately
// only because it now serves two document kinds and the manifest-specific name
// would read as a bug at the signal call sites.
func CanonicalSignedDocument(raw []byte) ([]byte, error) { return CanonicalManifestBytes(raw) }

// SignalStatement builds the domain-separated preimage a signal signature
// covers: SDN-UPDATE-SIGNAL-V1 || 0x00 || sha256(canonical signal bytes).
func SignalStatement(raw []byte) ([]byte, error) {
	canonical, err := CanonicalSignedDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize update signal: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return sigdomain.Statement(sigdomain.DomainUpdateSignalV1, sum[:])
}

// ParseSignal decodes a signal, retaining the raw bytes so the signature can be
// checked over the canonical form of the document that was actually published —
// including fields this struct does not model.
func ParseSignal(raw []byte) (*Signal, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty update signal")
	}
	if len(raw) > SignalMaxBytes {
		return nil, fmt.Errorf("update signal exceeds %d bytes", SignalMaxBytes)
	}
	var signal Signal
	if err := json.Unmarshal(raw, &signal); err != nil {
		return nil, fmt.Errorf("parse update signal: %w", err)
	}
	if signal.Schema != SignalSchema {
		return nil, fmt.Errorf("unsupported update signal schema: %s", signal.Schema)
	}
	signal.raw = append([]byte(nil), raw...)
	return &signal, nil
}

// Raw returns the exact published bytes.
func (s *Signal) Raw() []byte { return s.raw }

// Marshal renders an unsigned signal for submission to the signing endpoint.
// The statement domain is set HERE, before signing, because it is part of the
// signed document — asserting it afterwards would be an unsigned claim about a
// signed document.
func (s *Signal) Marshal() ([]byte, error) {
	s.Schema = SignalSchema
	s.Signing.StatementDomain = sigdomain.DomainUpdateSignalV1
	if strings.TrimSpace(s.Signing.Algorithm) == "" {
		s.Signing.Algorithm = SignalSignatureAlgorithm
	}
	s.Signing.Signature = ""
	return json.Marshal(s)
}

// SignalVerifyOptions gates what a host will accept.
type SignalVerifyOptions struct {
	TrustedRoots TrustedRoots
	Platform     string
	Arch         string
	Kind         string
	Channel      string
	// CurrentSequence is the sequence the box is running. A signal at or below
	// it is not news.
	CurrentSequence int64
	Now             time.Time
}

// Verify checks everything that can be judged from the signal alone. It is
// deliberately cheap and deliberately NOT sufficient: passing here earns a
// signal nothing but the right to cause an HTTPS fetch, after which the signed
// manifest is verified from scratch against the same roots.
func (s *Signal) Verify(opts SignalVerifyOptions) error {
	if s == nil {
		return errors.New("nil update signal")
	}
	if err := s.assertShape(); err != nil {
		return err
	}
	if err := s.assertFreshness(nowOr(opts.Now)); err != nil {
		return err
	}
	if opts.Channel != "" && !strings.EqualFold(s.Channel, opts.Channel) {
		return fmt.Errorf("update signal is for channel %q, this bundle is on %q", s.Channel, opts.Channel)
	}
	if opts.Kind != "" && s.Target.Kind != opts.Kind {
		return fmt.Errorf("update signal targets kind %q, this bundle is %q", s.Target.Kind, opts.Kind)
	}
	if opts.Platform != "" && !platformMatches(s.Target.Platform, opts.Platform) {
		return fmt.Errorf("update signal targets platform %q, this host is %q", s.Target.Platform, opts.Platform)
	}
	if opts.Arch != "" && !archMatches(s.Target.Arch, opts.Arch) {
		return fmt.Errorf("update signal targets arch %q, this host is %q", s.Target.Arch, opts.Arch)
	}
	if s.Sequence <= opts.CurrentSequence {
		return fmt.Errorf("update signal sequence %d is not newer than the installed sequence %d", s.Sequence, opts.CurrentSequence)
	}
	return s.assertSignature(opts.TrustedRoots)
}

func (s *Signal) assertShape() error {
	if strings.TrimSpace(s.UpdateID) == "" {
		return errors.New("update signal missing update_id")
	}
	if strings.TrimSpace(s.Version) == "" {
		return errors.New("update signal missing version")
	}
	if s.Sequence <= 0 {
		return errors.New("update signal missing sequence")
	}
	if strings.TrimSpace(s.Channel) == "" {
		return errors.New("update signal missing channel")
	}
	if strings.TrimSpace(s.Target.Platform) == "" || strings.TrimSpace(s.Target.Arch) == "" || strings.TrimSpace(s.Target.Kind) == "" {
		return errors.New("update signal missing target")
	}
	if err := requireHTTPSURL(s.ManifestURL, "manifest_url"); err != nil {
		return err
	}
	if err := requireHTTPSURL(s.CarrierURL, "carrier_url"); err != nil {
		return err
	}
	if strings.TrimSpace(s.FeedBaseURL) != "" {
		if err := requireHTTPSURL(s.FeedBaseURL, "feed_base_url"); err != nil {
			return err
		}
	}
	if s.BundleHash != "" {
		if err := assertHash(strings.ToLower(s.BundleHash), "update signal bundle_hash must be a sha256 hex digest"); err != nil {
			return err
		}
	}
	if s.WasmHash != "" {
		if err := assertHash(strings.ToLower(s.WasmHash), "update signal wasm_hash must be a sha256 hex digest"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Signal) assertFreshness(now time.Time) error {
	published, err := time.Parse(time.RFC3339, strings.TrimSpace(s.PublishedAt))
	if err != nil {
		return fmt.Errorf("update signal published_at is not RFC3339: %w", err)
	}
	if published.After(now.Add(SignalClockSkew)) {
		return fmt.Errorf("update signal is dated %s, which is in the future", s.PublishedAt)
	}
	expires := published.Add(SignalDefaultTTL)
	if raw := strings.TrimSpace(s.ExpiresAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return fmt.Errorf("update signal expires_at is not RFC3339: %w", err)
		}
		expires = parsed
	}
	if now.After(expires) {
		return fmt.Errorf("update signal expired at %s", expires.UTC().Format(time.RFC3339))
	}
	return nil
}

func (s *Signal) assertSignature(roots TrustedRoots) error {
	if len(roots) == 0 {
		return errors.New("no trusted update roots: an update signal cannot be verified")
	}
	trusted, ok := roots[s.Signing.KeyID]
	if !ok || trusted == "" {
		return errors.New("untrusted update signal signing key")
	}
	if s.Signing.PublicKey != "" && s.Signing.PublicKey != trusted {
		return errors.New("update signal signing key mismatch")
	}
	// Matched by EQUALITY against the one constant this lane may use, never by
	// sigdomain.Registered(): a registry lookup would accept the manifest and
	// module domains here, which is precisely the cross-protocol replay the
	// registry exists to prevent (Seal Council 2026-07-31, carried over from
	// Manifest.assertSignature).
	if strings.TrimSpace(s.Signing.StatementDomain) != sigdomain.DomainUpdateSignalV1 {
		return fmt.Errorf("update signal must be signed under %s, not %q", sigdomain.DomainUpdateSignalV1, s.Signing.StatementDomain)
	}
	publicKey, err := decodeTrustedPublicKey(trusted)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(s.Signing.Signature)
	if err != nil {
		return errors.New("invalid update signal signature encoding")
	}
	statement, err := SignalStatement(s.raw)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, statement, signature) {
		return errors.New("invalid update signal signature")
	}
	return nil
}

// SignalFromFeedEntry builds an unsigned signal from a feed index entry. The
// publisher's index and its signed manifests are written in the same act and
// must agree, so deriving the signal from the index rather than restating it by
// hand keeps a third copy of the same facts from existing.
func SignalFromFeedEntry(entry ProviderFeedUpdate, feedBaseURL string, publishedAt time.Time, rollback bool) *Signal {
	return &Signal{
		Schema:      SignalSchema,
		UpdateID:    entry.UpdateID,
		Version:     entry.Version,
		Sequence:    entry.Sequence,
		Channel:     entry.Channel,
		Target:      entry.Target,
		FeedBaseURL: strings.TrimSpace(feedBaseURL),
		ManifestURL: entry.ManifestURL,
		CarrierURL:  entry.CarrierURL,
		BundleHash:  entry.BundleHash,
		BundleSize:  entry.BundleSize,
		WasmHash:    entry.WasmHash,
		WasmSize:    entry.WasmSize,
		Rollback:    rollback,
		PublishedAt: nowOr(publishedAt).UTC().Format(time.RFC3339),
		ExpiresAt:   nowOr(publishedAt).Add(SignalDefaultTTL).UTC().Format(time.RFC3339),
		Signing: SignalSigning{
			Algorithm:       SignalSignatureAlgorithm,
			StatementDomain: sigdomain.DomainUpdateSignalV1,
		},
	}
}
