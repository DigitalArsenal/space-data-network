package api

// DISTRIBUTIONS — releases waiting for approval.
//
// Owner 2026-10-09: "I want the new way to be able to use the node key by
// default, or upload a new one", approved in the page.
//
// publish-fleet-update.mjs and publish-ui-update.mjs submit an unsigned release
// here, and nothing goes out until an administrator approves it in the updater
// module's page: by default this node signs with its own key (approve), or the
// page signs with an uploaded key it never saves. Either signature is accepted
// only when it verifies against this node's update roots and covers exactly
// the release that was submitted: the signer may fill in signing.key_id,
// signing.public_key and signing.signature, nothing else. The release's signal
// is built later from the feed, after the files are served, and is signed the
// same way before the node publishes it.
//
//	POST /api/v1/admin/updates/distributions                submit an unsigned manifest
//	GET  /api/v1/admin/updates/distributions                list, with this node's key and roots
//	GET  /api/v1/admin/updates/distributions/{id}           one, with its documents
//	POST /api/v1/admin/updates/distributions/{id}/approve   {"document": "manifest"|"signal"}: this node signs
//	POST /api/v1/admin/updates/distributions/{id}/manifest  a manifest signed with an uploaded key
//	POST /api/v1/admin/updates/distributions/{id}/signal    a signal signed with an uploaded key; publishes it
//
// The signal half starts at POST /api/v1/admin/updates/signal with
// "distribution": <id> (update_signal.go).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spacedatanetwork/sdn-server/internal/updatesign"
)

// UpdateDistributionsRoute is the collection path.
const UpdateDistributionsRoute = "/api/v1/admin/updates/distributions"

const (
	distributionMaxBody = 256 << 10
	distributionMax     = 32
	distributionTTL     = 24 * time.Hour
)

// Distribution states, in order.
const (
	DistributionAwaitingManifest = "awaiting-manifest-signature"
	DistributionManifestSigned   = "manifest-signed"
	DistributionAwaitingSignal   = "awaiting-signal-signature"
	DistributionSignalled        = "signalled"
)

