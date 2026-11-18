package node

// THE RECEIVE HALF OF THE PUSH LANE.
//
// OWNER RULING 2026-08-09: "We should be building locally and then pushing an
// update signal to all installs to upgrade in place, and only save the last
// five binaries for rollback purposes. That's the point of the update server."
//
// This is the "all installs upgrade in place" half. A publisher puts a signed
// artifact on the feed and emits ONE small signal on the channel topic the
// fleet already speaks; every daemon running this code hears it and upgrades
// ITSELF. No ssh, no operator on the box, no polling timer.
//
// The order of operations is the design:
//
//  1. VERIFY THE POINTER (cheap, in-process). Signature against the same trust
//     roots that gate manifests, under its own statement domain; target, channel,
//     sequence, freshness, lineage, and the local quarantine; and its manifest
//     and carrier URLs must lie under THIS bundle's own feed (manifest.json
//     update.feedBaseUrl), never a host the signal names. Nothing is fetched
//     until all of that passes, so a hostile topic cannot make this box spend
//     bandwidth, let alone disk, or contact a host of the signer's choosing.
//  2. FETCH AND STAGE, ONLINE. The daemon keeps serving while it downloads the
//     manifest and carrier over HTTPS, re-verifies the signed manifest from
//     scratch against the same roots, checks the index/manifest agreement, and
//     stages the payload. Everything here is reversible by deleting a directory.
//  3. HAND OFF THE SWAP. Only now does anything irreversible happen, and it
//     happens in a process that can outlive this one (see update.LaunchSelfUpgrade
//     for why that process must escape the daemon's cgroup). The helper stops the
//     daemon through the loopback control endpoint, applies, lets the supervisor
//     restart it, health-checks, and reverses to the previous slot if it does not
//     come up.
//
// WHY NOT REUSE THE G1 SUBSCRIBER IN update_subscription.go. That one carries
// the whole per-peer-encrypted bundle on the topic. It has never been wired to
// anything, and it could not have worked if it were: a bundle is ~20 MB against
// a 1 MiB gossipsub message limit. It also duplicates the feed's authority. It
// stays where it is, unused, until someone decides its fate; this file is
// deliberately a separate, small, pointer-only lane.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/spacedatanetwork/sdn-server/internal/adminaddr"
	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spacedatanetwork/sdn-server/internal/walletderive"
)

const (
	// signalFetchTimeout bounds the manifest+carrier download. Generous: a
	// 20 MB carrier over a saturated 1-vCPU box's uplink is not fast, and a
	// timeout here means the box silently stays behind.
	signalFetchTimeout = 10 * time.Minute

	// maxManifestFetchBytes / maxCarrierFetchBytes bound what a feed may hand
	// this box. The manifest is ~4 KB and the carrier ~20 MB; the caps are
	// deliberately loose enough not to be a version-to-version tripwire and
	// tight enough that a hostile feed cannot fill the disk.
	maxManifestFetchBytes = 64 << 20
	maxCarrierFetchBytes  = 2 << 30
)

// UpdateSignalSubscriberDeps are the explicit dependencies. Nothing here is a
// *Node, so the subscriber is unit-testable without a libp2p host, a pubsub
// swarm, a bundle on disk, or a live feed.
type UpdateSignalSubscriberDeps struct {
	CheckInterval time.Duration
	CheckDelay    time.Duration

	// Subscriber joins the signal topic. Production passes the same
	// pubSubTopicSubscriber adapter the G1 spine defines.
	Subscriber updateTopicSubscriber
	// Topic is the channel's signal topic (bundle manifest update.pubsubTopic).
	Topic string
	// Paths is the running bundle's update working tree.
	Paths update.Paths
	// TrustedRoots gates both the signal signature and the manifest signature.
	// Empty is refused at construction: a subscriber that trusts nothing can
	// verify nothing, and one that trusts everything is worse.
	TrustedRoots update.TrustedRoots
	// Channel is the channel this box accepts signals for.
	Channel string
	// FeedBaseURL is this bundle's own update feed (manifest.json
	// update.feedBaseUrl). A signal is acted on only when its manifest and
	// carrier lie under it, and every fetch (redirects included) stays under
	// it. Required: a subscriber that would follow a pointer anywhere lets the
	// holder of any trusted key — and SEC-001 showed a trusted key can be
	// public — send every install to a host of its choosing.
	FeedBaseURL string
	// Kind is the artifact kind this bundle is (e.g. "cli-bundle").
	Kind string
	// AdminURL is this daemon's own admin base URL, used by the helper for the
	// shutdown handshake and the health gate.
	AdminURL string
	// AdminCAFile is the certificate THIS daemon serves. Handed to the helper
	// so its loopback calls verify against the running daemon's own anchor
	// rather than re-deriving one from an ambient environment.
	AdminCAFile string
	// HealthTimeout bounds the helper's post-restart health wait.
	HealthTimeout time.Duration
	// StoreRoot is this daemon's record store (storage.path). A staged update
	// whose binary does not open the store's on-disk format is refused before
	// the helper is launched, so a refusal never stops this daemon; the helper
	// is handed the same root for Apply's own check and for any rollback.
	StoreRoot string
	// MinInterval is the floor between two self-upgrades on this box.
	MinInterval time.Duration
	// RolloutWindow is update.rollout_window_seconds: this node acts on an
	// accepted update at its own delay within the window (update.RolloutDelay).
	// Zero acts at once. A signal's signed rollout_window_seconds replaces it.
	RolloutWindow time.Duration
	// PeerID is this node's peer id, which (with the update id) fixes its
	// delay within the window.
	PeerID string
	// Fleet receives the update status messages other nodes publish on the
	// topic, and is told when this node's own update state changes. Nil
	// ignores them.
	Fleet *updateFleet
	// Client fetches the manifest and carrier. Nil builds a default.
	Client *http.Client
	// Now replaces time.Now. Test seam.
	Now func() time.Time
	// Launch performs the swap handoff. Nil uses update.LaunchSelfUpgrade —
	// tests substitute it so the suite never actually swaps a bundle.
	Launch func(update.Paths, update.SelfUpgradeOptions) (*update.SelfUpgradeLaunch, error)
	// Alerts is the node's operational alert registry. A box that cannot
	// install a pushed update is a box that silently falls behind the fleet,
	// which is only visible if someone compares versions by hand — so the
	// failure is raised here and cleared when a swap is handed off. Nil is
	// tolerated (every Registry method is nil-safe).
	Alerts *ops.Registry
	// BundleVersion is the build this daemon runs (update.RunningIdentity). A
	// UI package installs only when it names it.
	BundleVersion string
	// EnvelopeKey returns this node's key for an encrypted package: its
	// sealed-transport encryption key, addressed by its fingerprint. Nil means
	// this node cannot open one.
	EnvelopeKey func() (*update.EnvelopeKey, error)
}

