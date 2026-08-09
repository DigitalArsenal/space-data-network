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
//     sequence, freshness, lineage, and the local quarantine. Nothing is fetched
//     until all of that passes, so a hostile topic cannot make this box spend
//     bandwidth, let alone disk.
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
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/adminaddr"
	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/update"
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
	// MinInterval is the floor between two self-upgrades on this box.
	MinInterval time.Duration
	// MaxDelay spreads a fleet-wide roll. Zero acts immediately.
	MaxDelay time.Duration
	// Client fetches the manifest and carrier. Nil builds a default.
	Client *http.Client
	// Now replaces time.Now. Test seam.
	Now func() time.Time
	// Launch performs the swap handoff. Nil uses update.LaunchSelfUpgrade —
	// tests substitute it so the suite never actually swaps a bundle.
	Launch func(update.Paths, update.SelfUpgradeOptions) (*update.SelfUpgradeLaunch, error)
}

// UpdateSignalSubscriber listens for update signals and upgrades this install.
type UpdateSignalSubscriber struct {
	deps UpdateSignalSubscriberDeps

	mu          sync.Mutex
	lastUpgrade time.Time
	handled     map[string]bool
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
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Client == nil {
		deps.Client = &http.Client{Timeout: signalFetchTimeout}
	}
	if deps.Launch == nil {
		deps.Launch = update.LaunchSelfUpgrade
	}
	return &UpdateSignalSubscriber{deps: deps, handled: make(map[string]bool)}, nil
}

// Run joins the topic and processes signals until ctx is done. A per-message
// failure is logged and the loop continues: one malformed or hostile signal
// must never stop a box from hearing the next legitimate one.
func (s *UpdateSignalSubscriber) Run(ctx context.Context) error {
	sub, err := s.deps.Subscriber.Subscribe(s.deps.Topic)
	if err != nil {
		return fmt.Errorf("node: subscribe update signal topic %s: %w", s.deps.Topic, err)
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
		s.handle(ctx, data)
	}
}

// handle verifies one signal and, if it survives every gate, self-upgrades.
func (s *UpdateSignalSubscriber) handle(ctx context.Context, data []byte) {
	signal, err := update.ParseSignal(data)
	if err != nil {
		// Debug, not warn: this topic legitimately carries the older G1
		// announcement schema too, and a box that logs a warning for every
		// message it is not interested in trains its operator to ignore them.
		log.Debugf("update signal: dropping message on %s: %v", s.deps.Topic, err)
		return
	}

	state, err := update.LoadState(s.deps.Paths)
	if err != nil {
		log.Warnf("update signal: cannot read update state, ignoring %s: %v", signal.UpdateID, err)
		return
	}

	if err := signal.Verify(update.SignalVerifyOptions{
		TrustedRoots:    s.deps.TrustedRoots,
		Platform:        runtime.GOOS,
		Arch:            runtime.GOARCH,
		Kind:            s.deps.Kind,
		Channel:         s.deps.Channel,
		CurrentSequence: state.Sequence,
		Now:             s.deps.Now(),
	}); err != nil {
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
		log.Warnf("update signal %s refused: this box already tried that update and reversed it (updates/failed/%s). Clear that directory to try again deliberately.",
			signal.UpdateID, signal.UpdateID)
		return
	}

	if !s.claim(signal.UpdateID) {
		return
	}

	if delay := s.delay(); delay > 0 {
		log.Infof("update signal %s accepted; staggering the swap by %s.", signal.UpdateID, delay.Round(time.Second))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}

	if err := s.upgrade(ctx, signal, state); err != nil {
		log.Errorf("update signal %s: self-upgrade did not start: %v", signal.UpdateID, err)
		s.release(signal.UpdateID)
		return
	}
}

// claim enforces once-per-update and the minimum interval between swaps.
func (s *UpdateSignalSubscriber) claim(updateID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handled[updateID] {
		return false
	}
	if !s.lastUpgrade.IsZero() {
		if since := s.deps.Now().Sub(s.lastUpgrade); since < s.deps.MinInterval {
			log.Warnf("update signal %s deferred: this box self-upgraded %s ago and the floor is %s.",
				updateID, since.Round(time.Second), s.deps.MinInterval)
			return false
		}
	}
	s.handled[updateID] = true
	s.lastUpgrade = s.deps.Now()
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

func (s *UpdateSignalSubscriber) delay() time.Duration {
	if s.deps.MaxDelay <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(s.deps.MaxDelay)))
}

