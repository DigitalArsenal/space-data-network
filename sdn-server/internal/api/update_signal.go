package api

// THE PUSH — POST /api/v1/admin/updates/signal.
//
// OWNER RULING 2026-08-09: "We should be building locally and then pushing an
// update signal to all installs to upgrade in place... That's the point of the
// update server."
//
// This is the one act that was missing. Everything else in the lane already
// existed: build locally, verify the binary, wrap it, get the manifest signed
// by the bonded node key, put it on the feed. Then a human ssh'd into each box
// and typed `update install`. This endpoint replaces the human.
//
// WHY IT IS A SIBLING OF sign-manifest AND NOT A MODE OF IT. They sign
// different things under different domains for different purposes: a manifest
// AUTHORIZES bytes, a signal POINTS at them. Signing them at one door would
// mean one door that can be made to say two things, which is the property the
// domain registry exists to remove. So: second door, own lock, own domain, same
// admin wall and the same audited key.
//
// WHY THE NODE BUILDS THE DOCUMENT AND THE CALLER DOES NOT. The caller submits
// only a SELECTOR — which published update to announce. The node reads its own
// feed index, finds that entry, and derives every field from it. A caller that
// could hand over a finished signal document could point the fleet at a URL the
// feed never served; a caller that can only name a version can, at worst, name
// one that does not exist and get a 404. The signal is derived from the same
// index the feed serves, so a signal and the feed cannot disagree by
// construction.

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
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spacedatanetwork/sdn-server/internal/updatesign"
)

// UpdateSignalRoute is the single path this surface occupies.
const UpdateSignalRoute = "/api/v1/admin/updates/signal"

// updateSignalMaxBody bounds the selector document. It is a handful of short
// strings; anything larger is not a selector.
const updateSignalMaxBody = 8 << 10

// topicPublisher is defined in coreapi.go: PublishToTopic(ctx, topic, data).

// updateSignalRequest is the SELECTOR. No URLs, no hashes, no timestamps: every
// one of those is read off the feed index the node itself serves.
type updateSignalRequest struct {
	// Channel selects the feed lane. Required.
	Channel string `json:"channel"`
	// Platform/Arch/Kind select the artifact family. Default to this host's
	// own values, which is right for the overwhelmingly common case of a
	// publisher announcing the build it just published for its own fleet.
	Platform string `json:"platform,omitempty"`
	Arch     string `json:"arch,omitempty"`
	Kind     string `json:"kind,omitempty"`
	// UpdateID or Version pins a specific entry. Both empty announces the
	// newest entry in that lane.
	UpdateID string `json:"update_id,omitempty"`
	Version  string `json:"version,omitempty"`
	// Topic overrides the derived topic. Present for a channel whose bundles
	// declare a non-default pubsubTopic; normally omitted.
	Topic string `json:"topic,omitempty"`
	// DryRun signs nothing and publishes nothing: it reports the exact document
	// that WOULD be broadcast. A publisher should be able to see the pointer
	// before the fleet acts on it.
	DryRun bool `json:"dry_run,omitempty"`
}

type updateSignalResponse struct {
	Published   bool   `json:"published"`
	DryRun      bool   `json:"dry_run,omitempty"`
	Topic       string `json:"topic"`
	UpdateID    string `json:"update_id"`
	Version     string `json:"version"`
	Sequence    int64  `json:"sequence"`
	Channel     string `json:"channel"`
	Target      string `json:"target"`
	ManifestURL string `json:"manifest_url"`
	CarrierURL  string `json:"carrier_url"`
	BundleHash  string `json:"bundle_hash,omitempty"`
	KeyID       string `json:"key_id"`
	PublicKey   string `json:"public_key"`
	SignedAt    string `json:"signed_at"`
	Bytes       int    `json:"bytes"`
	// Signal is the exact document that was (or would be) published.
	Signal json.RawMessage `json:"signal"`
}

