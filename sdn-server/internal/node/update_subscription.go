// G1 — the update-channel subscriber RECEIVE SPINE.
//
// This file wires the already-built signed-update primitives in
// internal/update (manifest verification, G2 per-peer encryption, G3
// two-phase Kubo-first apply) to a pub/sub topic: UpdateSubscriber joins an
// update announcement topic, and for every message verifies the signed
// manifest, decrypts this node's per-peer envelope, stages the verified
// bundle, and — ONLY when explicitly enabled — applies it in place.
//
// Auto-apply is DEFAULT OFF (UpdateSubscriberDeps.AutoApply's zero value is
// false, and NewUpdateSubscriber never flips it). Flipping the default on
// is an owner/release-gated decision tied to a coordinator config change,
// not something this mechanism does on its own. Until then this subscriber
// only ever stages a verified update and logs that it is awaiting operator
// enablement.
//
// This file is intentionally self-contained: UpdateSubscriber takes
// explicit dependencies (a topic subscriber, update.Paths, trusted roots,
// this node's decrypt identity, an optional KuboPhaseHook, a sequence
// source, and the auto-apply flag) rather than a *Node, so it is
// unit-testable without a live libp2p host or pubsub swarm. node.go is not
// modified by this change; see the coordinator wiring notes recorded
// alongside this task's report for exactly where to construct and start
// this from node.go, and the new config field it needs.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// UpdateAnnouncementSchemaV1 identifies the wire envelope UpdateSubscriber
// expects on the update pub/sub topic (the bundle-root manifest.json's
// update.pubsubTopic, e.g. "/sdn/updates/v1/<channel>" — see
// cmd/spacedatanetwork/release_cli.go's verifyPortableCLIManifest and
// deployment/release/build-self-contained-cli.mjs): the signed update
// manifest (internal/update.Manifest, schema org.spacedatanetwork.update.v1)
// plus the G2 per-peer-encrypted bundle carrier
// (internal/update.EncryptedBundle, schema
// org.spacedatanetwork.update.envelope.v1). Both sub-messages are kept as
// raw JSON so update.ParseManifest / update.ParseEncryptedBundle perform
// their own schema and shape validation on exactly the bytes the
// signer/sealer produced, rather than round-tripping through an
// intermediate struct that could silently drop or reorder fields the
// signature covers.
const UpdateAnnouncementSchemaV1 = "org.spacedatanetwork.update.announcement.v1"

// UpdateAnnouncement is the pub/sub wire envelope for one signed, per-peer
// encrypted update announcement.
type UpdateAnnouncement struct {
	Schema   string          `json:"schema"`
	Manifest json.RawMessage `json:"manifest"`
	Bundle   json.RawMessage `json:"bundle"`
}

// NewUpdateAnnouncement builds the wire envelope from a signed manifest
// document and a G2-sealed bundle (update.EncryptCarrierForRecipients'
// result). Exported so a publish-side tool (release tooling, or a future
// G4 built-in-module updater) can build the exact bytes UpdateSubscriber
// consumes without duplicating the wire shape.
func NewUpdateAnnouncement(manifestBytes []byte, bundle *update.EncryptedBundle) (*UpdateAnnouncement, error) {
	if len(manifestBytes) == 0 {
		return nil, errors.New("node: update announcement requires manifest bytes")
	}
	if bundle == nil {
		return nil, errors.New("node: update announcement requires an encrypted bundle")
	}
	bundleBytes, err := bundle.Marshal()
	if err != nil {
		return nil, fmt.Errorf("node: marshal encrypted update bundle: %w", err)
	}
	return &UpdateAnnouncement{
		Schema:   UpdateAnnouncementSchemaV1,
		Manifest: json.RawMessage(append([]byte(nil), manifestBytes...)),
		Bundle:   json.RawMessage(bundleBytes),
	}, nil
}

// Marshal serializes the announcement for publishing. Schema defaults to
// UpdateAnnouncementSchemaV1 if unset.
func (a *UpdateAnnouncement) Marshal() ([]byte, error) {
	if a.Schema == "" {
		a.Schema = UpdateAnnouncementSchemaV1
	}
	return json.Marshal(a)
}

