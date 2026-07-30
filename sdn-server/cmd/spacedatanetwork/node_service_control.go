package main

// node_service_control.go — POST /api/node/service/{nonce,restart,stop}: the
// DESTRUCTIVE half of the daemon-lifecycle capability the owner authorized on
// 2026-07-30 ("approved", graph task sdn-dashboard-wave3-service-lifecycle).
//
// THE SEAL COUNCIL DISSENT THIS FILE EXISTS TO SATISFY. I proposed gating these
// verbs on the Admin session cookie plus a CSRF header plus a confirm token in the
// body. Hephaestus DISSENTED, and was right: that is "a bearer credential
// darkening a live host" — a stolen or borrowed cookie would be sufficient to stop
// the daemon that serves sdn.spaceaware.io, and on that host nothing else answers
// :443. The Council's requirement, implemented here:
//
//	a FRESH WALLET SIGNATURE over a SINGLE-USE server nonce that BINDS the verb
//	and the resolved unit, with a TTL <= 120s, from the SAME Admin identity the
//	session belongs to.
//
// So there are two round trips. First POST /api/node/service/nonce with the verb;
// the node mints a nonce bound to (verb, unit, session identity, expiry) and
// returns the exact bytes to sign. Then POST /api/node/service/{restart,stop} with
// the signature over those bytes. The nonce is consumed on use, whether or not the
// signature verified — a nonce that survives a failed attempt is a nonce an
// attacker may retry against.
//
// WHAT THIS MEANS FOR THE OPERATOR, and it is deliberate: a session RESTORED FROM
// THE COOKIE cannot restart or stop the node. Signing needs the key, the key lives
// only in the page that unlocked it, and §4/§6b already says a restored session
// "can administer, but must re-enter its recovery phrase to sign an attestation".
// Darkening a production host is at least as serious as an attestation.
//
// FAIL-CLOSED, five independent conditions, ALL required:
//
//  1. the Admin auth wall (isAdminOnlyAPIPath covers the whole /api/node/service
//     prefix, so these paths were classified before they existed);
//  2. the unit-level opt-in SDN_SERVICE_CONTROL=1 — absent on every host this
//     repository ships anything for, asserted by test;
//  3. a PROVEN supervisor: the unit resolved from /proc/self/cgroup and confirmed
//     ours because systemd's MainPID for it is our pid;
//  4. a single-use, unexpired nonce bound to this verb, this unit and this
//     identity;
//  5. a valid Ed25519 signature over that nonce from the session's OWN signing
//     key (auth.Handler.SigningKeyAuthorizesSession — the same three key->identity
//     forms the admit point accepts, and no fourth).
//
// Plus a RATE LIMIT of >= 60s between accepted actions, which is not politeness:
// systemd's StartLimitBurst=5 puts a unit into `failed` if it is restarted too
// often, and a failed unit on host-01 means the box is dark until someone runs
// `systemctl reset-failed` over ssh.
//
// EVERY REFUSAL IS LOGGED AT WARN with a machine-readable reason and the concrete
// operands, per graph/tasks/hard-host-refusal-contract.md. For an HTTP connector
// the operator surface required by that contract's item 2 is the response itself:
// the status code and the `reason` field ARE the visible refusal, rather than a
// counter an operator has to go looking for.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/hostsvc"
)

const (
	// serviceNonceTTL is the Council's ceiling, not a chosen convenience: "TTL
	// <= 120s". A minted nonce is a licence to darken the host; it expires while
	// the operator is still looking at the dialog that asked for it.
	serviceNonceTTL = 120 * time.Second
	// serviceActionMinInterval is the >= 60s rate limit (Hephaestus). Measured
	// from the last ACCEPTED action, because a refused attempt did not touch the
	// unit and must not lock the operator out of a legitimate retry.
	serviceActionMinInterval = 60 * time.Second
	// serviceNonceMax bounds the mint table. A caller that mints without ever
	// spending cannot grow memory; the oldest entries are swept first.
	serviceNonceMax = 64
)