// registerUpdateSignalRoutes mounts the push endpoint on a node that can both
// sign with the publisher key and reach the fleet's pub/sub, and says exactly
// which half is missing when it cannot. A silent no-mount here is
// indistinguishable from a publisher that simply never signalled — a confusion
// this fleet has already paid for once.
func (h *CoreAPIHandler) registerUpdateSignalRoutes(mux *http.ServeMux) {
	if h.updateSigner == nil {
		log.Warnf("Update signal endpoint NOT registered at %s: this node has no update signing key, so it cannot be a publisher of record.", UpdateSignalRoute)
		return
	}
	if _, ok := h.publisher.(topicPublisher); !ok {
		log.Warnf("Update signal endpoint NOT registered at %s: this node handle cannot publish to a pub/sub topic. Artifacts can be published to the feed but the fleet will not be told.", UpdateSignalRoute)
		return
	}
	if strings.TrimSpace(os.Getenv(updateFeedDirEnv)) == "" {
		log.Warnf("Update signal endpoint NOT registered at %s: %s is unset, so this node serves no feed to announce. Correct for every node that is not the publisher.", UpdateSignalRoute, updateFeedDirEnv)
		return
	}
	mux.HandleFunc(UpdateSignalRoute, h.withRL(h.requireAdminStrict(h.handleUpdateSignal)))
	log.Infof("Update signal endpoint registered at %s (POST, Admin session required, key_id %s, domain %s). A publish is not a push until this is called.",
		UpdateSignalRoute, h.updateSigner.KeyID(), sigdomain.DomainUpdateSignalV1)
}

func (h *CoreAPIHandler) handleUpdateSignal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "update signal accepts POST only")
		return
	}
	if h.updateSigner == nil {
		writeCoreAPIError(w, http.StatusServiceUnavailable, "SIGNER_UNAVAILABLE", "update signing is not available on this node")
		return
	}
	publisher, ok := h.publisher.(topicPublisher)
	if !ok {
		writeCoreAPIError(w, http.StatusServiceUnavailable, "PUBLISHER_UNAVAILABLE", "this node cannot publish to a pub/sub topic")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, updateSignalMaxBody+1))
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "BODY_READ_FAILED", "could not read the request body")
		return
	}
	if len(body) > updateSignalMaxBody {
		writeCoreAPIError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "an update signal selector is a few short fields; this body is not one")
		return
	}
	var req updateSignalRequest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid update signal selector: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Channel) == "" {
		writeCoreAPIError(w, http.StatusBadRequest, "CHANNEL_REQUIRED", "channel is required: a signal names one feed lane")
		return
	}
	if req.Platform == "" {
		req.Platform = runtime.GOOS
	}
	if req.Arch == "" {
		req.Arch = runtime.GOARCH
	}
	if req.Kind == "" {
		req.Kind = "cli-bundle"
	}

	entry, feedBaseURL, err := resolveFeedEntry(req)
	if err != nil {
		writeCoreAPIError(w, http.StatusNotFound, "NO_SUCH_UPDATE", err.Error())
		return
	}

	signal := update.SignalFromFeedEntry(*entry, feedBaseURL, time.Now(), false)
	// The signing identity goes in BEFORE the document is canonicalized, not
	// after. Canonicalization strips only signing.signature, so key_id and
	// public_key are covered by the signature like every other field — filling
	// them in afterwards would silently change the bytes the signature was made
	// over and produce a signal that verifies nowhere.
	signal.Signing.KeyID = h.updateSigner.KeyID()
	signal.Signing.PublicKey = h.updateSigner.PublicKeyB64()
	unsigned, err := signal.Marshal()
	if err != nil {
		writeCoreAPIError(w, http.StatusInternalServerError, "SIGNAL_ENCODE_FAILED", err.Error())
		return
	}

	if req.DryRun {
		writeJSON(w, http.StatusOK, buildSignalResponse(false, true, req, entry, unsigned, h.updateSigner.KeyID(), h.updateSigner.PublicKeyB64(), ""))
		return
	}

	statement, err := update.SignalStatement(unsigned)
	if err != nil {
		writeCoreAPIError(w, http.StatusInternalServerError, "SIGNAL_STATEMENT_FAILED", err.Error())
		return
	}
	result, err := h.updateSigner.SignSignal(updatesign.SignalRequest{
		Signal:    unsigned,
		Statement: statement,
		Requester: updatesign.FingerprintPrincipal(sessionPrincipal(r)),
		RemoteIP:  requestRemoteIP(r),
	})
	if err != nil {
		var refusal *updatesign.Refusal
		if errors.As(err, &refusal) {
			writeCoreAPIError(w, http.StatusBadRequest, refusal.Code, refusal.Message)
			return
		}
		log.Errorf("update signal signing failed: %v", err)
		writeCoreAPIError(w, http.StatusInternalServerError, "SIGNING_FAILED", "the node could not issue a signal signature")
		return
	}

	// Attach the signature and RE-VERIFY the finished document before it leaves
	// this process. Broadcasting a pointer the fleet cannot verify is, from
	// every box's side, indistinguishable from no push at all — and it would be
	// discovered one silent box at a time.
	signal.Signing.Signature = result.SignatureB64
	signed, err := json.Marshal(signal)
	if err != nil {
		writeCoreAPIError(w, http.StatusInternalServerError, "SIGNAL_ENCODE_FAILED", err.Error())
		return
	}
	if err := verifyOwnSignal(signed, h.updateSigner.KeyID(), h.updateSigner.PublicKeyB64()); err != nil {
		log.Errorf("update signal self-verification failed: %v", err)
		writeCoreAPIError(w, http.StatusInternalServerError, "SIGNAL_UNVERIFIABLE",
			"the signal this node just signed does not verify against its own key; nothing was published")
		return
	}

	topic := strings.TrimSpace(req.Topic)
	if topic == "" {
		topic = update.SignalTopic(entry.Channel)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := publisher.PublishToTopic(ctx, topic, signed); err != nil {
		log.Errorf("update signal publish to %s failed: %v", topic, err)
		writeCoreAPIError(w, http.StatusBadGateway, "PUBLISH_FAILED", "the signal was signed but could not be published: "+err.Error())
		return
	}
	log.Infof("Update signal PUSHED on %s: %s version %s sequence %d (%d bytes, key %s). Every subscribed install will now fetch, verify and upgrade itself.",
		topic, entry.UpdateID, entry.Version, entry.Sequence, len(signed), h.updateSigner.KeyID())

	resp := buildSignalResponse(true, false, req, entry, signed, h.updateSigner.KeyID(), h.updateSigner.PublicKeyB64(), topic)
	writeJSON(w, http.StatusOK, resp)
}