// UpdateSignalSubscriber listens for update signals and upgrades this install.
type UpdateSignalSubscriber struct {
	deps UpdateSignalSubscriberDeps
	feed update.FeedBase

	mu              sync.Mutex
	lastUpgrade     time.Time
	pendingUpgrade  bool
	pendingSignal   []byte
	pendingSequence int64
	// inflightSequence is the sequence of the signal that holds the claim.
	inflightSequence int64
	handled          map[string]bool
	installRefusals  map[string]error
	// handoffFailures counts helper hand-offs that timed out with nothing
	// swapped; handoffRetryAt holds every swap until its backoff ends.
	handoffFailures        int
	handoffRetryAt         time.Time
	feedDeferredCandidates map[string]bool
	// uiAttempts is when each UI package was last tried (claimUI).
	uiAttempts       map[string]time.Time
	reportedFailures map[string]bool
	failedAlerts     map[string]bool
	alertLedgerTime  time.Time
	alertLedgerSize  int64
	alertFilesStamp  string
	// acknowledged is the newest signal this node accepted (verified, for
	// this node, not a refused rollback or a quarantined update).
	acknowledged *update.StatusAck
	// latestVerified is the newest signal of this node's channel and kind
	// that verified against its trust roots (the fleet view's baseline).
	latestVerified *update.StatusAck
	// waitCancel ends the rollout wait in progress, for an update of
	// sequence waitSequence: a newer verified signal replaces it.
	waitCancel   context.CancelFunc
	waitSequence int64
}

// Acknowledged is the newest update signal this node accepted, or nil.
func (s *UpdateSignalSubscriber) Acknowledged() *update.StatusAck {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acknowledged == nil {
		return nil
	}
	ack := *s.acknowledged
	return &ack
}

// acknowledge records signal as the newest one this node accepted, in memory
// and in updates/acknowledged.json, so the node still states it after the
// restart the update causes.
func (s *UpdateSignalSubscriber) acknowledge(signal *update.Signal) {
	s.mu.Lock()
	newer := s.acknowledged == nil || signal.Sequence > s.acknowledged.Sequence
	if newer {
		s.acknowledged = &update.StatusAck{UpdateID: signal.UpdateID, Sequence: signal.Sequence}
	}
	s.mu.Unlock()
	if newer {
		if err := update.WriteAcknowledged(s.deps.Paths, update.StatusAck{UpdateID: signal.UpdateID, Sequence: signal.Sequence}); err != nil {
			log.Warnf("Record the accepted update signal %s: %v", signal.UpdateID, err)
		}
	}
}

// LatestVerified is the newest update signal of this node's channel and kind
// that this node verified against its trust roots, or nil.
func (s *UpdateSignalSubscriber) LatestVerified() *update.StatusAck {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latestVerified == nil {
		return nil
	}
	ack := *s.latestVerified
	return &ack
}

// noteVerified records signal as the newest one this node verified, when it
// is newer than the one held.
func (s *UpdateSignalSubscriber) noteVerified(signal *update.Signal) {
	ack := update.StatusAck{UpdateID: signal.UpdateID, Sequence: signal.Sequence}
	s.mu.Lock()
	newer := s.latestVerified == nil || ack.Sequence > s.latestVerified.Sequence
	if newer {
		s.latestVerified = &ack
	}
	s.mu.Unlock()
	if newer {
		if err := update.WriteLatestSignal(s.deps.Paths, ack); err != nil {
			log.Warnf("Record the newest verified update signal %s: %v", signal.UpdateID, err)
		}
	}
}

// rolloutWindow is the window for signal: its signed rollout_window_seconds
// when it carries one, else this node's update.rollout_window_seconds.
func (s *UpdateSignalSubscriber) rolloutWindow(signal *update.Signal) (time.Duration, string) {
	if signal != nil && signal.RolloutWindowSeconds != nil {
		return time.Duration(*signal.RolloutWindowSeconds) * time.Second, "signal"
	}
	return s.deps.RolloutWindow, "config"
}

// waitForRollout records the update as pending (updates/pending.json, kept
// across a restart, replacing a record of another update) and waits for this
// node's due time. It reports false when the wait ended without acting: ctx
// ended, a newer verified signal replaced this update (the newer one is then
// handled under its own window), or, at the due time, the feed index no
// longer lists the update (withdrawn).
func (s *UpdateSignalSubscriber) waitForRollout(ctx context.Context, signal *update.Signal, raw []byte, trigger string) bool {
	window, from := s.rolloutWindow(signal)
	if window <= 0 {
		// Acts at once; a record of an older update it replaces goes.
		if err := update.ClearPending(s.deps.Paths, ""); err != nil {
			log.Warnf("Update %s: cannot clear the pending record: %v", signal.UpdateID, err)
		}
		s.deps.Fleet.localChanged()
		return true
	}
	record := update.Pending{UpdateID: signal.UpdateID, Version: signal.Version, Sequence: signal.Sequence, Trigger: trigger, WindowFrom: from}
	if len(raw) > 0 {
		record.Signal = append([]byte(nil), raw...)
	}
	pending, err := update.SchedulePending(s.deps.Paths, record, s.deps.PeerID, window, s.deps.Now())
	if err != nil {
		log.Warnf("Update %s: cannot record its rollout wait (%v); acting at once", signal.UpdateID, err)
		return true
	}
	due := pending.Due()
	wait := due.Sub(s.deps.Now())
	if wait <= 0 {
		log.Infof("Update %s (version %s) was due at %s (rollout window %ds from %s, this node's delay %ds); acting now.",
			signal.UpdateID, signal.Version, pending.DueAt, pending.WindowSeconds, pending.WindowFrom, pending.DelaySeconds)
		return true
	}
	log.Infof("Update %s (version %s) accepted at %s; this node acts on it at %s (rollout window %ds from %s, this node's delay %ds). `spacedatanetwork update install` installs it at once.",
		signal.UpdateID, signal.Version, pending.AcceptedAt, pending.DueAt, pending.WindowSeconds, pending.WindowFrom, pending.DelaySeconds)
	s.deps.Fleet.localChanged()
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.waitCancel, s.waitSequence = cancel, signal.Sequence
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.waitCancel, s.waitSequence = nil, 0
		s.mu.Unlock()
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-waitCtx.Done():
		if ctx.Err() == nil {
			log.Infof("Update %s: a newer verified update signal replaces it; its rollout wait ends.", signal.UpdateID)
		}
		return false
	case <-timer.C:
	}
	if s.withdrawnFromFeed(ctx, signal.UpdateID) {
		log.Warnf("Update %s (version %s): rollout due time %s reached, and the feed index no longer lists it (withdrawn); not installing it.",
			signal.UpdateID, signal.Version, pending.DueAt)
		_ = update.ClearPending(s.deps.Paths, signal.UpdateID)
		s.deps.Fleet.localChanged()
		return false
	}
	log.Infof("Update %s: rollout due time %s reached; installing.", signal.UpdateID, pending.DueAt)
	return true
}