// serviceNonce is one minted licence. It binds everything that must not vary
// between the mint and the spend.
type serviceNonce struct {
	action    hostsvc.Action
	unit      string
	xpub      string
	challenge []byte
	expiresAt time.Time
}

// serviceControlState holds the mint table and the last-accepted-action clock.
// One instance per process, created where the routes are mounted.
type serviceControlState struct {
	mu sync.Mutex
	// nonces is keyed by the nonce id the client echoes back.
	nonces map[string]serviceNonce
	// lastAction is when an action was last ACCEPTED (not attempted).
	lastAction time.Time
	// now is injectable so the tests can drive expiry and the rate limit
	// without sleeping.
	now func() time.Time
}

func newServiceControlState() *serviceControlState {
	return &serviceControlState{nonces: make(map[string]serviceNonce), now: time.Now}
}

func (s *serviceControlState) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// mint creates a nonce bound to (action, unit, xpub) and returns its id and the
// bytes the client must sign. Sweeps expired entries first, so the table cannot
// fill with dead licences.
func (s *serviceControlState) mint(action hostsvc.Action, unit, xpub string) (string, []byte, time.Time, error) {
	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		return "", nil, time.Time{}, err
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", nil, time.Time{}, err
	}
	id := hex.EncodeToString(idBytes)

	now := s.clock()
	expiry := now.Add(serviceNonceTTL)

	s.mu.Lock()
	defer s.mu.Unlock()
	for key, pending := range s.nonces {
		if !pending.expiresAt.After(now) {
			delete(s.nonces, key)
		}
	}
	if len(s.nonces) >= serviceNonceMax {
		return "", nil, time.Time{}, errTooManyNonces
	}
	s.nonces[id] = serviceNonce{action: action, unit: unit, xpub: xpub, challenge: challenge, expiresAt: expiry}
	return id, challenge, expiry, nil
}

// consume removes a nonce and returns it. Removal happens whether or not the
// caller goes on to accept it: a nonce that survives a failed verification is a
// nonce an attacker may retry.
func (s *serviceControlState) consume(id string) (serviceNonce, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.nonces[id]
	if ok {
		delete(s.nonces, id)
	}
	return pending, ok
}

// rateLimited reports whether an action is too soon after the last ACCEPTED one,
// and how long is left.
func (s *serviceControlState) rateLimited() (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastAction.IsZero() {
		return false, 0
	}
	elapsed := s.clock().Sub(s.lastAction)
	if elapsed >= serviceActionMinInterval {
		return false, 0
	}
	return true, serviceActionMinInterval - elapsed
}

func (s *serviceControlState) recordAction() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAction = s.clock()
}

type errTooManyNoncesType struct{}

func (errTooManyNoncesType) Error() string { return "too many pending service nonces" }

var errTooManyNonces = errTooManyNoncesType{}

// serviceRefusal is the wire shape of every "no". `reason` is a STABLE machine
// code (the refusal contract's vocabulary), `message` is for the operator, and the
// remaining fields carry the OPERANDS of the decision — the contract is explicit
// that "refused without the numbers is not a compliant log", and the same applies
// to the answer.
type serviceRefusal struct {
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	Unit       string `json:"unit,omitempty"`
	Action     string `json:"action,omitempty"`
	RetryAfter int    `json:"retry_after_seconds,omitempty"`
}

// refuseService writes the refusal AND logs it at WARN with the operands, in the
// same code path — the requirement of hard-host-refusal-contract.md, applied to an
// HTTP connector.
func refuseService(w http.ResponseWriter, status int, refusal serviceRefusal) {
	log.Warnf(
		"service control REFUSED reason=%s action=%s unit=%q status=%d retry_after=%ds: %s",
		refusal.Reason, refusal.Action, refusal.Unit, status, refusal.RetryAfter, refusal.Message,
	)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(refusal)
}