func buildSignalResponse(published, dryRun bool, req updateSignalRequest, entry *update.ProviderFeedUpdate, doc []byte, keyID, publicKey, topic string) updateSignalResponse {
	if topic == "" {
		topic = strings.TrimSpace(req.Topic)
		if topic == "" {
			topic = update.SignalTopic(entry.Channel)
		}
	}
	return updateSignalResponse{
		Published:   published,
		DryRun:      dryRun,
		Topic:       topic,
		UpdateID:    entry.UpdateID,
		Version:     entry.Version,
		Sequence:    entry.Sequence,
		Channel:     entry.Channel,
		Target:      fmt.Sprintf("%s/%s/%s", entry.Target.Kind, entry.Target.Platform, entry.Target.Arch),
		ManifestURL: entry.ManifestURL,
		CarrierURL:  entry.CarrierURL,
		BundleHash:  entry.BundleHash,
		KeyID:       keyID,
		PublicKey:   publicKey,
		SignedAt:    time.Now().UTC().Format(time.RFC3339),
		Bytes:       len(doc),
		Signal:      json.RawMessage(doc),
	}
}

// verifyOwnSignal re-parses and re-verifies the finished document exactly as a
// fleet host will: same parser, same domain, same signature check.
func verifyOwnSignal(signed []byte, keyID, publicKeyB64 string) error {
	parsed, err := update.ParseSignal(signed)
	if err != nil {
		return err
	}
	roots := update.TrustedRoots{keyID: publicKeyB64}
	// Only the signature and shape are re-checked here: target/sequence/channel
	// are properties of the RECEIVING box, not of the publisher.
	return parsed.Verify(update.SignalVerifyOptions{TrustedRoots: roots, Now: time.Now()})
}

// resolveFeedEntry reads the node's own feed index and selects the entry to
// announce. Reading the index (rather than accepting one from the caller) is
// what makes a signal and the feed unable to disagree.
func resolveFeedEntry(req updateSignalRequest) (*update.ProviderFeedUpdate, string, error) {
	root := strings.TrimSpace(os.Getenv(updateFeedDirEnv))
	if root == "" {
		return nil, "", errors.New("this node serves no update feed")
	}
	indexPath := filepath.Join(root,
		filepath.Clean(req.Kind),
		filepath.Clean(req.Channel),
		filepath.Clean(req.Platform),
		filepath.Clean(req.Arch),
		"index.json")
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, "", fmt.Errorf("no feed index at %s/%s/%s/%s: %v", req.Kind, req.Channel, req.Platform, req.Arch, err)
	}
	feed, err := update.ParseProviderFeed(raw)
	if err != nil {
		return nil, "", err
	}
	entry, err := feed.Select(update.ProviderFeedSelection{
		UpdateID: strings.TrimSpace(req.UpdateID),
		Version:  strings.TrimSpace(req.Version),
		Channel:  strings.TrimSpace(req.Channel),
		Platform: req.Platform,
		Arch:     req.Arch,
		Kind:     req.Kind,
	})
	if err != nil {
		return nil, "", err
	}
	return entry, feed.FeedBaseURL, nil
}