// withdrawnFromFeed reads the feed index of this node's channel and target
// and reports true only when it was read and does not list updateID. An
// index that cannot be read withdraws nothing: the install verifies the
// signed manifest either way.
func (s *UpdateSignalSubscriber) withdrawnFromFeed(ctx context.Context, updateID string) bool {
	ctx, cancel := context.WithTimeout(ctx, signalFetchTimeout)
	defer cancel()
	indexURL, err := update.ProviderFeedIndexURL(s.feed.String(), s.deps.Channel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return false
	}
	raw, err := s.fetch(ctx, indexURL, 16<<20)
	if err != nil {
		log.Infof("Update %s: the feed index could not be read at the due time (%v); the install checks the signed manifest.", updateID, err)
		return false
	}
	feed, err := update.ParseProviderFeed(raw)
	if err != nil {
		return false
	}
	for _, entry := range feed.Updates {
		if entry.UpdateID == updateID {
			return false
		}
	}
	return true
}

// verifyForThisNode is the full check a signal passes before this node acts
// on it: signed by a trust root, for this node's platform, kind and channel,
// newer than the installed sequence, not expired.
func (s *UpdateSignalSubscriber) verifyForThisNode(signal *update.Signal, state *update.State) error {
	return signal.Verify(update.SignalVerifyOptions{
		TrustedRoots:    s.deps.TrustedRoots,
		Platform:        runtime.GOOS,
		Arch:            runtime.GOARCH,
		Kind:            s.deps.Kind,
		Channel:         s.deps.Channel,
		CurrentSequence: state.Sequence,
		Now:             s.deps.Now(),
		FeedBaseURL:     s.feed.String(),
	})
}

// resumePending picks up the wait a restart interrupted. Its remaining wait is
// capped at its window. A pending signal is handled again from its kept bytes
// (verified again, due time kept); one that no longer verifies (it expired
// while the node was down) is cleared with a warning, and checkNow asks for a
// feed check at once. A pending feed-check find is returned so a feed check
// runs at its due time.
func (s *UpdateSignalSubscriber) resumePending(ctx context.Context) (pendingFind *update.Pending, checkNow bool) {
	pending, err := update.CurrentPending(s.deps.Paths)
	if err != nil {
		log.Warnf("Read pending update: %v", err)
		return nil, false
	}
	if pending == nil {
		return nil, false
	}
	if capped, err := update.CapPendingDue(s.deps.Paths, pending, s.deps.Now()); err != nil {
		log.Warnf("Update %s: cannot cap its due time: %v", pending.UpdateID, err)
	} else if capped {
		log.Warnf("Update %s: its due time was more than its rollout window (%ds) away (the clock moved back since it was accepted); it is due at %s.",
			pending.UpdateID, pending.WindowSeconds, pending.DueAt)
	}
	log.Infof("Update %s (version %s) is pending from before this start; due at %s.", pending.UpdateID, pending.Version, pending.DueAt)
	if pending.Trigger == "signal" && len(pending.Signal) > 0 {
		raw := append([]byte(nil), pending.Signal...)
		signal, err := update.ParseSignal(raw)
		if err == nil {
			var state *update.State
			if state, err = update.LoadState(s.deps.Paths); err == nil {
				err = s.verifyForThisNode(signal, state)
			}
		}
		if err != nil {
			log.Warnf("Update %s (version %s) was pending, and its signal no longer verifies (%v): the pending record is cleared, and this node checks the feed for the newest release now.",
				pending.UpdateID, pending.Version, err)
			_ = update.ClearPending(s.deps.Paths, pending.UpdateID)
			return nil, true
		}
		go runUpdateGossip("update-signal-resume", func() { s.handle(ctx, raw) })
		return nil, false
	}
	return pending, false
}