// serviceActionFromPath maps the route to a verb. Path-derived, not body-derived:
// the verb a client asked for and the verb the nonce was minted for must be
// comparable without trusting a field.
func serviceActionFromPath(path string) (hostsvc.Action, bool) {
	switch {
	case strings.HasSuffix(path, "/restart"):
		return hostsvc.ActionRestart, true
	case strings.HasSuffix(path, "/stop"):
		return hostsvc.ActionStop, true
	default:
		return "", false
	}
}

type serviceNonceRequest struct {
	Action string `json:"action"`
}

type serviceNonceResponse struct {
	NonceID string `json:"nonce_id"`
	// Challenge is hex, and it is the EXACT bytes to sign — the client signs
	// what the server said, never a string the client assembled.
	Challenge string `json:"challenge_hex"`
	Action    string `json:"action"`
	Unit      string `json:"unit"`
	ExpiresAt int64  `json:"expires_at"`
	// RestartPolicy is included because the confirm dialog must be able to tell
	// the operator what a STOP will actually mean on THIS host.
	RestartPolicy string `json:"restart_policy,omitempty"`
}

type serviceActionRequest struct {
	NonceID         string `json:"nonce_id"`
	SignatureHex    string `json:"signature_hex"`
	ClientPubKeyHex string `json:"client_pubkey_hex"`
}

// handleServiceNonce mints the licence.
//
//	POST /api/node/service/nonce {"action":"restart"|"stop"}
//
// It performs every check the ACTION performs except the signature — so a client
// that cannot act finds out here, before it asks the operator to unlock a key.
func handleServiceNonce(state *serviceControlState, authHandler *auth.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req serviceNonceRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 4*1024)).Decode(&req); err != nil {
			refuseService(w, http.StatusBadRequest, serviceRefusal{Reason: "invalid_request", Message: "invalid JSON body"})
			return
		}
		action := hostsvc.Action(strings.TrimSpace(req.Action))
		if action != hostsvc.ActionRestart && action != hostsvc.ActionStop {
			refuseService(w, http.StatusBadRequest, serviceRefusal{
				Reason: "unknown_action", Action: string(action),
				Message: "action must be \"restart\" or \"stop\"",
			})
			return
		}

		session := auth.SessionFromContext(r.Context())
		if session == nil {
			// Unreachable behind the auth wall; a refusal rather than a panic if
			// the route is ever mounted somewhere looser.
			refuseService(w, http.StatusUnauthorized, serviceRefusal{Reason: "no_session", Action: string(action), Message: "no session on the request"})
			return
		}

		if !serviceControlEnabled() {
			refuseService(w, http.StatusNotImplemented, serviceRefusal{
				Reason: "control_not_enabled", Action: string(action),
				Message: "this host has not been granted control of its own unit (" + serviceControlEnvVar + " is not set)",
			})
			return
		}
		probe := hostsvc.Probe(r.Context())
		if !probe.Detected {
			refuseService(w, http.StatusConflict, serviceRefusal{
				Reason: "no_supervisor", Action: string(action),
				Message: "no supervisor unit could be proven for this process",
			})
			return
		}

		id, challenge, expiry, err := state.mint(action, probe.Unit, session.XPub)
		if err != nil {
			refuseService(w, http.StatusTooManyRequests, serviceRefusal{
				Reason: "nonce_table_full", Action: string(action), Unit: probe.Unit,
				Message: err.Error(),
			})
			return
		}

		log.Infof("service control nonce minted action=%s unit=%q ttl=%s", action, probe.Unit, serviceNonceTTL)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(serviceNonceResponse{
			NonceID:       id,
			Challenge:     hex.EncodeToString(challenge),
			Action:        string(action),
			Unit:          probe.Unit,
			ExpiresAt:     expiry.Unix(),
			RestartPolicy: probe.RestartPolicy,
		})
	}
}