// updateTopicSubscription is the minimal read surface UpdateSubscriber
// needs from a joined pub/sub subscription: block for the next message's
// raw bytes, or return ctx's error once ctx is done/cancelled. A thin
// adapter over *pubsub.Subscription's Next method (see
// pubSubTopicSubscriber below, and internal/pubsub.TipQueue's receiveLoop
// for the read-loop convention this mirrors) satisfies this for
// production use; tests supply an in-memory fake.
type updateTopicSubscription interface {
	Next(ctx context.Context) ([]byte, error)
}

// updateTopicSubscriber joins/subscribes a pub/sub topic by name and
// returns a handle to read announcement messages from it. Kept minimal and
// decoupled from *Node so UpdateSubscriber stays unit-testable without a
// live libp2p host.
type updateTopicSubscriber interface {
	Subscribe(topic string) (updateTopicSubscription, error)
}

// pubSubTopicSubscriber adapts a raw *pubsub.PubSub (the same handle
// exposed by Node.PubSub(), github.com/libp2p/go-libp2p-pubsub) to
// updateTopicSubscriber, joining the requested topic on first use and
// caching the *pubsub.Topic — mirroring Node.joinAndStoreTopic's
// join-once behavior for the general PublishToTopic path (node.go). Safe
// for concurrent use. This is the production adapter the coordinator
// passes UpdateSubscriberDeps.Subscriber as; see the wiring notes for the
// one-line construction.
type pubSubTopicSubscriber struct {
	ps *pubsub.PubSub

	mu     sync.Mutex
	topics map[string]*pubsub.Topic
}

// newPubSubTopicSubscriber builds a pubSubTopicSubscriber over ps. ps must
// be non-nil (a running node's pubsub router); callers should not
// construct an UpdateSubscriber before pubsub is up.
func newPubSubTopicSubscriber(ps *pubsub.PubSub) *pubSubTopicSubscriber {
	return &pubSubTopicSubscriber{ps: ps, topics: make(map[string]*pubsub.Topic)}
}

func (a *pubSubTopicSubscriber) Subscribe(topic string) (updateTopicSubscription, error) {
	if a == nil || a.ps == nil {
		return nil, errors.New("node: update pub/sub router is not running")
	}
	a.mu.Lock()
	joinedTopic, ok := a.topics[topic]
	if !ok {
		joined, err := a.ps.Join(topic)
		if err != nil {
			a.mu.Unlock()
			return nil, fmt.Errorf("node: join update topic %s: %w", topic, err)
		}
		a.topics[topic] = joined
		joinedTopic = joined
	}
	a.mu.Unlock()

	sub, err := joinedTopic.Subscribe()
	if err != nil {
		return nil, fmt.Errorf("node: subscribe update topic %s: %w", topic, err)
	}
	return pubSubSubscriptionAdapter{sub: sub}, nil
}

// pubSubSubscriptionAdapter adapts *pubsub.Subscription.Next (which returns
// a *pubsub.Message) to updateTopicSubscription.Next (which returns the
// message's raw bytes), the shape UpdateSubscriber's read loop consumes.
type pubSubSubscriptionAdapter struct {
	sub *pubsub.Subscription
}