// NewUpdateSignalSubscriber validates deps and constructs the subscriber.
func NewUpdateSignalSubscriber(deps UpdateSignalSubscriberDeps) (*UpdateSignalSubscriber, error) {
	if deps.Subscriber == nil {
		return nil, errors.New("node: update signal subscriber requires a topic subscriber")
	}
	if strings.TrimSpace(deps.Topic) == "" {
		return nil, errors.New("node: update signal subscriber requires a topic")
	}
	if strings.TrimSpace(deps.Paths.Root) == "" {
		return nil, errors.New("node: update signal subscriber requires a bundle root")
	}
	if len(deps.TrustedRoots) == 0 {
		return nil, errors.New("node: update signal subscriber requires trusted update roots")
	}
	feed, err := update.ParseFeedBase(deps.FeedBaseURL)
	if err != nil {
		return nil, fmt.Errorf("node: update signal subscriber requires this bundle's update feed: %w", err)
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Client == nil {
		deps.Client = &http.Client{Timeout: signalFetchTimeout}
	}
	if deps.Launch == nil {
		deps.Launch = update.LaunchSelfUpgrade
	}
	return &UpdateSignalSubscriber{deps: deps, feed: feed, handled: make(map[string]bool), uiAttempts: make(map[string]time.Time), reportedFailures: make(map[string]bool),
		acknowledged: update.ReadAcknowledged(deps.Paths), latestVerified: update.ReadLatestSignal(deps.Paths)}, nil
}

// Run joins the topic and processes signals until ctx is done. A per-message
// failure is logged and the loop continues: one malformed or hostile signal
// must never stop a box from hearing the next legitimate one.
func (s *UpdateSignalSubscriber) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.restoreFailedAlerts()
	s.restoreHandoffFailure()

	sub, err := s.deps.Subscriber.Subscribe(s.deps.Topic)
	if err != nil {
		return fmt.Errorf("node: subscribe update signal topic %s: %w", s.deps.Topic, err)
	}
	pendingFind, checkNow := s.resumePending(ctx)
	if s.deps.CheckInterval > 0 {
		done := make(chan struct{})
		go func() { defer close(done); s.runFeedChecks(ctx, pendingFind, checkNow) }()
		defer func() { cancel(); <-done }()
	} else if checkNow || pendingFind != nil {
		// No periodic feed checks: one check, at once or at the pending
		// find's due time.
		wait := time.Duration(0)
		if pendingFind != nil {
			wait = max(time.Until(pendingFind.Due()), 0)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if err := s.checkFeed(ctx); err != nil && ctx.Err() == nil {
				log.Warnf("Feed check: %v", err)
			}
		}()
		defer func() { cancel(); <-done }()
	}
	log.Infof("Update signal lane LIVE on %s (channel %s, kind %s, bundle %s). This install upgrades itself when the publisher signals.",
		s.deps.Topic, s.deps.Channel, s.deps.Kind, s.deps.Paths.Root)
	for {
		data, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Warnf("update signal: error reading topic %s: %v", s.deps.Topic, err)
			continue
		}
		if update.IsStatusMessage(data) {
			runUpdateGossip("update-status", func() { s.handle(ctx, data) })
			continue
		}
		// A signal's handling waits for its rollout due time, then fetches
		// and stages: it runs beside this loop, which keeps reading the
		// topic (other nodes' statuses, and a newer signal that replaces the
		// wait).
		go runUpdateGossip("update-signal", func() { s.handle(ctx, data) })
	}
}

// handle verifies one signal and, if it survives every gate, self-upgrades.
func (s *UpdateSignalSubscriber) handle(ctx context.Context, data []byte) {
	if status, err := update.ParseStatus(data); err == nil {
		// Authenticated by the topic validator: its peer_id is the author.
		s.deps.Fleet.record(status)
		return
	}
	log.Infof("Update signal handler received a validated message")
	signal, err := update.ParseSignal(data)
	if err != nil {
		// Debug, not warn: this topic legitimately carries the older G1
		// announcement schema too, and a box that logs a warning for every
		// message it is not interested in trains its operator to ignore them.
		log.Debugf("update signal: dropping message on %s: %v", s.deps.Topic, err)
		return
	}
	if signal.Target.Kind == update.TargetKindUIBundle {
		s.handleUI(ctx, signal)
		return
	}

	state, err := update.LoadState(s.deps.Paths)
	if err != nil {
		log.Warnf("update signal: cannot read update state, ignoring %s: %v", signal.UpdateID, err)
		return
	}

	// The fleet view's baseline: the newest signal of this channel and kind
	// that verifies against this node's trust roots, whatever this node runs.
	if signal.Verify(update.SignalVerifyOptions{
		TrustedRoots: s.deps.TrustedRoots,
		Kind:         s.deps.Kind,
		Channel:      s.deps.Channel,
		Now:          s.deps.Now(),
		FeedBaseURL:  s.feed.String(),
	}) == nil {
		s.noteVerified(signal)
	}

	if err := s.verifyForThisNode(signal, state); err != nil {
		log.Debugf("update signal %s not actionable here: %v", signal.UpdateID, err)
		return
	}

	// A DECLARED SOURCE-LINEAGE ROLLBACK IS NEVER AUTOMATIC. Going backwards is
	// a legitimate operation and an ordinary mistake, and the only thing that
	// tells them apart is an operator saying so (`update install
	// --allow-rollback`). A broadcast is not an operator, and a fleet that
	// auto-reverses on a signed message is one bad publish away from undoing
	// every lane that landed since.
	if signal.Rollback {
		log.Warnf("update signal %s declares a source-lineage ROLLBACK; this box will NOT install it automatically. Run `spacedatanetwork update install --update-id %s --allow-rollback` on the box to accept it deliberately.",
			signal.UpdateID, signal.UpdateID)
		return
	}

	// QUARANTINE. If this box already installed this update and reversed it,
	// the signal is not news no matter how well signed it is — and because a
	// rollback lowers the installed sequence, the sequence gate alone would let
	// a replay reinstall the exact build this box judged unhealthy, forever.
	if update.HasFailedUpdate(s.deps.Paths, signal.UpdateID) {
		log.Warnf("update signal %s refused: this box already tried that update and marked it failed (updates/failed/%s). Clear that directory to try again deliberately.",
			signal.UpdateID, signal.UpdateID)
		return
	}

	s.acknowledge(signal)
	if !s.claimSignal(signal, data) {
		return
	}

	succeeded := false
	defer func() { s.finishUpgrade(ctx, succeeded, signal.UpdateID) }()
	if !s.waitForRollout(ctx, signal, data, "signal") {
		return
	}
	// The minimum interval between swaps counts from here, not from the
	// start of the wait.
	s.mu.Lock()
	s.lastUpgrade = s.deps.Now()
	s.mu.Unlock()
	// Installed meanwhile (a typed `update install`): nothing to do.
	if state, err = update.LoadState(s.deps.Paths); err != nil {
		log.Warnf("update signal: cannot read update state, ignoring %s: %v", signal.UpdateID, err)
		return
	}
	if state.Sequence >= signal.Sequence {
		_ = update.ClearPending(s.deps.Paths, signal.UpdateID)
		return
	}

	if err := s.upgrade(ctx, signal, state); err != nil {
		s.reportFailure("signal", signal.UpdateID, err)
		s.deps.Alerts.Raise(ops.KindUpdateFailed, signal.UpdateID, ops.SeverityError, err.Error())
		// A store-format refusal is not transient: the store will not step
		// back down on its own, so the update stays claimed rather than being
		// refetched on every duplicate delivery.
		var refusal *update.StoreFormatRefusal
		if !errors.As(err, &refusal) {
			s.release(signal.UpdateID)
		}
		return
	}
	// The helper has the swap. Whatever failed on an earlier attempt at this
	// update is no longer the state of this box.
	succeeded = true
	_ = update.ClearPending(s.deps.Paths, signal.UpdateID)
	s.deps.Alerts.Clear(ops.KindUpdateFailed, signal.UpdateID)
}

