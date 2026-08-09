package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// SIGNED-REQUEST ADMISSION — the transport for an operator write from a page
// that is not on this node's origin.
//
// THE PROBLEM
// -----------
// The session cookie is HttpOnly + SameSite=Lax by contract
// (nst-node-admin-contract.md §5, setSessionCookie). That is correct and stays
// correct — and it means a page on a DIFFERENT SITE can never carry it. Two
// independent walls refuse it: the browser will not attach a Lax cookie to a
// cross-site fetch, and adminSecurityMiddleware's CSRF gate refuses any
// cross-origin state-changing request that carries a session cookie. So the
// cellular credential form on the gallery origin has no way to be anybody.
//
// WHAT WAS REJECTED, AND WHY IT MATTERS
// -------------------------------------
// The obvious fix — hand the session token to JavaScript as a bearer — was
// RULED OUT (HERMES 2026-08-09 §2). The contract says of the session token "you
// cannot read it: it is HttpOnly", and the gallery lives on a SHARED
// user-pages origin where every other page the owner publishes could read a
// parked operator token. A 24-hour credential in that namespace is a worse
// defect than the one being fixed.
//
// THE RULED TRANSPORT: SIGN THE REQUEST, MINT NOTHING
// ---------------------------------------------------
// The client takes an ordinary single-use challenge from the already-anonymous
// POST /api/auth/challenge, signs it TOGETHER WITH a digest of the exact
// request it is about to send, and puts that signature in the Authorization
// header. The node verifies it against the pending-challenge store, mints an
// EPHEMERAL in-memory session for that one request, and never persists or
// returns anything.
//
// The properties that follow are the point:
//
//   - Nothing stealable ever exists on the gallery origin. The signature is
//     spent the moment it is used and authorises exactly one method+path+body.
//   - It cannot be CSRF'd: a browser never attaches this header on its own, and
//     a forged request would need a signature over ITS OWN body.
//   - It cannot be replayed: the challenge is deleted on first use, whether the
//     signature verified or not.
//   - It cannot enrol anybody. The signer must already be an operator with a
//     BOUND signing key; first-admin-bootstrap challenges are refused outright,
//     because that is the one path that WRITES a user.
//   - No new session row, no cookie, no token in any response body.
//
// WHY THE Authorization HEADER AND NOT AN X-SDN-* ONE
// ---------------------------------------------------
// Not preference — reachability. The preflight's Access-Control-Allow-Headers
// list is emitted by cmd/spacedatanetwork/main.go and already names
// Authorization; an X-SDN-Auth-* header would be rejected by the browser at
// preflight and would require editing that file. The scheme token keeps it
// unambiguous: this is NOT a bearer credential and must never be treated as
// one.
//
//	Authorization: SDN-Signed challenge="<challenge id>", signature="<hex>"

// SignedRequestScheme is the Authorization scheme token for signed-request
// admission. It is deliberately not "Bearer": nothing here is a bearer
// credential.
const SignedRequestScheme = "SDN-Signed"

// SignedRequestCanonicalVersion prefixes the canonical request string. Every
// signature is computed over it, so changing this string invalidates every
// signature in flight — which is why it carries an explicit version and why a
// change requires a client change in the same wave.
const SignedRequestCanonicalVersion = "SDN-SIGNED-REQUEST/v1"

// maxSignedRequestBody caps the body this admission mode will digest.
//
// The digest requires buffering the body before the handler sees it, so an
// unbounded read here would be a memory amplifier reachable before any auth
// check. Credential envelopes are a few hundred bytes; a megabyte is three
// orders of magnitude of headroom. A larger body is REFUSED (the request is
// simply not admitted) rather than admitted unsigned.
const maxSignedRequestBody = 1 << 20

var errNoSignedRequest = errors.New("no signed-request authorization")

// signedRequestCredentials are the two values carried in the Authorization
// header. Neither is reusable: the challenge is single-use and the signature
// covers this exact request.
type signedRequestCredentials struct {
	challengeID string
	signature   []byte
}

// parseSignedRequestAuthorization pulls the credentials out of an Authorization
// header, or reports errNoSignedRequest when the header is absent or belongs to
// some other scheme (so an unrelated Authorization value never becomes an auth
// failure — it simply is not this mode).
func parseSignedRequestAuthorization(raw string) (signedRequestCredentials, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return signedRequestCredentials{}, errNoSignedRequest
	}
	scheme, params, found := strings.Cut(value, " ")
	if !found || !strings.EqualFold(strings.TrimSpace(scheme), SignedRequestScheme) {
		return signedRequestCredentials{}, errNoSignedRequest
	}

	creds := signedRequestCredentials{}
	for _, part := range strings.Split(params, ",") {
		key, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "challenge":
			creds.challengeID = val
		case "signature":
			creds.signature, _ = hex.DecodeString(strings.TrimPrefix(val, "0x"))
		}
	}
	if creds.challengeID == "" || len(creds.signature) != ed25519.SignatureSize {
		return signedRequestCredentials{}, fmt.Errorf("malformed %s authorization", SignedRequestScheme)
	}
	return creds, nil
}