func (a pubSubSubscriptionAdapter) Next(ctx context.Context) ([]byte, error) {
	msg, err := a.sub.Next(ctx)
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

// UpdateSubscriberDeps are the explicit dependencies UpdateSubscriber
// needs. Nothing here is a *Node — every field is a narrow, individually
// fakeable/testable value — so UpdateSubscriber can be constructed and
// exercised in a unit test without a live libp2p host, pubsub swarm, or
// real node identity.
type UpdateSubscriberDeps struct {
	// Subscriber joins the update topic. Production callers pass
	// newPubSubTopicSubscriber(n.PubSub()); tests pass an in-memory fake.
	Subscriber updateTopicSubscriber
	// Topic is the update announcement pub/sub topic to join, e.g.
	// "/sdn/updates/v1/<channel>" (the running bundle's manifest.json
	// update.pubsubTopic field). Required.
	Topic string
	// Paths is the update working tree for the running bundle
	// (update.PathsFor(bundleRoot)). Required.
	Paths update.Paths
	// TrustedRoots gates manifest signature verification
	// (update.LoadTrustRoots(Paths) at construction time is the normal
	// source). Required — a subscriber with no trusted roots can verify
	// nothing, so NewUpdateSubscriber rejects an empty set rather than
	// silently accepting every announcement.
	TrustedRoots update.TrustedRoots
	// RecipientKeyID is this node's opaque G2 envelope selector — the same
	// value a bundle sealer put in the matching EnvelopeRecipient.KeyID
	// when it called update.EncryptCarrierForRecipients for this node
	// (see internal/channelkeys.Channel.WrapForMembers for the
	// established KeyID-is-a-stable-member-identifier convention this
	// should mirror; the coordinator wiring notes recommend
	// []byte(n.PeerID().String())). Required.
	RecipientKeyID []byte
	// RecipientPrivateKey is this node's X25519 decrypt private key
	// (32 bytes), used with RecipientKeyID to unwrap the per-peer content
	// key via update.DecryptCarrierForRecipient. Required.
	RecipientPrivateKey []byte
	// KuboPhaseHook drives Kubo process control around the Kubo-first
	// phase of a two-phase apply (see update.KuboPhaseHook). Nil uses
	// update.NoopKuboPhaseHook — no process control, which is the required
	// posture until a coordinator wires a real stop/restart+health hook at
	// release.
	KuboPhaseHook update.KuboPhaseHook
	// InstalledKuboVersion, when set, is threaded into every Validate/Apply
	// call so a manifest's compatibility.min_kubo_version gate (G3) is
	// enforced. Left empty (the default), the gate is skipped — back
	// compat, matching update.ApplyOptions.InstalledKuboVersion's own
	// documented default.
	InstalledKuboVersion string
	// CurrentSequence, when set, supplies the monotonic sequence number
	// used for manifest freshness/rollback checks instead of the default
	// (reading update.LoadState(Paths).Sequence on every message). Tests
	// use this to simulate a specific on-disk state without writing one.
	CurrentSequence func() int64
	// Now, when set, replaces time.Now for verification/apply timestamps.
	// Test seam; production callers leave it nil.
	Now func() time.Time
	// AutoApply gates whether a verified, decrypted, staged update is also
	// applied in place. The zero value (false) is REQUIRED default
	// behavior: activation is an owner/release-gated decision (tie to a
	// coordinator config flag, not a code default — see the coordinator
	// wiring notes). When false, UpdateSubscriber only ever stages a
	// verified update and logs that it is awaiting operator/enablement; it
	// never mutates the running bundle.
	AutoApply bool
}

// UpdateSubscriber is the G1 receive spine: it joins an update-channel
// pub/sub topic and, for each message, verifies the signed manifest,
// decrypts this node's per-peer envelope (G2), stages the verified bundle,
// and — only when AutoApply is enabled — applies it via the two-phase
// Kubo-first path (G3) with rollback-on-failure. Every verification/
// decrypt/parse/stage error drops that single message and continues; it
// never applies an unverified or wrongly-addressed bundle, and never blocks
// the read loop on a bad message.
type UpdateSubscriber struct {
	subscriber updateTopicSubscriber
	topic      string
	paths      update.Paths
	roots      update.TrustedRoots

	recipientKeyID []byte
	recipientPriv  []byte

	hook                 update.KuboPhaseHook
	installedKuboVersion string

	sequenceFn func() int64
	now        func() time.Time

	mu        sync.RWMutex
	autoApply bool

	// testAfterHandle, if set, is called synchronously at the end of every
	// handleMessage, after all staging/apply side effects are visible. It
	// is an in-package-only test seam (unexported, only this package's own
	// tests can set it) that lets a Run()-driven test wait for a specific
	// message to finish processing without a sleep-poll loop — the same
	// pattern internal/update/apply.go uses for its own fault injection.
	testAfterHandle func()
}

// NewUpdateSubscriber validates deps and constructs an UpdateSubscriber.
// AutoApply defaults to false (deps.AutoApply's zero value) and
// NewUpdateSubscriber never overrides it — flipping it on is exclusively
// the caller's (coordinator's) explicit choice.
func NewUpdateSubscriber(deps UpdateSubscriberDeps) (*UpdateSubscriber, error) {
	if deps.Subscriber == nil {
		return nil, errors.New("node: update subscriber requires a topic subscriber")
	}
	if strings.TrimSpace(deps.Topic) == "" {
		return nil, errors.New("node: update subscriber requires a pub/sub topic")
	}
	if len(deps.TrustedRoots) == 0 {
		return nil, errors.New("node: update subscriber requires trusted update roots")
	}
	if len(deps.RecipientKeyID) == 0 {
		return nil, errors.New("node: update subscriber requires a recipient key id")
	}
	if len(deps.RecipientPrivateKey) == 0 {
		return nil, errors.New("node: update subscriber requires a recipient private key")
	}

	hook := deps.KuboPhaseHook
	if hook == nil {
		hook = update.NoopKuboPhaseHook
	}
	nowFn := deps.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	return &UpdateSubscriber{
		subscriber:           deps.Subscriber,
		topic:                strings.TrimSpace(deps.Topic),
		paths:                deps.Paths,
		roots:                deps.TrustedRoots,
		recipientKeyID:       append([]byte(nil), deps.RecipientKeyID...),
		recipientPriv:        append([]byte(nil), deps.RecipientPrivateKey...),
		hook:                 hook,
		installedKuboVersion: deps.InstalledKuboVersion,
		sequenceFn:           deps.CurrentSequence,
		now:                  nowFn,
		autoApply:            deps.AutoApply,
	}, nil
}

// AutoApply reports whether a staged update is also applied in place.
func (s *UpdateSubscriber) AutoApply() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.autoApply
}