// claim enforces once-per-update and the minimum interval between swaps.
func (s *UpdateSignalSubscriber) claimSignal(signal *update.Signal, raw []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := signal.UpdateID
	if s.pendingUpgrade {
		if signal.Sequence > max(s.pendingSequence, s.inflightSequence) {
			if s.pendingSignal != nil {
				log.Debugf("update signal: dropping remembered sequence %d for newer sequence %d", s.pendingSequence, signal.Sequence)
			}
			s.pendingSignal, s.pendingSequence = append([]byte(nil), raw...), signal.Sequence
			if s.waitCancel != nil && signal.Sequence > s.waitSequence {
				// The update in flight is still waiting for its due time:
				// this newer one replaces it, under its own window.
				log.Infof("Update signal %s (sequence %d) replaces the update waiting for its due time (sequence %d)", id, signal.Sequence, s.waitSequence)
				s.waitCancel()
			} else {
				log.Infof("Update signal %s remembered while an update is in flight", id)
			}
		} else {
			log.Debugf("update signal %s dropped: a newer or equal signal is already remembered", id)
		}
		return false
	}
	if s.handled[id] {
		log.Debugf("update signal %s dropped: already handled", id)
		return false
	}
	if s.deps.Now().Before(s.swapAllowedAtLocked()) {
		log.Debugf("update signal %s dropped: minimum upgrade interval or hand-off retry backoff", id)
		return false
	}
	s.pendingUpgrade = true
	s.inflightSequence = signal.Sequence
	s.handled[id] = true
	return true
}

// release re-opens an update for a later attempt after a PRE-SWAP failure
// (fetch, verify, stage). Those are genuinely transient — a feed hiccup should
// not exile a box from an upgrade forever — so the once-only mark is dropped.
//
// What it deliberately does NOT drop is the interval floor. Clearing that would
// remove the rate limit at exactly the moment things are failing, turning a
// broken feed into a retry storm: every duplicate gossipsub delivery would pull
// another 20 MB. So a retry is possible, but not before MinInterval, and the
// failure that needs no retry at all — an update that applied and was reversed —
// is held by the durable updates/failed/ quarantine instead.
func (s *UpdateSignalSubscriber) release(updateID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.handled, updateID)
}

// upgrade fetches, verifies, stages (all while the daemon keeps serving), then
// hands the swap to a process that can outlive this one.
func (s *UpdateSignalSubscriber) upgrade(ctx context.Context, signal *update.Signal, state *update.State) error {
	return s.upgradeTriggered(ctx, signal, state, "signal")
}

func (s *UpdateSignalSubscriber) upgradeTriggered(ctx context.Context, signal *update.Signal, state *update.State, trigger string) error {
	ctx, cancel := context.WithTimeout(ctx, signalFetchTimeout)
	defer cancel()
	log.Infof("Update signal accepted: %s version %s sequence %d (installed sequence %d). Fetching and verifying while this daemon keeps serving.",
		signal.UpdateID, signal.Version, signal.Sequence, state.Sequence)

	manifestBytes, err := s.fetch(ctx, signal.ManifestURL, maxManifestFetchBytes)
	if err != nil {
		return fmt.Errorf("fetch update manifest: %w", err)
	}
	// The SIGNAL and the SIGNED MANIFEST must describe the same artifact. The
	// signal is unauthoritative by design, so this is not a trust check — it is
	// a divergence check, the same one `update install` runs against the feed
	// index. If the pointer and the document disagree, something rewrote one of
	// them and the install stops rather than quietly preferring whichever the
	// code happened to read first.
	parsed, err := update.ParseManifest(manifestBytes)
	if err != nil {
		return fmt.Errorf("parse update manifest: %w", err)
	}
	if err := signalMatchesManifest(signal, parsed, 0); err != nil {
		return err
	}

	verifyOpts := update.HostVerifyOptions(s.deps.TrustedRoots, state.Sequence, s.deps.Now())
	// Validate the signed metadata before spending payload bandwidth or RAM.
	// The hash argument here checks metadata only; Stage independently verifies
	// the actual carrier and extracted bundle bytes against these signed hashes.
	if _, err := parsed.Validate(parsed.Bundle.Hash, verifyOpts); err != nil {
		return fmt.Errorf("verify update manifest: %w", err)
	}
	// A sealed release opens only with this node's key (owner 2026-10-07:
	// "updates even the binary need to be encrypted per node"), so one that
	// was not sealed for this node stops here, before the carrier download.
	var key *update.EnvelopeKey
	if parsed.Envelope != nil {
		if s.deps.EnvelopeKey == nil {
			return errors.New("this update is sealed and this node has no key to open it")
		}
		if key, err = s.deps.EnvelopeKey(); err != nil {
			return fmt.Errorf("load this node's envelope key: %w", err)
		}
		if !parsed.AddressedTo(key.KeyID) {
			return fmt.Errorf("update %s is not sealed for this node (key %s)", parsed.UpdateID, key.KeyID)
		}
	}
	carrierLimit := int64(maxCarrierFetchBytes)
	if signal.WasmSize < 0 || signal.WasmSize > carrierLimit {
		return fmt.Errorf("update carrier size %d is outside the download limit", signal.WasmSize)
	}
	if signal.WasmSize > 0 {
		carrierLimit = signal.WasmSize
	}
	carrierBytes, err := s.fetch(ctx, signal.CarrierURL, carrierLimit)
	if err != nil {
		return fmt.Errorf("fetch update carrier: %w", err)
	}
	if err := signalMatchesManifest(signal, parsed, len(carrierBytes)); err != nil {
		return err
	}
	staged, err := update.Stage(s.deps.Paths, manifestBytes, carrierBytes, verifyOpts, key)
	if err != nil {
		var parseErr *update.CarrierParseError
		if errors.As(err, &parseErr) {
			return fmt.Errorf("parse update carrier: %w", err)
		}
		return fmt.Errorf("stage update: %w", err)
	}
	// The store-format guard, here and not only in the helper's Apply: the
	// helper stops this daemon before it applies, and a refused update must
	// not cost a serving node its uptime.
	if err := update.CheckStagedStoreFormat(s.deps.Paths, staged.UpdateID, s.deps.StoreRoot); err != nil {
		return err
	}
	log.Infof("Update %s staged and verified (version %s, sequence %d). Installing it; this daemon keeps serving until it restarts for it.",
		staged.UpdateID, staged.Result.Version, staged.Result.Sequence)

	launch, err := s.deps.Launch(s.deps.Paths, update.SelfUpgradeOptions{
		UpdateID:      staged.UpdateID,
		AdminURL:      s.deps.AdminURL,
		HealthTimeout: s.deps.HealthTimeout,
		Trigger:       trigger,
		AdminCAFile:   s.deps.AdminCAFile,
		StoreRoot:     s.deps.StoreRoot,
		SignalKeyID:   signal.Signing.KeyID,
		// Never: see the rollback refusal above.
		AllowRollback: false,
	})
	if err != nil {
		return err
	}
	if s.deps.Paths.IsHome() {
		log.Infof("Update %s installed in %s as a trial (%s). This daemon stops and starts again through the installed path, which runs it; it is rolled back if it does not answer ready within %s.",
			staged.UpdateID, s.deps.Paths.Root, launch.Mode, s.deps.HealthTimeout)
		return nil
	}
	log.Infof("Self-upgrade to %s launched (%s%s, pid %d). The helper will stop this daemon, apply, and roll back to the previous slot if it does not come back healthy within %s.",
		staged.UpdateID, launch.Mode, unitSuffix(launch.Unit), launch.PID, s.deps.HealthTimeout)
	return nil
}