// upgrade fetches, verifies, stages (all while the daemon keeps serving), then
// hands the swap to a process that can outlive this one.
func (s *UpdateSignalSubscriber) upgrade(ctx context.Context, signal *update.Signal, state *update.State) error {
	log.Infof("Update signal accepted: %s version %s sequence %d (installed sequence %d). Fetching and verifying while this daemon keeps serving.",
		signal.UpdateID, signal.Version, signal.Sequence, state.Sequence)

	manifestBytes, err := s.fetch(ctx, signal.ManifestURL, maxManifestFetchBytes)
	if err != nil {
		return fmt.Errorf("fetch update manifest: %w", err)
	}
	carrierBytes, err := s.fetch(ctx, signal.CarrierURL, maxCarrierFetchBytes)
	if err != nil {
		return fmt.Errorf("fetch update carrier: %w", err)
	}

	// The SIGNAL and the SIGNED MANIFEST must describe the same artifact. The
	// signal is unauthoritative by design, so this is not a trust check — it is
	// a divergence check, the same one `update install` runs against the feed
	// index. If the pointer and the document disagree, something rewrote one of
	// them and the install stops rather than quietly preferring whichever the
	// code happened to read first.
	parsed, err := update.ParseManifest(manifestBytes)
	if err != nil {
		return err
	}
	if err := signalMatchesManifest(signal, parsed, len(carrierBytes)); err != nil {
		return err
	}

	verifyOpts := update.HostVerifyOptions(s.deps.TrustedRoots, state.Sequence, s.deps.Now())
	staged, err := update.Stage(s.deps.Paths, manifestBytes, carrierBytes, verifyOpts)
	if err != nil {
		return fmt.Errorf("stage update: %w", err)
	}
	log.Infof("Update %s staged and verified (version %s, sequence %d). Handing the swap to the helper; this daemon stays up until the helper stops it.",
		staged.UpdateID, staged.Result.Version, staged.Result.Sequence)

	launch, err := s.deps.Launch(s.deps.Paths, update.SelfUpgradeOptions{
		UpdateID:      staged.UpdateID,
		AdminURL:      s.deps.AdminURL,
		HealthTimeout: s.deps.HealthTimeout,
		Trigger:       "signal",
		AdminCAFile:   s.deps.AdminCAFile,
		SignalKeyID:   signal.Signing.KeyID,
		// Never: see the rollback refusal above.
		AllowRollback: false,
	})
	if err != nil {
		return err
	}
	log.Infof("Self-upgrade to %s launched (%s%s, pid %d). The helper will stop this daemon, apply, and roll back to the previous slot if it does not come back healthy within %s.",
		staged.UpdateID, launch.Mode, unitSuffix(launch.Unit), launch.PID, s.deps.HealthTimeout)
	return nil
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
	resp, err := s.deps.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned HTTP %d", rawURL, resp.StatusCode)
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
// listening looks exactly like a publisher that never signalled, and telling
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
		Subscriber:    nodeTopicSubscriber{node: n},
		Topic:         topic,
		Paths:         paths,
		TrustedRoots:  roots,
		Channel:       channel,
		Kind:          manifest.Kind(),
		AdminURL:      n.localAdminURL(),
		AdminCAFile:   adminaddr.DaemonCertPath(n.config.Admin.TLSCertFile, n.config.Admin.TLSCacheDir),
		HealthTimeout: cfg.HealthTimeout(),
		MinInterval:   cfg.MinInterval(),
		MaxDelay:      time.Duration(cfg.MaxDelaySeconds) * time.Second,
		Client:        n.updateFetchClient(),
	})
	if err != nil {
		log.Warnf("Update signal lane NOT started: %v", err)
		return
	}

	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		if err := subscriber.Run(n.ctx); err != nil && n.ctx.Err() == nil {
			log.Warnf("Update signal lane stopped: %v", err)
		}
	}()
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