// SetAutoApply toggles auto-apply at runtime (e.g. a coordinator reacting
// to a config reload or an operator action). Callers outside this package's
// tests should treat flipping this to true as owner/release-gated — see
// UpdateSubscriberDeps.AutoApply's doc comment.
func (s *UpdateSubscriber) SetAutoApply(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoApply = enabled
}

// RecoverPending wraps update.RecoverPendingApply for the coordinator to
// call at boot, before this subscriber (or anything else) touches the
// bundle: it detects and undoes a two-phase apply that crashed between the
// Kubo phase committing and the SDN/main phase completing, so the bundle
// root is guaranteed consistent before normal operation resumes.
func (s *UpdateSubscriber) RecoverPending() (recovered bool, err error) {
	return update.RecoverPendingApply(s.paths)
}

// Run joins the update topic and blocks, processing one announcement at a
// time, until ctx is done or the subscribe itself fails. A per-message
// error (parse, verify, decrypt, stage, apply) is logged and the loop
// continues; it is never fatal to Run.
func (s *UpdateSubscriber) Run(ctx context.Context) error {
	sub, err := s.subscriber.Subscribe(s.topic)
	if err != nil {
		return fmt.Errorf("node: subscribe update topic %s: %w", s.topic, err)
	}
	for {
		data, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Warnf("update subscriber: error reading topic %s: %v", s.topic, err)
			continue
		}
		s.handleMessage(data)
	}
}

// resolveSequence returns the monotonic sequence number used for manifest
// freshness/rollback checks: sequenceFn if the caller supplied one,
// otherwise the bundle's on-disk update state (0 if unreadable/absent, the
// same fallback update.LoadState itself uses for a never-updated bundle).
func (s *UpdateSubscriber) resolveSequence() int64 {
	if s.sequenceFn != nil {
		return s.sequenceFn()
	}
	state, err := update.LoadState(s.paths)
	if err != nil {
		return 0
	}
	return state.Sequence
}