// canonicalRequestString is the exact text both sides digest.
//
// Four lines, newline-separated, in this order and no other:
//
//	SDN-SIGNED-REQUEST/v1
//	PUT
//	/api/v1/cellular/credentials/opencellid?x=1
//	<lowercase hex sha256 of the raw request body>
//
// The method binds the verb (a signature for a PUT cannot authorise the
// DELETE), the RequestURI binds the target INCLUDING its query, and the body
// digest binds the payload. An empty body digests as the SHA-256 of no bytes,
// so "no body" is a value rather than an absence.
func canonicalRequestString(method string, requestURI string, bodyDigest []byte) string {
	return strings.Join([]string{
		SignedRequestCanonicalVersion,
		strings.ToUpper(strings.TrimSpace(method)),
		requestURI,
		hex.EncodeToString(bodyDigest),
	}, "\n")
}

// signedRequestMessage is what the operator's Ed25519 key actually signs:
// the challenge bytes followed by the digest of the canonical request.
//
// Concatenating the DIGEST rather than the canonical text keeps the signed
// message a fixed 64 bytes and makes the two halves impossible to confuse for
// one another.
func signedRequestMessage(challenge []byte, canonical string) []byte {
	canonicalDigest := sha256.Sum256([]byte(canonical))
	message := make([]byte, 0, len(challenge)+len(canonicalDigest))
	message = append(message, challenge...)
	message = append(message, canonicalDigest[:]...)
	return message
}

// readAndRestoreBody buffers the request body so it can be digested, then puts
// it back so the real handler reads exactly the bytes the client sent.
func readAndRestoreBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSignedRequestBody+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) > maxSignedRequestBody {
		// Restore what was read so the handler chain still behaves, then
		// refuse: admitting a request whose signature covers only a PREFIX of
		// its body would be worse than not admitting it at all.
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		return nil, fmt.Errorf("signed request body exceeds %d bytes", maxSignedRequestBody)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// sessionFromSignedRequest is admission mode 2.
//
// It returns errNoSignedRequest when the request is not using this mode at all,
// which is the signal for the caller to keep its ordinary "no session" answer.
// Every other failure is an authentication failure and is deliberately
// indistinguishable in the response the wall writes.
func (h *Handler) sessionFromSignedRequest(r *http.Request) (*Session, error) {
	if h == nil || r == nil {
		return nil, errNoSignedRequest
	}
	creds, err := parseSignedRequestAuthorization(r.Header.Get("Authorization"))
	if err != nil {
		return nil, err
	}

	body, err := readAndRestoreBody(r)
	if err != nil {
		return nil, fmt.Errorf("signed request rejected: %w", err)
	}
	bodyDigest := sha256.Sum256(body)

	now := time.Now().UTC()
	h.cleanupChallenges(now)

	// Single use, consumed BEFORE verification: a signature that fails must
	// burn its challenge too, or the header becomes an oracle to grind against.
	h.mu.Lock()
	pending, ok := h.challenges[creds.challengeID]
	if ok {
		delete(h.challenges, creds.challengeID)
	}
	h.mu.Unlock()

	if !ok || pending.expiresAt.Before(now) {
		return nil, errors.New("signed request: unknown or expired challenge")
	}

	// This mode never enrols anybody. A bootstrap challenge is the one path
	// that WRITES a user row, so it is refused here rather than reasoned about.
	if pending.firstAdminBootstrap {
		return nil, errors.New("signed request: first-admin bootstrap must use the ordinary sign-in")
	}

	canonical := canonicalRequestString(r.Method, r.URL.RequestURI(), bodyDigest[:])
	if !ed25519.Verify(pending.pubKey, signedRequestMessage(pending.challenge, canonical), creds.signature) {
		return nil, errors.New("signed request: signature does not cover this request")
	}

	// The node's own root account, recognised at the challenge, needs no store
	// row (§14/§19) — the same rule completeRootSignIn applies.
	if pending.rootAdmin {
		return h.ephemeralSession(pending.xpub, rootTrustLevel, r, now), nil
	}

	if h.userStore == nil {
		return nil, errors.New("signed request: no user store")
	}
	user, err := h.userStore.GetUser(pending.xpub)
	if err != nil || user == nil {
		return nil, errors.New("signed request: not an enrolled operator")
	}

	// The signing key must ALREADY be bound. Binding one is a security-relevant
	// WRITE, and this side channel is read-only by construction: an operator
	// who has never completed an ordinary sign-in signs in the ordinary way
	// once, and every later signed request works.
	bound, nerr := normalizeEd25519PubKeyHex(user.SigningPubKeyHex)
	if nerr != nil || bound == "" {
		return nil, errors.New("signed request: operator has no bound signing key")
	}
	boundRaw, derr := hex.DecodeString(bound)
	if derr != nil || !bytes.Equal(boundRaw, pending.pubKey) {
		return nil, errors.New("signed request: signing key is not the operator's bound key")
	}

	return h.ephemeralSession(user.XPub, user.TrustLevel, r, now), nil
}

// ephemeralSession is a session that exists for the duration of ONE request.
//
// It is never written to the session store, never given a token, and never
// returned to the client. Token is left empty on purpose: nothing downstream
// can mistake it for something to hand back, and maybeRefreshSessionCookie
// finds no cookie to rotate.
func (h *Handler) ephemeralSession(xpub string, trust peers.TrustLevel, r *http.Request, now time.Time) *Session {
	return &Session{
		XPub:       xpub,
		TrustLevel: trust,
		CreatedAt:  now,
		ExpiresAt:  now,
		IPAddress:  clientIPForRequest(r),
		UserAgent:  r.UserAgent(),
	}
}