// handleServiceAction spends the licence and acts.
//
//	POST /api/node/service/restart
//	POST /api/node/service/stop
//
// On success it answers 202 BEFORE the supervisor is asked to act, because the
// answer travels over the connection the action is about to break: the dashboard
// is served BY this daemon. `systemctl --no-block` then enqueues the job and
// returns; systemd kills this process a moment later, by which time the response
// has been flushed and the page is already showing its reconnect state.
func handleServiceAction(state *serviceControlState, authHandler *auth.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		action, ok := serviceActionFromPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}

		// CSRF: this header cannot be set cross-origin without CORS, and it is
		// the pattern the rest of the admin surface already uses. It is a belt,
		// not the trousers — the signature below is the authority.
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			refuseService(w, http.StatusForbidden, serviceRefusal{
				Reason: "missing_xhr_header", Action: string(action),
				Message: "X-Requested-With: XMLHttpRequest is required",
			})
			return
		}

		session := auth.SessionFromContext(r.Context())
		if session == nil {
			refuseService(w, http.StatusUnauthorized, serviceRefusal{Reason: "no_session", Action: string(action), Message: "no session on the request"})
			return
		}
		if !serviceControlEnabled() {
			// The operand is named, per the refusal contract: an operator reading
			// this must learn WHICH grant is missing, not merely that one is.
			refuseService(w, http.StatusNotImplemented, serviceRefusal{
				Reason: "control_not_enabled", Action: string(action),
				Message: "this host has not been granted control of its own unit (" + serviceControlEnvVar + " is not set on the unit)",
			})
			return
		}
		probe := hostsvc.Probe(r.Context())
		if !probe.Detected {
			refuseService(w, http.StatusConflict, serviceRefusal{
				Reason: "no_supervisor", Action: string(action),
				Message: "no supervisor unit could be proven for this process (no systemd unit whose MainPID is this process)",
			})
			return
		}
		if limited, left := state.rateLimited(); limited {
			refuseService(w, http.StatusTooManyRequests, serviceRefusal{
				Reason: "rate_limited", Action: string(action), Unit: probe.Unit,
				RetryAfter: int(left.Seconds()) + 1,
				Message: "a lifecycle action was accepted less than " +
					serviceActionMinInterval.String() +
					" ago; systemd's start-limit would put this unit into a failed state",
			})
			return
		}

		var req serviceActionRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&req); err != nil {
			refuseService(w, http.StatusBadRequest, serviceRefusal{Reason: "invalid_request", Action: string(action), Message: "invalid JSON body"})
			return
		}
		req.NonceID = strings.TrimSpace(req.NonceID)
		req.SignatureHex = strings.TrimPrefix(strings.TrimSpace(req.SignatureHex), "0x")
		req.ClientPubKeyHex = strings.TrimPrefix(strings.TrimSpace(req.ClientPubKeyHex), "0x")
		if req.NonceID == "" || req.SignatureHex == "" || req.ClientPubKeyHex == "" {
			refuseService(w, http.StatusBadRequest, serviceRefusal{
				Reason: "invalid_request", Action: string(action),
				Message: "nonce_id, signature_hex and client_pubkey_hex are all required",
			})
			return
		}

		signature, err := hex.DecodeString(req.SignatureHex)
		if err != nil || len(signature) != ed25519.SignatureSize {
			refuseService(w, http.StatusBadRequest, serviceRefusal{Reason: "invalid_signature", Action: string(action), Message: "signature_hex must be a 64-byte Ed25519 signature"})
			return
		}
		pubKey, err := hex.DecodeString(req.ClientPubKeyHex)
		if err != nil || len(pubKey) != ed25519.PublicKeySize {
			refuseService(w, http.StatusBadRequest, serviceRefusal{Reason: "invalid_public_key", Action: string(action), Message: "client_pubkey_hex must be a 32-byte Ed25519 public key"})
			return
		}

		// SINGLE USE: consumed here, before verification, so a failed attempt
		// burns the licence.
		pending, found := state.consume(req.NonceID)
		if !found {
			refuseService(w, http.StatusForbidden, serviceRefusal{Reason: "unknown_nonce", Action: string(action), Unit: probe.Unit, Message: "no such nonce, or it has already been used"})
			return
		}
		if !pending.expiresAt.After(state.clock()) {
			refuseService(w, http.StatusForbidden, serviceRefusal{Reason: "nonce_expired", Action: string(action), Unit: probe.Unit, Message: "the nonce expired; ask for a new one"})
			return
		}
		// The nonce BINDS the verb, the unit and the identity. All three are
		// re-checked, so a nonce minted for a restart cannot spend a stop, a
		// nonce minted before a unit change cannot act on the new one, and one
		// operator's nonce cannot be spent inside another's session.
		if pending.action != action {
			refuseService(w, http.StatusForbidden, serviceRefusal{Reason: "nonce_action_mismatch", Action: string(action), Unit: probe.Unit, Message: "this nonce was minted for a different action"})
			return
		}
		if pending.unit != probe.Unit {
			refuseService(w, http.StatusConflict, serviceRefusal{Reason: "nonce_unit_mismatch", Action: string(action), Unit: probe.Unit, Message: "the resolved unit changed since this nonce was minted"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(pending.xpub), []byte(session.XPub)) != 1 {
			refuseService(w, http.StatusForbidden, serviceRefusal{Reason: "nonce_identity_mismatch", Action: string(action), Unit: probe.Unit, Message: "this nonce belongs to a different identity"})
			return
		}

		// THE SIGNATURE, and whose key it must be. Both halves are required: the
		// bytes must verify, AND the key must be the one this session's identity
		// signs with (the same three forms the admit point accepts).
		if !ed25519.Verify(ed25519.PublicKey(pubKey), pending.challenge, signature) {
			refuseService(w, http.StatusForbidden, serviceRefusal{Reason: "bad_signature", Action: string(action), Unit: probe.Unit, Message: "the signature does not verify over the nonce"})
			return
		}
		if !authHandler.SigningKeyAuthorizesSession(pubKey, session) {
			refuseService(w, http.StatusForbidden, serviceRefusal{
				Reason: "key_not_this_identity", Action: string(action), Unit: probe.Unit,
				Message: "the signing key is not the key this session's identity signs with",
			})
			return
		}

		// ACCEPTED. Record it for the rate limit, answer, flush, and only then
		// act — the response rides the connection the action is about to end.
		state.recordAction()
		log.Warnf(
			"service control ACCEPTED action=%s unit=%q policy=%q identity=%s — the daemon is about to %s",
			action, probe.Unit, probe.RestartPolicy, shortXPubForLog(session.XPub), action,
		)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"accepted":       true,
			"action":         string(action),
			"unit":           probe.Unit,
			"restart_policy": probe.RestartPolicy,
			// What the CLIENT should expect, said by the host rather than
			// assumed by the page: a restart drops this connection and comes
			// back; a stop drops it and stays down.
			"expect": map[string]interface{}{
				"connection_drops": true,
				"comes_back":       action == hostsvc.ActionRestart,
			},
		})
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		if err := hostsvc.Control(r.Context(), probe, action); err != nil {
			// The client already has its 202 — the only place left to say this is
			// the journal, and it must be said loudly: the operator was told the
			// action was accepted.
			log.Errorf("service control action=%s unit=%q FAILED AFTER ACCEPT: %v", action, probe.Unit, err)
		}
	}
}

// shortXPubForLog keeps the journal readable and avoids writing a full extended
// public key into it on every action. It is an identifier for a human reading
// logs, not a credential.
func shortXPubForLog(xpub string) string {
	trimmed := strings.TrimSpace(xpub)
	if len(trimmed) <= 12 {
		return trimmed
	}
	return trimmed[:8] + "…" + trimmed[len(trimmed)-4:]
}