// uiRetryFloor spaces out attempts at one UI package, so a feed that fails
// cannot turn duplicate deliveries into a download loop.
const uiRetryFloor = time.Minute

// handleUI is the in-place half of the lane for UI packages (OWNER 2026-10-07:
// "have the running servers update in situ WITHOUT needing to republish the
// binaries"). The same gates as a binary update — the signal's signature,
// channel, freshness, sequence against the installed UI package, the rollback
// refusal and the quarantine — and then no helper and no stop: the package is
// fetched, opened with this node's key, verified file by file and switched in
// while the daemon serves (update.InstallUIPackage).
func (s *UpdateSignalSubscriber) handleUI(ctx context.Context, signal *update.Signal) {
	active, err := update.ActiveUIPackage(s.deps.Paths)
	if err != nil {
		log.Warnf("update signal: cannot read the installed UI package, ignoring %s: %v", signal.UpdateID, err)
		return
	}
	var installed int64
	if active != nil {
		installed = active.Sequence
	}
	if err := signal.Verify(update.SignalVerifyOptions{
		TrustedRoots:    s.deps.TrustedRoots,
		Kind:            update.TargetKindUIBundle,
		Channel:         s.deps.Channel,
		CurrentSequence: installed,
		Now:             s.deps.Now(),
		FeedBaseURL:     s.feed.String(),
	}); err != nil {
		log.Debugf("UI package signal %s not actionable here: %v", signal.UpdateID, err)
		return
	}
	if signal.Rollback {
		log.Warnf("UI package signal %s declares a source-lineage ROLLBACK; this box will NOT install it automatically.", signal.UpdateID)
		return
	}
	if update.HasFailedUpdate(s.deps.Paths, signal.UpdateID) {
		log.Warnf("UI package signal %s refused: this box already tried it and reversed it (updates/failed/%s).", signal.UpdateID, signal.UpdateID)
		return
	}
	if !s.claimUI(signal.UpdateID) {
		return
	}
	pkg, err := s.installUI(ctx, signal, installed)
	if err != nil {
		log.Errorf("UI package %s not installed: %v", signal.UpdateID, err)
		s.deps.Alerts.Raise(ops.KindUpdateFailed, signal.UpdateID, ops.SeverityError, err.Error())
		s.release(signal.UpdateID)
		return
	}
	s.deps.Alerts.Clear(ops.KindUpdateFailed, signal.UpdateID)
	log.Infof("UI package %s (version %s, sequence %d) installed while serving; the dashboard and homepage switch on the next request, with no restart.",
		pkg.UpdateID, pkg.Version, pkg.Sequence)
}

// claimUI is once-per-update for UI packages. Nothing restarts, so there is
// no interval floor between packages, only between attempts at one.
func (s *UpdateSignalSubscriber) claimUI(updateID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handled[updateID] {
		return false
	}
	if last, ok := s.uiAttempts[updateID]; ok && s.deps.Now().Sub(last) < uiRetryFloor {
		return false
	}
	s.handled[updateID] = true
	s.uiAttempts[updateID] = s.deps.Now()
	return true
}

func (s *UpdateSignalSubscriber) installUI(ctx context.Context, signal *update.Signal, installed int64) (*update.UIPackage, error) {
	ctx, cancel := context.WithTimeout(ctx, signalFetchTimeout)
	defer cancel()
	log.Infof("UI package signal accepted: %s version %s sequence %d (installed UI sequence %d). Fetching while this daemon keeps serving.",
		signal.UpdateID, signal.Version, signal.Sequence, installed)

	manifestBytes, err := s.fetch(ctx, signal.ManifestURL, maxManifestFetchBytes)
	if err != nil {
		return nil, fmt.Errorf("fetch UI package manifest: %w", err)
	}
	parsed, err := update.ParseManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	if err := signalMatchesManifest(signal, parsed, 0); err != nil {
		return nil, err
	}
	verifyOpts := update.HostVerifyOptions(s.deps.TrustedRoots, installed, s.deps.Now())
	verifyOpts.RunningBundleVersion = s.deps.BundleVersion
	// The signed metadata, before any payload bandwidth: a package for another
	// build, or one this node was not sealed for, stops here.
	if _, err := parsed.Validate(parsed.Bundle.Hash, verifyOpts); err != nil {
		return nil, fmt.Errorf("verify UI package manifest: %w", err)
	}
	if s.deps.EnvelopeKey == nil {
		return nil, errors.New("this node has no key to open an encrypted UI package")
	}
	key, err := s.deps.EnvelopeKey()
	if err != nil {
		return nil, fmt.Errorf("load this node's envelope key: %w", err)
	}
	if !parsed.AddressedTo(key.KeyID) {
		return nil, fmt.Errorf("UI package %s is not sealed for this node (key %s)", parsed.UpdateID, key.KeyID)
	}
	carrierLimit := int64(maxCarrierFetchBytes)
	if signal.WasmSize < 0 || signal.WasmSize > carrierLimit {
		return nil, fmt.Errorf("UI package carrier size %d is outside the download limit", signal.WasmSize)
	}
	if signal.WasmSize > 0 {
		carrierLimit = signal.WasmSize
	}
	carrier, err := s.fetch(ctx, signal.CarrierURL, carrierLimit)
	if err != nil {
		return nil, fmt.Errorf("fetch UI package carrier: %w", err)
	}
	if err := signalMatchesManifest(signal, parsed, len(carrier)); err != nil {
		return nil, err
	}
	return update.InstallUIPackage(s.deps.Paths, update.UIInstall{
		Manifest:    manifestBytes,
		Carrier:     carrier,
		Verify:      verifyOpts,
		Key:         key,
		Trigger:     "signal",
		SignalKeyID: signal.Signing.KeyID,
	})
}