// handleMessage parses, verifies, decrypts, and stages one announcement
// message, applying it only if AutoApply is enabled. Every failure mode
// drops the message (logs and returns) rather than propagating an error,
// so one bad/hostile message never stalls Run's read loop and never
// results in a partially-verified bundle being staged or applied.
func (s *UpdateSubscriber) handleMessage(data []byte) {
	if s.testAfterHandle != nil {
		defer s.testAfterHandle()
	}

	var ann UpdateAnnouncement
	if err := json.Unmarshal(data, &ann); err != nil {
		log.Debugf("update subscriber: dropping malformed announcement on %s: %v", s.topic, err)
		return
	}
	if ann.Schema != UpdateAnnouncementSchemaV1 {
		log.Debugf("update subscriber: dropping announcement on %s with unsupported schema %q", s.topic, ann.Schema)
		return
	}

	manifest, err := update.ParseManifest(ann.Manifest)
	if err != nil {
		log.Debugf("update subscriber: dropping announcement on %s: %v", s.topic, err)
		return
	}
	encBundle, err := update.ParseEncryptedBundle(ann.Bundle)
	if err != nil {
		log.Debugf("update subscriber: dropping announcement %s: %v", manifest.UpdateID, err)
		return
	}

	verifyOpts := update.HostVerifyOptions(s.roots, s.resolveSequence(), s.now())
	verifyOpts.InstalledKuboVersion = s.installedKuboVersion

	// Pre-decrypt gate: verify everything the manifest itself asserts —
	// signature, target platform/arch, expiration, sequence/rollback
	// policy, Kubo-version compatibility — using the manifest's own
	// self-declared bundle hash. An unsigned, forged, untrusted-signer,
	// expired, or rolled-back-without-authorization announcement is
	// rejected here, before any per-peer decrypt is attempted. This does
	// NOT yet prove the encrypted payload actually hashes to what the
	// manifest claims (that requires the plaintext, which does not exist
	// yet at this point) — Stage below re-verifies end to end against the
	// real decrypted bytes, so a manifest that lies about its own bundle
	// hash is still caught rather than silently trusted.
	if _, err := manifest.Validate(manifest.Bundle.Hash, verifyOpts); err != nil {
		log.Warnf("update subscriber: rejected announcement %s on %s: %v", manifest.UpdateID, s.topic, err)
		return
	}

	plainCarrier, err := update.DecryptCarrierForRecipient(encBundle, s.recipientKeyID, s.recipientPriv)
	if err != nil {
		if errors.Is(err, update.ErrEnvelopeNotForRecipient) {
			log.Debugf("update subscriber: announcement %s is not addressed to this node; dropping", manifest.UpdateID)
		} else {
			log.Warnf("update subscriber: decrypt announcement %s failed: %v", manifest.UpdateID, err)
		}
		return
	}

	// Stage re-parses the manifest and re-verifies it (signature, target,
	// expiration, sequence, compatibility) against the DECRYPTED wasm/
	// bundle bytes — the real, end-to-end hash/size check that a tampered
	// or mismatched ciphertext cannot pass even with a validly-signed
	// manifest attached.
	staged, err := update.Stage(s.paths, ann.Manifest, plainCarrier, verifyOpts)
	if err != nil {
		log.Warnf("update subscriber: staging announcement %s failed: %v", manifest.UpdateID, err)
		return
	}

	if !s.AutoApply() {
		log.Infof("update subscriber: staged signed update %s (version %s, sequence %d); auto-apply is disabled, awaiting operator/enablement", staged.UpdateID, staged.Result.Version, staged.Result.Sequence)
		return
	}

	s.applyStaged(staged)
}

// applyStaged runs the two-phase Kubo-first apply (G3) for a staged,
// verified update. update.Apply already performs rollback-on-failure
// internally (it restores the pre-apply bundle state and moves the failed
// payload to updates/failed/<update-id>/ on any swap error), so this only
// needs to surface the outcome.
func (s *UpdateSubscriber) applyStaged(staged *update.StagedUpdate) {
	result, err := update.Apply(s.paths, update.ApplyOptions{
		UpdateID:             staged.UpdateID,
		Now:                  s.now(),
		KuboPhaseHook:        s.hook,
		InstalledKuboVersion: s.installedKuboVersion,
	})
	if err != nil {
		log.Errorf("update subscriber: auto-apply of %s failed (rolled back): %v", staged.UpdateID, err)
		return
	}
	log.Infof("update subscriber: auto-applied update %s (version %s, sequence %d, two_phase=%v)", result.UpdateID, result.Version, result.Sequence, result.TwoPhase)
}