type distribution struct {
	ID             string          `json:"id"`
	State          string          `json:"state"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
	UpdateID       string          `json:"update_id"`
	Version        string          `json:"version"`
	Target         string          `json:"target"`
	Recipients     int             `json:"recipients"`
	KeyID          string          `json:"key_id,omitempty"`
	Topic          string          `json:"topic,omitempty"`
	Manifest       json.RawMessage `json:"manifest"`
	SignedManifest json.RawMessage `json:"signed_manifest,omitempty"`
	Signal         json.RawMessage `json:"signal,omitempty"`
	SignedSignal   json.RawMessage `json:"signed_signal,omitempty"`

	created time.Time
}

type distributionStore struct {
	mu    sync.Mutex
	items map[string]*distribution
}

func newDistributionStore() *distributionStore {
	return &distributionStore{items: map[string]*distribution{}}
}

func (s *distributionStore) prune(now time.Time) {
	for id, d := range s.items {
		if now.Sub(d.created) > distributionTTL {
			delete(s.items, id)
		}
	}
}

func (s *distributionStore) add(d *distribution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(d.created)
	if len(s.items) >= distributionMax {
		return errors.New("too many releases are waiting for approval")
	}
	s.items[d.ID] = d
	return nil
}

// with runs fn on the distribution under the lock.
func (s *distributionStore) with(id string, fn func(*distribution) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.items[id]
	if !ok {
		return errNoDistribution
	}
	return fn(d)
}

func (s *distributionStore) list() []distribution {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now())
	out := make([]distribution, 0, len(s.items))
	for _, d := range s.items {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].created.After(out[j].created) })
	return out
}

var errNoDistribution = errors.New("no such distribution")

// registerUpdateDistributionRoutes mounts the routes on the node that serves the
// feed and can publish to the fleet's pub/sub: the coordinator.
func (h *CoreAPIHandler) registerUpdateDistributionRoutes(mux *http.ServeMux) {
	if _, ok := h.publisher.(topicPublisher); !ok || strings.TrimSpace(os.Getenv(updateFeedDirEnv)) == "" {
		return
	}
	h.distributions = newDistributionStore()
	mux.HandleFunc(UpdateDistributionsRoute, h.withRL(h.requireAdminStrict(h.handleDistributions)))
	mux.HandleFunc(UpdateDistributionsRoute+"/{id}", h.withRL(h.requireAdminStrict(h.handleDistribution)))
	mux.HandleFunc(UpdateDistributionsRoute+"/{id}/{document}", h.withRL(h.requireAdminStrict(h.handleDistributionSignature)))
	log.Infof("Update distribution routes registered at %s: releases wait here for approval in the Updater page.", UpdateDistributionsRoute)
}

func (h *CoreAPIHandler) handleDistributions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		roots := []string{}
		if loaded, err := update.LoadTrustRoots(update.PathsFor(bundle.ResolveCurrent().Root)); err == nil {
			for id := range loaded {
				roots = append(roots, id)
			}
			sort.Strings(roots)
		}
		writeJSON(w, http.StatusOK, map[string]any{"distributions": h.distributions.list(), "node_key_id": signerKeyID(h.updateSigner), "update_roots": roots})
	case http.MethodPost:
		body, ok := readDistributionBody(w, r)
		if !ok {
			return
		}
		d, err := newDistribution(body, time.Now())
		if err != nil {
			writeCoreAPIError(w, http.StatusBadRequest, "INVALID_RELEASE", err.Error())
			return
		}
		if err := h.distributions.add(d); err != nil {
			writeCoreAPIError(w, http.StatusTooManyRequests, "DISTRIBUTIONS_FULL", err.Error())
			return
		}
		log.Infof("Release %s (%s, %s) is waiting for approval in the Updater page as distribution %s.", d.UpdateID, d.Version, d.Target, d.ID)
		writeJSON(w, http.StatusCreated, d)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "distributions accept GET and POST")
	}
}

func (h *CoreAPIHandler) handleDistribution(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "a distribution accepts GET")
		return
	}
	var out distribution
	if err := h.distributions.with(r.PathValue("id"), func(d *distribution) error { out = *d; return nil }); err != nil {
		writeCoreAPIError(w, http.StatusNotFound, "NO_SUCH_DISTRIBUTION", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *CoreAPIHandler) handleDistributionSignature(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "a signature is submitted with POST")
		return
	}
	body, ok := readDistributionBody(w, r)
	if !ok {
		return
	}
	roots, err := update.LoadTrustRoots(update.PathsFor(bundle.ResolveCurrent().Root))
	if err != nil {
		writeCoreAPIError(w, http.StatusServiceUnavailable, "NO_UPDATE_ROOTS", "this node cannot read its update roots: "+err.Error())
		return
	}
	id, document := r.PathValue("id"), r.PathValue("document")
	if document == "approve" {
		if document, body, err = h.approveDistribution(r, id, body); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errNoDistribution) {
				status = http.StatusNotFound
			}
			writeCoreAPIError(w, status, "APPROVAL_REFUSED", err.Error())
			return
		}
	}
	var publish func(context.Context) error
	err = h.distributions.with(id, func(d *distribution) error {
		switch document {
		case "manifest":
			if d.State != DistributionAwaitingManifest {
				return errors.New("this release's manifest is not waiting for a signature")
			}
			keyID, err := verifyDistributionManifest(d.Manifest, body, roots)
			if err != nil {
				return err
			}
			d.SignedManifest, d.KeyID, d.State = json.RawMessage(body), keyID, DistributionManifestSigned
		case "signal":
			if d.State != DistributionAwaitingSignal {
				return errors.New("this release's signal is not waiting for a signature")
			}
			if err := verifyDistributionSignal(d.Signal, body, roots); err != nil {
				return err
			}
			topic, signed := d.Topic, append([]byte(nil), body...)
			publish = func(ctx context.Context) error {
				return h.publisher.(topicPublisher).PublishToTopic(ctx, topic, signed)
			}
			d.SignedSignal = json.RawMessage(body)
		default:
			return errNoDistribution
		}
		d.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return nil
	})
	if errors.Is(err, errNoDistribution) {
		writeCoreAPIError(w, http.StatusNotFound, "NO_SUCH_DISTRIBUTION", err.Error())
		return
	}
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "SIGNATURE_REFUSED", err.Error())
		return
	}
	if publish != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := publish(ctx); err != nil {
			writeCoreAPIError(w, http.StatusBadGateway, "PUBLISH_FAILED", "the signal verified but could not be published: "+err.Error())
			return
		}
		_ = h.distributions.with(id, func(d *distribution) error { d.State = DistributionSignalled; return nil })
		log.Infof("Distribution %s signalled: every subscribed install will now fetch, verify and upgrade itself.", id)
	}
	h.handleDistribution(w, withMethod(r, http.MethodGet))
}

// attachDistributionSignal stores the signal the node built from its feed for a
// distribution whose manifest is signed and served.
func (h *CoreAPIHandler) attachDistributionSignal(id string, unsigned []byte, topic string) (distribution, error) {
	var out distribution
	if h.distributions == nil {
		return out, errNoDistribution
	}
	err := h.distributions.with(id, func(d *distribution) error {
		if d.State != DistributionManifestSigned {
			return errors.New("the release's manifest must be signed before its signal")
		}
		d.Signal, d.Topic, d.State = json.RawMessage(unsigned), topic, DistributionAwaitingSignal
		d.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		out = *d
		return nil
	})
	return out, err
}

// approveDistribution signs the waiting document with this node's own key. It
// returns which document it signed and the signed bytes, which then pass the
// same checks as a document signed with an uploaded key.
func (h *CoreAPIHandler) approveDistribution(r *http.Request, id string, body []byte) (string, []byte, error) {
	if h.updateSigner == nil {
		return "", nil, errors.New("this node has no signing key: upload a key instead")
	}
	var req struct {
		Document string `json:"document"`
	}
	if err := json.Unmarshal(body, &req); err != nil || (req.Document != "manifest" && req.Document != "signal") {
		return "", nil, errors.New(`approve names one document: {"document": "manifest"} or {"document": "signal"}`)
	}
	var unsigned []byte
	err := h.distributions.with(id, func(d *distribution) error {
		if req.Document == "manifest" && d.State == DistributionAwaitingManifest {
			unsigned = append([]byte(nil), d.Manifest...)
		} else if req.Document == "signal" && d.State == DistributionAwaitingSignal {
			unsigned = append([]byte(nil), d.Signal...)
		} else {
			return errors.New("this release's " + req.Document + " is not waiting for a signature")
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	// The signer names itself before the document is canonicalized: key_id and
	// public_key are covered by the signature.
	decoder := json.NewDecoder(bytes.NewReader(unsigned))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		return "", nil, err
	}
	signing, _ := doc["signing"].(map[string]any)
	if signing == nil {
		return "", nil, errors.New("the release has no signing block")
	}
	signing["key_id"], signing["public_key"] = h.updateSigner.KeyID(), h.updateSigner.PublicKeyB64()
	named, err := json.Marshal(doc)
	if err != nil {
		return "", nil, err
	}
	requester, remote := updatesign.FingerprintPrincipal(sessionPrincipal(r)), requestRemoteIP(r)
	var signature string
	if req.Document == "manifest" {
		result, err := h.updateSigner.Sign(updatesign.Request{Manifest: named, Requester: requester, RemoteIP: remote})
		if err != nil {
			return "", nil, err
		}
		signature = result.SignatureB64
	} else {
		statement, err := update.SignalStatement(named)
		if err != nil {
			return "", nil, err
		}
		result, err := h.updateSigner.SignSignal(updatesign.SignalRequest{Signal: named, Statement: statement, Requester: requester, RemoteIP: remote})
		if err != nil {
			return "", nil, err
		}
		signature = result.SignatureB64
	}
	signing["signature"] = signature
	signed, err := json.Marshal(doc)
	return req.Document, signed, err
}

func newDistribution(manifest []byte, now time.Time) (*distribution, error) {
	m, err := update.ParseManifest(manifest)
	if err != nil {
		return nil, err
	}
	if m.Schema != update.ManifestSchema || m.UpdateID == "" || m.Version == "" || m.Sequence == nil {
		return nil, errors.New("not an update manifest")
	}
	if m.Signing.Signature != "" {
		return nil, errors.New("submit the release unsigned: it is signed when approved")
	}
	if m.Signing.StatementDomain != sigdomain.DomainUpdateManifestV1 {
		return nil, errors.New("the manifest must be signed under " + sigdomain.DomainUpdateManifestV1)
	}
	var idBytes [8]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, err
	}
	recipients := 0
	if m.Envelope != nil {
		recipients = len(m.Envelope.Recipients)
	}
	stamp := now.UTC().Format(time.RFC3339)
	return &distribution{
		ID: hex.EncodeToString(idBytes[:]), State: DistributionAwaitingManifest, CreatedAt: stamp, UpdatedAt: stamp,
		UpdateID: m.UpdateID, Version: m.Version, Recipients: recipients,
		Target:   m.Target.Kind + "/" + m.Target.Platform + "/" + m.Target.Arch,
		Manifest: json.RawMessage(append([]byte(nil), manifest...)), created: now,
	}, nil
}

// verifyDistributionManifest accepts a signed manifest that verifies against
// roots and differs from the submitted one only in the signer's fields.
func verifyDistributionManifest(unsigned, signed []byte, roots update.TrustedRoots) (string, error) {
	m, err := update.ParseManifest(signed)
	if err != nil {
		return "", err
	}
	if err := m.VerifySignature(roots); err != nil {
		return "", errors.New("the signature does not verify against this node's update roots: " + err.Error())
	}
	if err := sameExceptSigner(unsigned, signed); err != nil {
		return "", err
	}
	return m.Signing.KeyID, nil
}

func verifyDistributionSignal(unsigned, signed []byte, roots update.TrustedRoots) error {
	s, err := update.ParseSignal(signed)
	if err != nil {
		return err
	}
	if err := s.Verify(update.SignalVerifyOptions{TrustedRoots: roots, Now: time.Now()}); err != nil {
		return errors.New("the signal does not verify against this node's update roots: " + err.Error())
	}
	return sameExceptSigner(unsigned, signed)
}

// sameExceptSigner compares two documents with signing.key_id,
// signing.public_key and signing.signature removed.
func sameExceptSigner(want, got []byte) error {
	strip := func(raw []byte) ([]byte, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var doc map[string]any
		if err := decoder.Decode(&doc); err != nil {
			return nil, err
		}
		if signing, ok := doc["signing"].(map[string]any); ok {
			delete(signing, "key_id")
			delete(signing, "public_key")
		}
		normalized, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		return update.CanonicalManifestBytes(normalized)
	}
	a, err := strip(want)
	if err != nil {
		return err
	}
	b, err := strip(got)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return errors.New("the signed document is not the release that was submitted")
	}
	return nil
}

func readDistributionBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, distributionMaxBody+1))
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "BODY_READ_FAILED", "could not read the request body")
		return nil, false
	}
	if len(body) > distributionMaxBody {
		writeCoreAPIError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "a release document is at most 256 KiB")
		return nil, false
	}
	return body, true
}

func withMethod(r *http.Request, method string) *http.Request {
	clone := r.Clone(r.Context())
	clone.Method = method
	return clone
}