func unitSuffix(unit string) string {
	if unit == "" {
		return ""
	}
	return " " + unit
}

func (s *UpdateSignalSubscriber) fetch(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "https" {
		return nil, errors.New("update fetch requires HTTPS")
	}
	// Only this bundle's own feed: checked again here, at the fetch, so no
	// path into this function can contact another host.
	if err := s.feed.Contains(rawURL, "update fetch"); err != nil {
		return nil, err
	}
	// Preserve the caller's transport (including Tor) and redirect policy,
	// while refusing a downgrade, or a hop off this bundle's feed, before
	// contacting the redirected endpoint.
	client := *s.deps.Client
	originalRedirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != "https" {
			return errors.New("update fetch redirected away from HTTPS")
		}
		if err := s.feed.Contains(next.URL.String(), "update fetch redirect"); err != nil {
			return err
		}
		if originalRedirect != nil {
			return originalRedirect(next, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned HTTP %d", rawURL, resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", rawURL, limit)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", rawURL, limit)
	}
	return body, nil
}

// signalMatchesManifest reuses the feed index's divergence check by expressing
// the signal as the index entry it was derived from. One implementation, one
// set of rules, no second place for the two to drift.
func signalMatchesManifest(signal *update.Signal, manifest *update.Manifest, carrierLen int) error {
	entry := update.ProviderFeedUpdate{
		UpdateID:   signal.UpdateID,
		Version:    signal.Version,
		Sequence:   signal.Sequence,
		Channel:    signal.Channel,
		Target:     signal.Target,
		BundleHash: signal.BundleHash,
		BundleSize: signal.BundleSize,
		WasmHash:   signal.WasmHash,
		WasmSize:   signal.WasmSize,
	}
	if err := entry.AssertMatchesPayload(manifest, carrierLen); err != nil {
		return fmt.Errorf("update signal and signed manifest disagree: %w", err)
	}
	return nil
}

// startUpdateSignalSubscriber wires the lane from Start(). It is deliberately
// non-fatal in every direction: a box that cannot run the lane must still boot
// and serve. What it must never do is fail SILENTLY — an install that is not
// listening looks exactly like a publisher that never signaled, and telling
// those apart after the fact has cost this fleet whole afternoons.
func (n *Node) startUpdateSignalSubscriber() {
	if n == nil {
		return
	}
	cfg := n.config.Update
	if !cfg.Enabled {
		log.Infof("Update signal lane DISABLED by config (update.enabled=false). This install will not upgrade itself; roll it by hand and record the reason.")
		return
	}
	layout := bundle.ResolveCurrent()
	if layout.Root == "" {
		log.Infof("Update signal lane not started: this daemon is not running from a self-contained SDN bundle, so there is nothing for an in-place upgrade to swap.")
		return
	}
	paths := update.PathsFor(layout.Root)
	if paths.HomeErr != nil {
		log.Warnf("Update signal lane NOT started: %v. This install will not upgrade itself.", paths.HomeErr)
		return
	}
	if paths.IsHome() {
		log.Infof("Updates install into the home bundle %s: this account cannot write the install folder %s. This process runs the build in %s.", paths.Root, paths.Installed, layout.Root)
	}

	manifest, err := readBundleUpdateMetadata(layout.ManifestPath)
	if err != nil {
		log.Warnf("Update signal lane NOT started: %v. This install will not hear pushed updates.", err)
		return
	}
	channel := strings.TrimSpace(cfg.Channel)
	if channel == "" {
		channel = manifest.Channel
	}
	topic := strings.TrimSpace(cfg.Topic)
	if topic == "" {
		topic = strings.TrimSpace(manifest.Update.PubsubTopic)
	}
	if topic == "" {
		topic = update.SignalTopic(channel)
	}

	roots, err := update.LoadTrustRoots(paths)
	if err != nil {
		log.Warnf("Update signal lane NOT started: %v. Without trust roots nothing could be verified, so listening would be worse than not listening.", err)
		return
	}

	subscriber, err := NewUpdateSignalSubscriber(UpdateSignalSubscriberDeps{
		CheckInterval: resolvedCheckInterval(cfg.CheckIntervalSeconds),
		CheckDelay:    time.Duration(cfg.CheckDelaySeconds) * time.Second,
		Subscriber:    nodeTopicSubscriber{node: n},
		Topic:         topic,
		Paths:         paths,
		TrustedRoots:  roots,
		Channel:       channel,
		FeedBaseURL:   manifest.Update.FeedBaseURL,
		Kind:          manifest.Kind(),
		AdminURL:      n.localAdminURL(),
		AdminCAFile:   adminaddr.DaemonCertPath(n.config.Admin.TLSCertFile, n.config.Admin.TLSCacheDir),
		HealthTimeout: cfg.HealthTimeout(),
		StoreRoot:     absPathOr(n.config.Storage.Path),
		MinInterval:   cfg.MinInterval(),
		RolloutWindow: cfg.RolloutWindow(),
		PeerID:        n.host.ID().String(),
		Fleet:         n.updateFleet,
		Client:        n.updateFetchClient(),
		Alerts:        n.alerts,
		BundleVersion: update.RunningIdentity().BundleVersion,
		EnvelopeKey:   n.updateEnvelopeKey,
	})
	if err != nil {
		log.Warnf("Update signal lane NOT started: %v", err)
		return
	}

	// This node's own update status, on the same topic (update_fleet.go).
	if n.updateFleet != nil {
		peerID := n.host.ID().String()
		version := update.RunningIdentity().BundleVersion
		if strings.TrimSpace(version) == "" {
			version = strings.TrimSpace(manifest.Version)
		}
		n.updateFleet.mu.Lock()
		n.updateFleet.local = func() *update.Status {
			return update.LocalStatus(paths, peerID, version, subscriber.Acknowledged(), time.Now())
		}
		n.updateFleet.latest = subscriber.LatestVerified
		n.updateFleet.topic = topic
		n.updateFleet.mu.Unlock()
	}

	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		if err := subscriber.Run(n.ctx); err != nil && n.ctx.Err() == nil {
			log.Warnf("Update signal lane stopped: %v", err)
		}
	}()

	if n.updateFleet != nil {
		if joined, err := n.joinAndStoreTopic(topic, topic); err != nil {
			log.Warnf("Update status not announced: join %s: %v", topic, err)
		} else {
			n.wg.Add(1)
			go func() {
				defer n.wg.Done()
				n.updateFleet.announce(n.ctx, joined)
			}()
		}
	}
}

