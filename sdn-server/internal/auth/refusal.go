package auth

import (
	"net/http"

	"github.com/spacedatanetwork/sdn-server/internal/gateway"
)

// writeAuthRefusal is the ONLY way the auth wall answers 401/403.
//
// Every refusal in middleware.go goes through here so that "a refusal is
// readable by the browser it refuses" is a property of the WALL rather than of
// whichever line happened to be updated. The decoration itself, and the reasons
// it is deliberately narrower than the public-surface one, live in
// internal/gateway/cors.go — the package that already owns the single predicate
// serving the wall, CORS, CSRF and the OpenAPI stamp.
//
// TestEveryAuthWallRefusalGoesThroughWriteAuthRefusal pins that no bare
// writeJSON refusal creeps back into middleware.go.
func writeAuthRefusal(w http.ResponseWriter, r *http.Request, status int, body errorResponse) {
	gateway.DecorateRefusal(w, r)
	writeJSON(w, status, body)
}

// decorateAdmitted makes an ADMITTED response readable to the cross-origin
// browser that authenticated for it — but ONLY for a caller admitted through
// signed-request mode.
//
// The refusal decoration alone is not enough to make the lane usable: a gated
// route is not public on the way out either, so a caller that authenticated
// successfully still could not read its own 200. The gate on `SignedRequest` is
// HERMES's narrowing (2026-08-09 §b): a cookie-authenticated caller gains
// nothing from the decoration, so it does not get one and nobody has to
// re-derive that argument the next time cookie handling is touched.
func decorateAdmitted(w http.ResponseWriter, r *http.Request, session *Session) {
	if session == nil || !session.SignedRequest {
		return
	}
	gateway.DecorateAdmitted(w, r)
}