// absPathOr returns p made absolute (the helper runs from another working
// directory), or p itself when that fails.
func absPathOr(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// nodeTopicSubscriber subscribes through the NODE'S OWN join cache
// (joinAndStoreTopic) rather than calling pubsub.Join directly.
//
// FOUND LIVE, 2026-08-09, on the first real push. go-libp2p-pubsub permits ONE
// Join per topic per host and returns "topic already exists" for a second. The
// publisher is also a subscriber — it runs the same daemon and joins
// /sdn/updates/v1/beta at boot to hear its own channel — so a separate Join for
// the subscriber meant `PublishToTopic` on that exact topic failed with
//
//	the signal was signed but could not be published: join topic
//	/sdn/updates/v1/beta: topic already exists
//
// i.e. the one node in the fleet that must be able to push was the only node
// that could not. Routing both through the node's cache makes the publish and
// the subscription share one handle, which is also what every other topic in
// this daemon already does.
type nodeTopicSubscriber struct{ node *Node }

func (a nodeTopicSubscriber) Subscribe(topic string) (updateTopicSubscription, error) {
	if a.node == nil || a.node.pubsub == nil {
		return nil, errors.New("node: update pub/sub router is not running")
	}
	joined, err := a.node.joinAndStoreTopic(topic, topic)
	if err != nil {
		return nil, fmt.Errorf("node: join update topic %s: %w", topic, err)
	}
	sub, err := joined.Subscribe()
	if err != nil {
		return nil, fmt.Errorf("node: subscribe update topic %s: %w", topic, err)
	}
	return pubSubSubscriptionAdapter{sub: sub}, nil
}

// localAdminURL is how this daemon reaches ITSELF, and it is the value the
// helper needs to stop it and then prove it came back. It goes through
// adminaddr so the daemon and the CLI cannot disagree about which address that
// is — a disagreement there means the helper cannot stop the daemon, reports
// "daemon_shutdown=unavailable", and the box keeps running the old binary while
// the lane reports success.
func (n *Node) localAdminURL() string {
	if n == nil || n.config == nil {
		return ""
	}
	return adminaddr.LocalAdminURL(n.config.Admin.ListenAddr, n.config.Admin.EffectiveTLSMode() != "disabled")
}

// updateFetchClient fetches the manifest and carrier from the public feed.
// System roots, deliberately: the feed is a public HTTPS surface reached by its
// real name, not the daemon's own loopback socket, so the daemon-certificate
// anchor used for admin calls is the wrong trust store here.
func (n *Node) updateFetchClient() *http.Client {
	return &http.Client{Timeout: signalFetchTimeout}
}

// bundleSelfDescription is the subset of the running bundle's manifest.json the
// signal lane needs. It is read directly rather than through the CLI's
// bundleManifest type because internal/node must not depend on package main.
type bundleSelfDescription struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Update  struct {
		FeedBaseURL string `json:"feedBaseUrl"`
		PubsubTopic string `json:"pubsubTopic"`
	} `json:"update"`
	Target struct {
		Kind string `json:"kind"`
	} `json:"target"`
}

// Kind is the artifact kind this bundle is. Shipped bundle manifests do not
// carry one today, and the only kind the fleet publishes is the CLI bundle, so
// that is the fallback — stated here rather than hidden in a caller.
func (b bundleSelfDescription) Kind() string {
	if kind := strings.TrimSpace(b.Target.Kind); kind != "" {
		return kind
	}
	return "cli-bundle"
}

func readBundleUpdateMetadata(manifestPath string) (*bundleSelfDescription, error) {
	if strings.TrimSpace(manifestPath) == "" {
		return nil, errors.New("bundle manifest path is empty")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read bundle manifest %s: %w", manifestPath, err)
	}
	var described bundleSelfDescription
	if err := json.Unmarshal(raw, &described); err != nil {
		return nil, fmt.Errorf("parse bundle manifest %s: %w", manifestPath, err)
	}
	if strings.TrimSpace(described.Channel) == "" {
		return nil, fmt.Errorf("bundle manifest %s declares no channel", manifestPath)
	}
	return &described, nil
}

// updateEnvelopeKey is the key this node opens an encrypted update with: the
// sealed-transport encryption key its /api/node/info advertises and its card
// publishes, addressed by the same fingerprint, derived the way the sealed
// admin transport derives it.
func (n *Node) updateEnvelopeKey() (*update.EnvelopeKey, error) {
	var profile *epm.Profile
	if p, err := epm.LoadProfile(n.config.Storage.Path); err == nil {
		profile = p
	}
	_, encPath := epm.EffectiveKeyPaths(profile, 0)
	priv, _, err := n.SealedTransportKeys(encPath)
	if err != nil {
		return nil, err
	}
	pub := secp256k1.PrivKeyFromBytes(priv).PubKey().SerializeCompressed()
	return &update.EnvelopeKey{KeyID: []byte(walletderive.Fingerprint(pub)), Private: priv}, nil
}
